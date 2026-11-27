package security

import (
	"path"
	"regexp"
	"strings"
)

// PermissionMode mirrors Claude Code / JumpServer operational profiles.
type PermissionMode string

const (
	ModeReadOnly PermissionMode = "readonly"
	ModeSmart    PermissionMode = "smart"   // Read-only auto-allow; mutating/unknown ask; deny rules block
	ModeAskAll   PermissionMode = "ask_all" // Every execution/write asks user
	ModeTrusted  PermissionMode = "trusted" // Non-denied auto-allow; high-risk still asks, deny rules still block
)

// Decision is the result of evaluating an Agent tool request against PolicyConfig.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

// PolicyConfig stores Agent bridge governance & command firewall rules.
type PolicyConfig struct {
	Mode          PermissionMode `json:"mode"`
	AllowPatterns []string       `json:"allowPatterns,omitempty"` // Glob/prefix patterns e.g. "kubectl get *"
	DenyPatterns  []string       `json:"denyPatterns,omitempty"`  // Always blocked (highest precedence)
	DeniedPaths   []string       `json:"deniedPaths,omitempty"`   // Sensitive remote paths blocked from SFTP read/write
}

func DefaultPolicyConfig() PolicyConfig {
	return PolicyConfig{
		Mode: ModeTrusted,
		DeniedPaths: []string{
			"/etc/shadow",
			"/etc/gshadow",
			"/etc/sudoers",
			"*/.ssh/id_*",
			"*/.ssh/*_key",
			"/proc/*/environ",
			"*/.aws/credentials",
			"*/.kube/config",
		},
	}
}

// CommandSegment represents one parsed atomic command inside a compound shell pipeline/chain.
type CommandSegment struct {
	Raw      string   `json:"raw"`
	Program  string   `json:"program"`
	Args     []string `json:"args"`
	Wrapped  bool     `json:"wrapped"` // true if preceded by sudo/su/doas/env
	HasRedir bool     `json:"hasRedir"`
	Risk     string   `json:"risk"` // "safe", "caution", "danger"
	Reason   string   `json:"reason,omitempty"`
}

// Evaluation holds the structured permission verdict for an Agent tool call.
type Evaluation struct {
	Decision Decision         `json:"decision"`
	Risk     string           `json:"risk"` // "safe", "caution", "danger"
	Reason   string           `json:"reason"`
	Segments []CommandSegment `json:"segments,omitempty"`
}

