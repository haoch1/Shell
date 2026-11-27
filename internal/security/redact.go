// Package security implements secret/PII redaction (inspired by Gitleaks, TruffleHog, and Presidio),
// structural shell command risk analysis & tiered permission governance (inspired by Claude Code and JumpServer),
// and local JSONL audit logging for AI Agent and MCP invocations.
package security

import (
	"math"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// RedactionConfig controls automatic masking before text leaves Shell toward any AI model or external Agent.
type RedactionConfig struct {
	Enabled        bool     `json:"enabled"`
	MaskSecrets    bool     `json:"maskSecrets"`
	MaskPII        bool     `json:"maskPII"`
	MaskIPs        bool     `json:"maskIPs"`
	CustomPatterns []string `json:"customPatterns,omitempty"`
	Allowlist      []string `json:"allowlist,omitempty"`
}

func DefaultRedactionConfig() RedactionConfig {
	return RedactionConfig{
		Enabled:     true,
		MaskSecrets: true,
		MaskPII:     true,
		MaskIPs:     false,
	}
}

// RedactResult holds redacted output along with per-category hit counts.
type RedactResult struct {
	Text  string         `json:"text"`
	Count int            `json:"count"`
	Hits  map[string]int `json:"hits"`
}

type secretRule struct {
	tag   string
	re    *regexp.Regexp
	group int // 0 = replace entire match; >0 = replace only capture group while preserving prefix/key
}

var (
	// Multi-line private key block (PEM / OpenSSH / PGP)
	pemPrivateKeyRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)

	// High-precision cloud/SaaS token patterns (curated from Gitleaks ruleset)
	tokenRules = []secretRule{
		{tag: "AWS_KEY", re: regexp.MustCompile(`\b(?:AKIA|ASIA|AIDA|AROA|AIPA|ANPA|ANVA)[0-9A-Z]{16}\b`)},
		{tag: "GITHUB_TOKEN", re: regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,255}\b|\bgithub_pat_[A-Za-z0-9_]{22,255}\b`)},
		{tag: "GITLAB_TOKEN", re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,64}\b`)},
		{tag: "SLACK_TOKEN", re: regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z\-]{10,256}\b`)},
		{tag: "OPENAI_KEY", re: regexp.MustCompile(`\bsk-(?:proj-|svcacct-|ant-api\d+-)?[A-Za-z0-9_\-]{20,200}\b`)},
		{tag: "STRIPE_KEY", re: regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[0-9A-Za-z]{20,99}\b`)},
		{tag: "GCP_API_KEY", re: regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{35}\b`)},
		{tag: "ALIYUN_KEY", re: regexp.MustCompile(`\bLTAI[0-9A-Za-z]{12,24}\b`)},
		{tag: "TENCENT_KEY", re: regexp.MustCompile(`\bAKID[0-9A-Za-z]{28,40}\b`)},
		{tag: "HF_TOKEN", re: regexp.MustCompile(`\bhf_[A-Za-z0-9]{24,64}\b`)},
		{tag: "JWT", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\b`)},
	}

	// HTTP Authorization / Cookie / Bearer headers
	authHeaderRe = regexp.MustCompile(`(?im)^([ \t]*(?:Authorization|Proxy-Authorization|X-Api-Key|X-Auth-Token|Cookie|Set-Cookie)[ \t]*:[ \t]*(?:Bearer[ \t]+|Basic[ \t]+|Token[ \t]+)?)([^\r\n]{6,})`)

	// Connection strings with embedded password: scheme://user:password@host
	dsnSecretRe = regexp.MustCompile(`(?i)\b((?:postgres(?:ql)?|mysql|mariadb|mongodb(?:\+srv)?|redis|rediss|amqp|amqps|mssql|ftp|sftp|http|https)://[^:\s/@]+:)([^@\s/'"]{3,})(@)`)

	// Key-value config / .env / JSON / YAML / CLI flag assignments for secrets
	kvSecretRe = regexp.MustCompile(`(?i)((?:^|[\s,;{"'])(?:[A-Z0-9_.-]*(?:PASSWORD|PASSWD|PWD|SECRET|API_?KEY|ACCESS_?KEY|AUTH_?TOKEN|PRIVATE_?KEY|CLIENT_?SECRET|TOKEN|DB_PASS|MYSQL_PWD|PGPASSWORD|REDIS_PASS)[A-Z0-9_.-]*)\s*["']?\s*(?:=|:)\s*["']?)([^\s,"'}{;\]\r\n]{4,})`)
	cliPassRe  = regexp.MustCompile(`(?i)((?:^|\s)(?:--password|--passwd|--token|--api-key|--secret|-p)(?:=|\s+))(['"]?)([^\s'"\r\n]{4,})(['"]?)`)

	// PII patterns (Presidio-inspired with strict boundary & checksum validation)
	emailRe      = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]{1,64}@[A-Za-z0-9.\-]{1,253}\.[A-Za-z]{2,16}\b`)
	cnMobileRe   = regexp.MustCompile(`(?:^|[^0-9A-Za-z])(1[3-9]\d{9})(?:[^0-9A-Za-z]|$)`)
	cnIDCardRe   = regexp.MustCompile(`(?:^|[^0-9A-Za-z])([1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx])(?:[^0-9A-Za-z]|$)`)
	cardNumberRe = regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`)
	ipv4Re       = regexp.MustCompile(`\b((?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\b`)
)

