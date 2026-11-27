package local

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const utf8BOM = "\xef\xbb\xbf"

var netcattyCSVHeader = []string{
	"Groups",
	"Label",
	"Tags",
	"Notes",
	"Hostname/IP",
	"Protocol",
	"Port",
	"Username",
	"Password",
	"KeyPath",
	"Passphrase",
	"Proxy",
	"ProxyJump",
	"LocalShell",
	"SshInputCompatibility",
}

func formatCSVHostname(host string) string {
	clean, err := cleanHost(host)
	if err == nil {
		host = clean
	} else {
		host = strings.TrimSpace(host)
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}

func formatCSVProxy(p Profile) string {
	switch strings.ToLower(strings.TrimSpace(p.ProxyType)) {
	case "direct":
		return "direct"
	case "system":
		return "system"
	case "socks5", "http":
		host := strings.TrimSpace(p.ProxyHost)
		if host == "" {
			return ""
		}
		scheme := strings.ToLower(strings.TrimSpace(p.ProxyType))
		port := p.ProxyPort
		if port < 1 || port > 65535 {
			if scheme == "socks5" {
				port = 1080
			} else {
				port = 8080
			}
		}
		hostPart := formatCSVHostname(host)
		userPart := ""
		if u := strings.TrimSpace(p.ProxyUser); u != "" {
			userPart = url.QueryEscape(u) + "@"
		}
		return fmt.Sprintf("%s://%s%s:%d", scheme, userPart, hostPart, port)
	default:
		return ""
	}
}

func formatCSVProxyJumpHop(p Profile) string {
	host := strings.TrimSpace(p.Host)
	if host == "" {
		return ""
	}
	port := p.Port
	if port < 1 || port > 65535 {
		port = 22
	}
	hostPart := formatCSVHostname(host)
	user := strings.TrimSpace(p.User)
	base := hostPart
	if user != "" && !strings.ContainsAny(user, " \t\r\n,@#") {
		base = user + "@" + hostPart
	}
	if port == 22 {
		return base
	}
	return fmt.Sprintf("%s:%d", base, port)
}

func formatCSVProxyJumpChain(p Profile, byID map[string]Profile) (string, error) {
	if p.JumpHostID == "" {
		return "", nil
	}
	var hops []string
	seen := map[string]bool{p.ID: true}
	cur := p.JumpHostID
	for cur != "" {
		if seen[cur] || len(hops) >= 5 {
			return "", errors.New("跳板机引用循环或超过五级，请检查后导出")
		}
		seen[cur] = true
		hop, ok := byID[cur]
		if !ok {
			return "", errors.New("未找到所配置的跳板机，请检查后导出")
		}
		formatted := formatCSVProxyJumpHop(hop)
		if formatted == "" {
			return "", nil
		}
		// Jump chain runs outer-most bastion first (A,B -> connect A then B then target).
		hops = append([]string{formatted}, hops...)
		cur = hop.JumpHostID
	}
	return strings.Join(hops, ","), nil
}

// Guard spreadsheet metadata columns against formula injection while preserving credentials verbatim.
func guardCSVFormula(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	default:
		return v
	}
}

func renderNetcattyCSV(selected, all []Profile, secrets map[string]string, includeSecrets bool) ([]byte, int, []string, error) {
	needed, err := sshSelection(selected, all)
	if err != nil {
		return nil, 0, nil, err
	}
	byID := make(map[string]Profile, len(all))
	for _, p := range all {
		byID[p.ID] = p
	}
	// Preserve saved profile order while including every dependent jump host cleanly.
	ordered := make([]Profile, 0, len(needed))
	for _, p := range all {
		if _, ok := needed[p.ID]; ok {
			ordered = append(ordered, p)
		}
	}
	var warnings []string
	if extra := len(ordered) - len(selected); extra > 0 {
		warnings = append(warnings, fmt.Sprintf("已一并导出 %d 台依赖的跳板机。", extra))
	}

	var buf bytes.Buffer
	buf.WriteString(utf8BOM)
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	if err := w.Write(netcattyCSVHeader); err != nil {
		return nil, 0, nil, err
	}
	for _, p := range ordered {
		label := strings.TrimSpace(p.Name)
		if label == "" {
			label = strings.TrimSpace(p.Host)
		}
		port := p.Port
		if port < 1 || port > 65535 {
			port = 22
		}
		var password, keyPath, passphrase string
		switch strings.ToLower(strings.TrimSpace(p.Auth)) {
		case "key":
			keyPath = strings.TrimSpace(p.KeyPath)
			if includeSecrets {
				passphrase = secrets[p.ID]
			}
		case "agent":
			// Agent uses OS SSH agent; no password or static key file.
		default:
			if includeSecrets {
				password = secrets[p.ID]
			}
		}
		jumpChain, err := formatCSVProxyJumpChain(p, byID)
		if err != nil {
			return nil, 0, nil, err
		}
		row := []string{
			guardCSVFormula(strings.TrimSpace(p.Group)),
			guardCSVFormula(label),
			guardCSVFormula(strings.Join(p.Tags, ",")),
			guardCSVFormula(strings.TrimSpace(p.Notes)),
			formatCSVHostname(p.Host),
			"ssh",
			strconv.Itoa(port),
			strings.TrimSpace(p.User),
			password,
			keyPath,
			passphrase,
			formatCSVProxy(p),
			jumpChain,
			"",
			"",
		}
		if err := w.Write(row); err != nil {
			return nil, 0, nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, 0, nil, err
	}
	return buf.Bytes(), len(ordered), warnings, nil
}