var (
	// High-risk destructive / remote-code-execution patterns (JumpServer + Claude Code inspired)
	hardDangerPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\brm\s+-[a-z]*[rf][a-z]*\s+(?:/\s*$|/\*|~|/\w+/\*)`),
		regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f|\brm\s+-[a-z]*f[a-z]*r`),
		regexp.MustCompile(`(?i)\b(?:mkfs(?:\.[a-z0-9]+)?|wipefs|fdisk|parted|shred)\b`),
		regexp.MustCompile(`(?i)\bdd\s+[^\n]*\bof=/dev/`),
		regexp.MustCompile(`:\(\)\s*\{`), // Fork bomb
		regexp.MustCompile(`(?i)\bchmod\s+-R\s+777\s+/`),
		regexp.MustCompile(`(?i)\b(?:shutdown|reboot|poweroff|halt|init\s+[06])\b`),
		regexp.MustCompile(`(?i)\biptables\s+-F\b|\bufw\s+disable\b`),
		regexp.MustCompile(`(?i)\bdrop\s+(?:database|table|schema)\b|\btruncate\s+table\b`),
		regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^|\n]*\|\s*(?:sudo\s+)?(?:ba|z|da|a)?sh\b`),
		regexp.MustCompile(`(?i)\bbase64\s+(?:-d|--decode)[^|\n]*\|\s*(?:sudo\s+)?(?:ba|z|da|a)?sh\b`),
		regexp.MustCompile(`(?i)>\s*/dev/(?:sd[a-z]|nvme|vd[a-z]|hd[a-z])`),
	}

	// Strictly read-only POSIX / Linux inspection commands
	readOnlyPrograms = map[string]bool{
		"ls": true, "ll": true, "la": true, "dir": true, "pwd": true, "whoami": true, "id": true,
		"uname": true, "hostname": true, "uptime": true, "date": true, "cal": true,
		"cat": true, "head": true, "tail": true, "less": true, "more": true, "nl": true, "tac": true, "stat": true, "file": true, "wc": true,
		"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
		"cut": true, "sort": true, "uniq": true, "tr": true, "column": true, "jq": true, "yq": true,
		"df": true, "du": true, "free": true, "lsblk": true, "lscpu": true, "lsmem": true, "lspci": true, "lsusb": true, "nproc": true,
		"ps": true, "pgrep": true, "pstree": true, "top": true, "vmstat": true, "iostat": true, "mpstat": true, "sar": true,
		"ss": true, "netstat": true, "lsof": true, "ping": true, "traceroute": true, "dig": true, "nslookup": true, "host": true, "whois": true,
		"which": true, "whereis": true, "type": true, "echo": true, "printf": true, "true": true, "false": true,
		"md5sum": true, "sha1sum": true, "sha256sum": true, "cksum": true, "diff": true, "cmp": true,
		"dmesg": true, "last": true, "who": true, "w": true, "journalctl": true, "readlink": true, "realpath": true, "basename": true, "dirname": true,
	}

	// Subcommand-scoped read-only tools (e.g. `docker ps`, `git status`, `systemctl status`, `kubectl get`)
	readOnlySubcommands = map[string]map[string]bool{
		"git":       {"status": true, "log": true, "diff": true, "branch": true, "tag": true, "show": true, "rev-parse": true, "remote": true},
		"docker":    {"ps": true, "images": true, "logs": true, "inspect": true, "stats": true, "info": true, "version": true, "top": true, "port": true, "network": true, "volume": true},
		"podman":    {"ps": true, "images": true, "logs": true, "inspect": true, "stats": true, "info": true, "version": true},
		"systemctl": {"status": true, "is-active": true, "is-enabled": true, "list-units": true, "list-unit-files": true, "list-timers": true, "show": true, "cat": true},
		"kubectl":   {"get": true, "describe": true, "logs": true, "top": true, "explain": true, "version": true, "cluster-info": true},
		"nginx":     {"-t": true, "-T": true, "-v": true, "-V": true},
		"ip":        {"addr": true, "a": true, "route": true, "r": true, "link": true, "l": true, "neigh": true},
		"ufw":       {"status": true},
		"crontab":   {"-l": true},
		"find":      {}, // handled specially to block -delete / -exec
	}

	// Commands that can execute arbitrary subshells or mutate silently
	shellWrappers = map[string]bool{
		"sh": true, "bash": true, "zsh": true, "dash": true, "ash": true, "eval": true, "exec": true,
		"python": true, "python3": true, "perl": true, "ruby": true, "node": true, "php": true,
	}
)

// SplitShellSegments lexes a shell command line into atomic command segments, respecting quotes,
// operators (&&, ||, ;, |, newline), redirections, and embedded $(...) / backtick substitutions.
func SplitShellSegments(command string) []CommandSegment {
	var rawChunks []string
	var cur strings.Builder
	var subshells []string

	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			rawChunks = append(rawChunks, s)
		}
		cur.Reset()
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if escaped {
			cur.WriteRune(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			cur.WriteRune(ch)
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			cur.WriteRune(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			cur.WriteRune(ch)
			continue
		}
		// Extract $(...) subshell expressions even inside double quotes!
		if !inSingle && ch == '$' && i+1 < len(runes) && runes[i+1] == '(' {
			depth := 1
			j := i + 2
			for j < len(runes) && depth > 0 {
				if runes[j] == '(' {
					depth++
				} else if runes[j] == ')' {
					depth--
				}
				j++
			}
			inner := strings.TrimSpace(string(runes[i+2 : j-1]))
			if inner != "" {
				subshells = append(subshells, inner)
			}
			cur.WriteString(string(runes[i:j]))
			i = j - 1
			continue
		}
		// Extract backtick subshell expressions
		if !inSingle && ch == '`' {
			j := i + 1
			for j < len(runes) && runes[j] != '`' {
				j++
			}
			if j < len(runes) {
				inner := strings.TrimSpace(string(runes[i+1 : j]))
				if inner != "" {
					subshells = append(subshells, inner)
				}
				cur.WriteString(string(runes[i : j+1]))
				i = j
				continue
			}
		}

		if !inSingle && !inDouble {
			if ch == ';' || ch == '\n' {
				flush()
				continue
			}
			if (ch == '&' && i+1 < len(runes) && runes[i+1] == '&') ||
				(ch == '|' && i+1 < len(runes) && runes[i+1] == '|') {
				flush()
				i++
				continue
			}
			if ch == '|' || ch == '&' {
				flush()
				continue
			}
		}
		cur.WriteRune(ch)
	}
	flush()

	segments := make([]CommandSegment, 0, len(rawChunks)+len(subshells))
	for _, chunk := range rawChunks {
		segments = append(segments, analyzeAtomicSegment(chunk))
	}
	for _, sub := range subshells {
		for _, nested := range SplitShellSegments(sub) {
			segments = append(segments, nested)
		}
	}
	return segments
}