var (
	regexCacheMu sync.RWMutex
	regexCache   = make(map[string]*regexp.Regexp)
)

const maxCompiledRegexCache = 256

func compileCachedRegex(pattern string) (*regexp.Regexp, bool) {
	regexCacheMu.RLock()
	re, ok := regexCache[pattern]
	regexCacheMu.RUnlock()
	if ok {
		return re, re != nil
	}
	compiled, err := regexp.Compile(pattern)
	regexCacheMu.Lock()
	if len(regexCache) >= maxCompiledRegexCache {
		clear(regexCache)
	}
	if err != nil {
		regexCache[pattern] = nil
		regexCacheMu.Unlock()
		return nil, false
	}
	regexCache[pattern] = compiled
	regexCacheMu.Unlock()
	return compiled, true
}

// Redact scrubs secrets, credentials, PII, and optional IP addresses from text according to cfg.
func Redact(input string, cfg RedactionConfig) RedactResult {
	res := RedactResult{Text: input, Hits: map[string]int{}}
	if !cfg.Enabled || input == "" {
		return res
	}

	var allowRes []*regexp.Regexp
	for _, raw := range cfg.Allowlist {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if r, ok := compileCachedRegex(raw); ok {
			allowRes = append(allowRes, r)
		}
	}
	isAllowed := func(s string) bool {
		if strings.HasPrefix(s, "[REDACTED:") {
			return true
		}
		for _, r := range allowRes {
			if r.MatchString(s) {
				return true
			}
		}
		return false
	}

	out := input
	record := func(tag string) {
		res.Count++
		res.Hits[tag]++
	}

	if cfg.MaskSecrets {
		// 1. PEM Private Key blocks
		out = pemPrivateKeyRe.ReplaceAllStringFunc(out, func(m string) string {
			if isAllowed(m) {
				return m
			}
			record("PRIVATE_KEY")
			return "[REDACTED:PRIVATE_KEY]"
		})

		// 2. Known high-precision cloud/SaaS tokens
		for _, rule := range tokenRules {
			out = rule.re.ReplaceAllStringFunc(out, func(m string) string {
				if isAllowed(m) {
					return m
				}
				record(rule.tag)
				return "[REDACTED:" + rule.tag + "]"
			})
		}

		// 3. Database / URL credentials (preserve host & username)
		out = dsnSecretRe.ReplaceAllStringFunc(out, func(m string) string {
			sub := dsnSecretRe.FindStringSubmatch(m)
			if len(sub) < 4 || isAllowed(sub[2]) {
				return m
			}
			record("DSN_CREDENTIAL")
			return sub[1] + "[REDACTED:CREDENTIAL]" + sub[3]
		})

		// 4. HTTP Authorization / Cookie headers
		out = authHeaderRe.ReplaceAllStringFunc(out, func(m string) string {
			sub := authHeaderRe.FindStringSubmatch(m)
			if len(sub) < 3 || isAllowed(sub[2]) {
				return m
			}
			record("AUTH_HEADER")
			return sub[1] + "[REDACTED:AUTH]"
		})

		// 5. Key-value secrets (.env / JSON / YAML)
		out = kvSecretRe.ReplaceAllStringFunc(out, func(m string) string {
			sub := kvSecretRe.FindStringSubmatch(m)
			if len(sub) < 3 {
				return m
			}
			val := sub[2]
			if isAllowed(val) || isPlaceholderValue(val) {
				return m
			}
			// Guard against false-positive boolean/numeric config flags like "password_required=true" or "token_ttl=3600"
			if isTrivialLiteral(val) && shannonEntropy(val) < 2.2 {
				return m
			}
			record("SECRET_VALUE")
			return sub[1] + "[REDACTED:SECRET]"
		})

		// 6. CLI password/token flags (--password=xxx, --token xxx)
		out = cliPassRe.ReplaceAllStringFunc(out, func(m string) string {
			sub := cliPassRe.FindStringSubmatch(m)
			if len(sub) < 5 || isAllowed(sub[3]) || isPlaceholderValue(sub[3]) {
				return m
			}
			// Skip numeric port flags like `ssh -p 2222`
			if strings.HasSuffix(strings.TrimSpace(sub[1]), "-p") && isDigitsOnly(sub[3]) {
				return m
			}
			record("CLI_SECRET")
			return sub[1] + sub[2] + "[REDACTED:SECRET]" + sub[4]
		})
	}

	if cfg.MaskPII {
		// 7. Email addresses
		out = emailRe.ReplaceAllStringFunc(out, func(m string) string {
			if isAllowed(m) {
				return m
			}
			record("EMAIL")
			return "[REDACTED:EMAIL]"
		})

		// 8. Chinese 18-digit National ID (checksum verified -> near-zero false positives)
		out = cnIDCardRe.ReplaceAllStringFunc(out, func(m string) string {
			sub := cnIDCardRe.FindStringSubmatch(m)
			if len(sub) < 2 || isAllowed(sub[1]) || !validChineseID(sub[1]) {
				return m
			}
			record("ID_CARD")
			return strings.Replace(m, sub[1], "[REDACTED:ID_CARD]", 1)
		})

		// 9. Chinese 11-digit mobile phone numbers
		out = cnMobileRe.ReplaceAllStringFunc(out, func(m string) string {
			sub := cnMobileRe.FindStringSubmatch(m)
			if len(sub) < 2 || isAllowed(sub[1]) {
				return m
			}
			record("PHONE")
			return strings.Replace(m, sub[1], "[REDACTED:PHONE]", 1)
		})

		// 10. Payment card numbers (Luhn validated -> avoids random log numbers)
		out = cardNumberRe.ReplaceAllStringFunc(out, func(m string) string {
			digits := stripNonDigits(m)
			if len(digits) < 13 || len(digits) > 19 || isAllowed(m) || !validLuhn(digits) {
				return m
			}
			record("CARD_NUMBER")
			return "[REDACTED:CARD]"
		})
	}

	if cfg.MaskIPs {
		out = ipv4Re.ReplaceAllStringFunc(out, func(m string) string {
			if m == "127.0.0.1" || m == "0.0.0.0" || isAllowed(m) {
				return m
			}
			record("IP_ADDR")
			return "[REDACTED:IP]"
		})
	}

	// 11. User-defined custom regex rules
	for _, raw := range cfg.CustomPatterns {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		re, ok := compileCachedRegex(raw)
		if !ok {
			continue
		}
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			if m == "" || isAllowed(m) {
				return m
			}
			record("CUSTOM")
			return "[REDACTED:CUSTOM]"
		})
	}

	res.Text = out
	return res
}

func shannonEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	freq := make(map[rune]float64, len(s))
	n := 0.0
	for _, r := range s {
		freq[r]++
		n++
	}
	var h float64
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

func isPlaceholderValue(v string) bool {
	lower := strings.ToLower(strings.Trim(v, `"'`))
	switch lower {
	case "", "null", "none", "nil", "true", "false", "yes", "no", "undefined", "changeme", "placeholder", "example", "xxx", "****":
		return true
	}
	return strings.HasPrefix(lower, "<") && strings.HasSuffix(lower, ">") ||
		strings.HasPrefix(lower, "${") || strings.HasPrefix(lower, "{{") || strings.HasPrefix(lower, "$")
}

func isTrivialLiteral(v string) bool {
	for _, r := range v {
		if !unicode.IsDigit(r) && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

func isDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func stripNonDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validLuhn(digits string) bool {
	sum := 0
	alt := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

func validChineseID(id string) bool {
	if len(id) != 18 {
		return false
	}
	weights := [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checkChars := "10X98765432"
	sum := 0
	for i := 0; i < 17; i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
		sum += int(id[i]-'0') * weights[i]
	}
	expected := checkChars[sum%11]
	actual := byte(unicode.ToUpper(rune(id[17])))
	return actual == expected
}