func analyzeAtomicSegment(raw string) CommandSegment {
	tokens := tokenizeSimple(raw)
	seg := CommandSegment{Raw: raw, Risk: "caution"}
	if len(tokens) == 0 {
		seg.Risk = "safe"
		return seg
	}

	// Detect file-write output redirections (> or >>, excluding harmless 2>/dev/null or 2>&1)
	cleaned := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "2>/dev/null" || tok == "2>&1" || tok == ">/dev/null" || tok == "1>/dev/null" {
			continue
		}
		if strings.HasPrefix(tok, ">") || strings.HasPrefix(tok, "1>") || strings.HasPrefix(tok, "2>") || strings.HasPrefix(tok, "&>") {
			if strings.HasSuffix(tok, "/dev/null") || strings.HasSuffix(tok, "&1") {
				continue
			}
			// Check next token for `/dev/null`
			if (tok == ">" || tok == ">>" || tok == "2>" || tok == "1>") && i+1 < len(tokens) && tokens[i+1] == "/dev/null" {
				i++
				continue
			}
			seg.HasRedir = true
		}
		cleaned = append(cleaned, tok)
	}

	// Strip leading VAR=val env assignments & sudo/env/nohup/nice wrappers
	idx := 0
	for idx < len(cleaned) {
		tok := cleaned[idx]
		if strings.Contains(tok, "=") && !strings.HasPrefix(tok, "-") && !strings.HasPrefix(tok, "/") {
			idx++
			continue
		}
		base := path.Base(tok)
		if base == "sudo" || base == "doas" || base == "env" || base == "nohup" || base == "nice" || base == "command" || base == "time" {
			if base == "sudo" || base == "doas" {
				seg.Wrapped = true
			}
			idx++
			// Skip wrapper flags like `sudo -u root`
			for idx < len(cleaned) && strings.HasPrefix(cleaned[idx], "-") {
				if (cleaned[idx] == "-u" || cleaned[idx] == "-g" || cleaned[idx] == "-n") && idx+1 < len(cleaned) {
					idx += 2
				} else {
					idx++
				}
			}
			continue
		}
		break
	}

	if idx >= len(cleaned) {
		seg.Risk = "caution"
		seg.Reason = "包含环境赋值或提权前缀"
		return seg
	}

	seg.Program = path.Base(cleaned[idx])
	seg.Args = cleaned[idx+1:]

	// 1. Check hard danger patterns on atomic segment
	for _, re := range hardDangerPatterns {
		if re.MatchString(raw) {
			seg.Risk = "danger"
			seg.Reason = "匹配高危破坏性操作模式"
			return seg
		}
	}

	// 2. Shell interpreters / arbitrary code execution
	if shellWrappers[seg.Program] {
		seg.Risk = "caution"
		seg.Reason = "调用脚本或子 Shell 解释器"
		if seg.Wrapped {
			seg.Risk = "danger"
			seg.Reason = "提权执行脚本或子 Shell"
		}
		return seg
	}

	// 3. Output redirection writes to filesystem -> never read-only
	if seg.HasRedir {
		seg.Risk = "caution"
		seg.Reason = "包含文件重定向写入"
		return seg
	}

	// 4. Special read-only check for `find` (block -delete / -exec / -execdir / -ok)
	if seg.Program == "find" {
		for _, a := range seg.Args {
			if a == "-delete" || a == "-exec" || a == "-execdir" || a == "-ok" || a == "-okdir" || a == "-fls" || a == "-fprint" {
				seg.Risk = "caution"
				seg.Reason = "find 包含修改或执行参数 " + a
				return seg
			}
		}
		seg.Risk = "safe"
		return seg
	}

	// 5. Special read-only check for `sed` / `awk` (block `sed -i` in-place write or awk system())
	if seg.Program == "sed" {
		for _, a := range seg.Args {
			if a == "-i" || strings.HasPrefix(a, "-i") || a == "--in-place" {
				seg.Risk = "caution"
				seg.Reason = "sed 原地修改文件"
				return seg
			}
		}
		seg.Risk = "safe"
		return seg
	}
	if seg.Program == "awk" || seg.Program == "gawk" {
		lower := strings.ToLower(raw)
		if strings.Contains(lower, "system(") || strings.Contains(lower, "getline") {
			seg.Risk = "caution"
			seg.Reason = "awk 包含外部命令调用"
			return seg
		}
		seg.Risk = "safe"
		return seg
	}

	// 6. Strictly read-only binaries
	if readOnlyPrograms[seg.Program] {
		if seg.Wrapped {
			// `sudo tail` or `sudo journalctl` is still cautious because of sudo elevation
			seg.Risk = "caution"
			seg.Reason = "使用 sudo 提权执行"
			return seg
		}
		seg.Risk = "safe"
		return seg
	}

	// 7. Subcommand-scoped read-only binaries (docker ps, systemctl status, kubectl get, etc.)
	if subs, ok := readOnlySubcommands[seg.Program]; ok && len(seg.Args) > 0 {
		first := seg.Args[0]
		if subs[first] && !seg.Wrapped {
			// Guard `docker network/volume` to only `ls` or `inspect`
			if (seg.Program == "docker" || seg.Program == "podman") && (first == "network" || first == "volume") {
				if len(seg.Args) >= 2 && (seg.Args[1] == "ls" || seg.Args[1] == "inspect") {
					seg.Risk = "safe"
					return seg
				}
				seg.Risk = "caution"
				seg.Reason = "可能修改容器网络或存储卷"
				return seg
			}
			seg.Risk = "safe"
			return seg
		}
	}

	// 8. Known mutating / dangerous programs
	switch seg.Program {
	case "rm", "rmdir", "dd", "mkfs", "fdisk", "parted", "reboot", "shutdown", "poweroff", "halt", "killall", "pkill":
		seg.Risk = "danger"
		seg.Reason = "潜在破坏性或中断服务命令：" + seg.Program
	default:
		seg.Risk = "caution"
		if seg.Wrapped {
			seg.Reason = "提权执行修改操作：" + seg.Program
		} else {
			seg.Reason = "非只读或未分类命令：" + seg.Program
		}
	}
	return seg
}

func tokenizeSimple(s string) []string {
	var out []string
	var cur strings.Builder
	inS, inD, esc := false, false, false
	for _, r := range s {
		if esc {
			cur.WriteRune(r)
			esc = false
			continue
		}
		if r == '\\' && !inS {
			esc = true
			continue
		}
		if r == '\'' && !inD {
			inS = !inS
			continue
		}
		if r == '"' && !inS {
			inD = !inD
			continue
		}
		if !inS && !inD && (r == ' ' || r == '\t') {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// MatchGlob performs case-insensitive glob matching (* and ?) for allow/deny command & path patterns.
func MatchGlob(pattern, target string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	// If pattern has no wildcard, treat as prefix or exact match on normalized whitespace
	if !strings.ContainsAny(pattern, "*?") {
		p := strings.ToLower(pattern)
		t := strings.ToLower(strings.TrimSpace(target))
		return t == p || strings.HasPrefix(t, p+" ") || strings.HasPrefix(t, p+"/")
	}
	return wildcardMatch(strings.ToLower(pattern), strings.ToLower(strings.TrimSpace(target)))
}

func wildcardMatch(pat, str string) bool {
	p, s := 0, 0
	starIdx, matchIdx := -1, 0
	for s < len(str) {
		if p < len(pat) && (pat[p] == '?' || pat[p] == str[s]) {
			p++
			s++
		} else if p < len(pat) && pat[p] == '*' {
			starIdx = p
			matchIdx = s
			p++
		} else if starIdx != -1 {
			p = starIdx + 1
			matchIdx++
			s = matchIdx
		} else {
			return false
		}
	}
	for p < len(pat) && pat[p] == '*' {
		p++
	}
	return p == len(pat)
}

func effectiveMode(globalMode PermissionMode, hostAccess string) (PermissionMode, bool) {
	switch strings.ToLower(strings.TrimSpace(hostAccess)) {
	case "deny":
		return ModeReadOnly, false
	case "readonly":
		return ModeReadOnly, true
	default:
		if globalMode == "" {
			return ModeSmart, true
		}
		return globalMode, true
	}
}

// EvaluateCommand applies Claude Code precedence (Deny > Ask > Allow) over all parsed shell subcommands.
func EvaluateCommand(command string, cfg PolicyConfig, hostAccess string) Evaluation {
	mode, allowedHost := effectiveMode(cfg.Mode, hostAccess)
	if !allowedHost {
		return Evaluation{Decision: DecisionDeny, Risk: "danger", Reason: "该主机已禁止 Agent 访问"}
	}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return Evaluation{Decision: DecisionDeny, Risk: "safe", Reason: "命令为空"}
	}

	// 1. Top-level full-line hard danger checks (e.g. curl ... | bash spanning pipe)
	fullDanger := false
	for _, re := range hardDangerPatterns {
		if re.MatchString(trimmed) {
			fullDanger = true
			break
		}
	}

	// 2. Structural split & per-segment risk analysis
	segments := SplitShellSegments(trimmed)
	highestRisk := "safe"
	reason := "只读查询命令"
	rank := map[string]int{"safe": 0, "caution": 1, "danger": 2}
	for _, seg := range segments {
		if rank[seg.Risk] > rank[highestRisk] {
			highestRisk = seg.Risk
			if seg.Reason != "" {
				reason = seg.Reason
			}
		}
	}
	if fullDanger {
		highestRisk = "danger"
		reason = "检测到高危组合或远程脚本管道执行"
	}

	// 3. Explicit user DENY rules (Highest precedence — ALWAYS deny!)
	for _, pat := range cfg.DenyPatterns {
		if MatchGlob(pat, trimmed) {
			return Evaluation{Decision: DecisionDeny, Risk: "danger", Reason: "命中禁止规则：" + pat, Segments: segments}
		}
		for _, seg := range segments {
			if MatchGlob(pat, seg.Raw) {
				return Evaluation{Decision: DecisionDeny, Risk: "danger", Reason: "子命令命中禁止规则：" + pat, Segments: segments}
			}
		}
	}

	// 4. Explicit user ALLOW rules (only if not danger and not in readonly mode blocking writes)
	userAllowed := false
	if highestRisk != "danger" {
		for _, pat := range cfg.AllowPatterns {
			if MatchGlob(pat, trimmed) {
				userAllowed = true
				break
			}
		}
	}

	// 5. Evaluate against effective PermissionMode
	switch mode {
	case ModeReadOnly:
		if highestRisk == "safe" {
			return Evaluation{Decision: DecisionAllow, Risk: "safe", Reason: "只读模式自动放行", Segments: segments}
		}
		return Evaluation{Decision: DecisionDeny, Risk: highestRisk, Reason: "只读模式拒绝修改或高危命令（" + reason + "）", Segments: segments}

	case ModeAskAll:
		return Evaluation{Decision: DecisionAsk, Risk: highestRisk, Reason: "严格审批模式需人工确认（" + reason + "）", Segments: segments}

	case ModeTrusted:
		if highestRisk == "danger" {
			return Evaluation{Decision: DecisionAsk, Risk: "danger", Reason: "高危操作在信任模式下仍需人工确认（" + reason + "）", Segments: segments}
		}
		return Evaluation{Decision: DecisionAllow, Risk: highestRisk, Reason: "信任模式自动放行", Segments: segments}

	default: // ModeSmart
		if highestRisk == "safe" || userAllowed {
			return Evaluation{Decision: DecisionAllow, Risk: highestRisk, Reason: "只读或白名单规则自动放行", Segments: segments}
		}
		return Evaluation{Decision: DecisionAsk, Risk: highestRisk, Reason: reason, Segments: segments}
	}
}

// EvaluatePath checks SFTP read/write operations against DeniedPaths and permission mode.
func EvaluatePath(remotePath string, write bool, cfg PolicyConfig, hostAccess string) Evaluation {
	mode, allowedHost := effectiveMode(cfg.Mode, hostAccess)
	if !allowedHost {
		return Evaluation{Decision: DecisionDeny, Risk: "danger", Reason: "该主机已禁止 Agent 访问"}
	}
	cleaned := path.Clean(strings.TrimSpace(remotePath))
	for _, pat := range cfg.DeniedPaths {
		if MatchGlob(pat, cleaned) || MatchGlob(pat, path.Base(cleaned)) {
			return Evaluation{Decision: DecisionDeny, Risk: "danger", Reason: "命中敏感路径保护规则：" + pat}
		}
	}
	if !write {
		if mode == ModeAskAll {
			return Evaluation{Decision: DecisionAsk, Risk: "safe", Reason: "严格审批模式读取远端文件"}
		}
		return Evaluation{Decision: DecisionAllow, Risk: "safe", Reason: "只读文件读取自动放行"}
	}
	// Write operation
	switch mode {
	case ModeReadOnly:
		return Evaluation{Decision: DecisionDeny, Risk: "caution", Reason: "只读模式禁止写入远端文件"}
	case ModeTrusted:
		return Evaluation{Decision: DecisionAllow, Risk: "caution", Reason: "信任模式允许写入远端文件"}
	default:
		return Evaluation{Decision: DecisionAsk, Risk: "caution", Reason: "写入远端文件需人工确认"}
	}
}

// EvaluateMutatingTool handles non-shell mutating operations (e.g. connect, tunnel start/stop).
func EvaluateMutatingTool(actionDesc string, cfg PolicyConfig, hostAccess string) Evaluation {
	mode, allowedHost := effectiveMode(cfg.Mode, hostAccess)
	if !allowedHost {
		return Evaluation{Decision: DecisionDeny, Risk: "danger", Reason: "该主机已禁止 Agent 访问"}
	}
	switch mode {
	case ModeReadOnly:
		return Evaluation{Decision: DecisionDeny, Risk: "caution", Reason: "只读模式禁止：" + actionDesc}
	case ModeTrusted:
		return Evaluation{Decision: DecisionAllow, Risk: "caution", Reason: "信任模式放行：" + actionDesc}
	default:
		return Evaluation{Decision: DecisionAsk, Risk: "caution", Reason: actionDesc + " 需人工确认"}
	}
}
