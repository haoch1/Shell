package local

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ImportedHost represents a normalized SSH host candidate parsed from any supported client config file.
type ImportedHost struct {
	Name         string   `json:"name"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	User         string   `json:"user"`
	Auth         string   `json:"auth"` // "password", "key", "agent"
	Password     string   `json:"password,omitempty"`
	KeyPath      string   `json:"keyPath,omitempty"`
	Passphrase   string   `json:"passphrase,omitempty"`
	Group        string   `json:"group,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	ProxyType    string   `json:"proxyType,omitempty"`
	ProxyHost    string   `json:"proxyHost,omitempty"`
	ProxyPort    int      `json:"proxyPort,omitempty"`
	ProxyUser    string   `json:"proxyUser,omitempty"`
	ProxyJump    string   `json:"proxyJump,omitempty"`
	ForwardAgent bool     `json:"forwardAgent,omitempty"`
	KeepAliveSec int      `json:"keepAliveSec,omitempty"`
	Notes        string   `json:"notes,omitempty"`
	SourceFormat string   `json:"sourceFormat"`
	AlreadySaved bool     `json:"alreadySaved"`
}

// ImportPreviewResult reports the detected source tool format and all discovered hosts.
type ImportPreviewResult struct {
	DetectedFormat string         `json:"detectedFormat"`
	Hosts          []ImportedHost `json:"hosts"`
	Warnings       []string       `json:"warnings,omitempty"`
}

const maxImportFileBytes = 16 << 20

// ParseImportFiles auto-detects and parses one or more exported config files from mainstream SSH clients:
// OpenSSH config, Xshell (.xsh / .zip / .xts), MobaXterm (.mxtsessions), FinalShell (.json),
// Termius (.csv / .json), Tabby (.yaml / .yml), Electerm (.json), SecureCRT (.xml / .ini),
// PuTTY / KiTTY (.reg), WindTerm (.sessions), Shell backup (.json), and generic CSV/JSON/text.
func ParseImportFiles(paths []string) (ImportPreviewResult, error) {
	if len(paths) == 0 {
		return ImportPreviewResult{}, errors.New("未选择要导入的配置文件")
	}
	var result ImportPreviewResult
	formats := make(map[string]bool)

	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".zip" || ext == ".xts" {
			sub, err := parseZipArchive(p)
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", filepath.Base(p), err))
				continue
			}
			if sub.DetectedFormat != "" {
				formats[sub.DetectedFormat] = true
			}
			result.Hosts = append(result.Hosts, sub.Hosts...)
			result.Warnings = append(result.Warnings, sub.Warnings...)
			continue
		}
		raw, err := readLimited(p, maxImportFileBytes)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", filepath.Base(p), err))
			continue
		}
		sub, err := ParseImportBytes(filepath.Base(p), raw)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", filepath.Base(p), err))
			continue
		}
		if sub.DetectedFormat != "" {
			formats[sub.DetectedFormat] = true
		}
		result.Hosts = append(result.Hosts, sub.Hosts...)
		result.Warnings = append(result.Warnings, sub.Warnings...)
	}

	if len(formats) == 1 {
		for k := range formats {
			result.DetectedFormat = k
		}
	} else if len(formats) > 1 {
		keys := make([]string, 0, len(formats))
		for k := range formats {
			keys = append(keys, k)
		}
		result.DetectedFormat = strings.Join(keys, " + ")
	}
	if len(result.Hosts) == 0 && len(result.Warnings) > 0 {
		return result, errors.New(strings.Join(result.Warnings, "；"))
	}
	flagExistingHosts(result.Hosts)
	return result, nil
}

// ParseImportBytes auto-detects the SSH client format from file name + payload bytes and parses hosts.
func ParseImportBytes(filename string, raw []byte) (ImportPreviewResult, error) {
	text := decodeTextAuto(raw)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ImportPreviewResult{}, errors.New("文件内容为空")
	}
	ext := strings.ToLower(filepath.Ext(filename))
	lowerName := strings.ToLower(filename)

	// 1. PuTTY / KiTTY .reg
	if ext == ".reg" || strings.HasPrefix(trimmed, "Windows Registry Editor") || strings.Contains(trimmed, `SimonTatham\PuTTY\Sessions`) {
		hosts := parsePuttyReg(text)
		return ImportPreviewResult{DetectedFormat: "PuTTY / KiTTY (.reg)", Hosts: hosts}, nil
	}

	// 2. Xshell .xsh
	if ext == ".xsh" || (strings.Contains(text, "[CONNECTION]") && strings.Contains(text, "[SessionInfo]")) {
		hosts := parseXshellXSH(filename, text, "")
		return ImportPreviewResult{DetectedFormat: "Xshell (.xsh)", Hosts: hosts}, nil
	}

	// 3. MobaXterm .mxtsessions
	if ext == ".mxtsessions" || strings.Contains(text, "[Bookmarks]") && strings.Contains(text, "#109#") {
		hosts := parseMobaXterm(text)
		return ImportPreviewResult{DetectedFormat: "MobaXterm (.mxtsessions)", Hosts: hosts}, nil
	}

	// 4. SecureCRT XML
	if ext == ".xml" || strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<VanDyke") {
		hosts := parseSecureCRTXML(text)
		if len(hosts) > 0 {
			return ImportPreviewResult{DetectedFormat: "SecureCRT (.xml)", Hosts: hosts}, nil
		}
	}

	// 5. SecureCRT single .ini session
	if ext == ".ini" && (strings.Contains(text, `S:"Hostname"=`) || strings.Contains(text, `D:"[SSH2] Port"=`)) {
		hosts := parseSecureCRTINI(filename, text, "")
		return ImportPreviewResult{DetectedFormat: "SecureCRT (.ini)", Hosts: hosts}, nil
	}

	// 6. JSON formats (Electerm, WindTerm, FinalShell, Termius JSON, Shell native, generic JSON array)
	if ext == ".json" || ext == ".sessions" || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if fmtName, hosts, ok := parseJSONFormats(trimmed); ok {
			return ImportPreviewResult{DetectedFormat: fmtName, Hosts: hosts}, nil
		}
	}

	// 7. Tabby YAML config (.yaml / .yml)
	if ext == ".yaml" || ext == ".yml" || (strings.Contains(text, "profiles:") && strings.Contains(text, "type: ssh")) {
		hosts := parseTabbyYAML(text)
		if len(hosts) > 0 {
			return ImportPreviewResult{DetectedFormat: "Tabby (.yaml)", Hosts: hosts}, nil
		}
	}

	// 8. Netcatty / Termius / Royal TS / Generic CSV
	if ext == ".csv" || ext == ".tsv" || looksLikeCSV(trimmed) {
		fmtName, hosts, headerMatched := parseCSVDetailed(trimmed)
		if headerMatched {
			if len(hosts) == 0 {
				return ImportPreviewResult{
					DetectedFormat: fmtName,
					Warnings:       []string{fmt.Sprintf("已识别为 %s，但文件中没有主机记录（仅包含表头）", fmtName)},
				}, nil
			}
			return ImportPreviewResult{DetectedFormat: fmtName, Hosts: hosts}, nil
		}
	}

	// 9. OpenSSH config (config, *.ssh, or contains `Host ` + `HostName `)
	if lowerName == "config" || ext == ".ssh" || ext == ".conf" || containsOpenSSHDirectives(text) {
		hosts := parseOpenSSHConfigText(text)
		if len(hosts) > 0 {
			return ImportPreviewResult{DetectedFormat: "OpenSSH Config", Hosts: hosts}, nil
		}
	}

	// 10. Plain text list (`user@host:port` / `ssh -p 2222 user@host` per line)
	if hosts := parsePlainHostLines(text); len(hosts) > 0 {
		return ImportPreviewResult{DetectedFormat: "纯文本主机列表", Hosts: hosts}, nil
	}

	return ImportPreviewResult{}, errors.New("未能识别该文件格式或文件中未包含有效的 SSH 主机配置")
}

// ImportParsedHosts saves selected ImportedHost entries into Shell profiles.
// When overrideGroup is empty, each host preserves its original folder/group from the source tool!
// Second pass resolves ProxyJump references against newly imported hosts, existing saved hosts,
// multi-hop comma/semicolon chains, or inline `user@host:port` jump addresses.
func ImportParsedHosts(entries []ImportedHost, overrideGroup string) ([]Profile, error) {
	overrideGroup = strings.TrimSpace(overrideGroup)
	type savedEntry struct {
		entry   ImportedHost
		profile Profile
	}
	pairs := make([]savedEntry, 0, len(entries))
	lookup := make(map[string]string, len(entries)*3)

	registerLookup := func(p Profile) {
		add := func(k string) {
			k = strings.ToLower(strings.TrimSpace(k))
			if k != "" {
				if _, exists := lookup[k]; !exists {
					lookup[k] = p.ID
				}
			}
		}
		add(p.Name)
		add(p.Host)
		add(fmt.Sprintf("%s:%d", p.Host, p.Port))
		add(fmt.Sprintf("%s@%s", p.User, p.Host))
		add(fmt.Sprintf("%s@%s:%d", p.User, p.Host, p.Port))
	}

	// Pre-register already-saved hosts so imported configs can reference existing bastions.
	if existing, err := Profiles(); err == nil {
		for _, ep := range existing {
			registerLookup(ep)
		}
	}

	for _, e := range entries {
		grp := strings.TrimSpace(e.Group)
		if overrideGroup != "" {
			grp = overrideGroup
		}
		auth := strings.ToLower(strings.TrimSpace(e.Auth))
		keyPath := strings.TrimSpace(e.KeyPath)
		if keyPath != "" {
			keyPath = expandHome(keyPath)
			if _, err := os.Stat(keyPath); err == nil {
				auth = "key"
			} else if auth == "key" {
				// Private key path from another machine doesn't exist locally; fallback to password cleanly
				auth = "password"
				keyPath = ""
			}
		}
		if auth != "password" && auth != "key" && auth != "agent" {
			auth = "password"
		}
		port := e.Port
		if port < 1 || port > 65535 {
			port = 22
		}
		user := strings.TrimSpace(e.User)
		if user == "" {
			user = "root"
		}
		p := Profile{
			Name:         strings.TrimSpace(e.Name),
			Group:        grp,
			Tags:         e.Tags,
			Host:         strings.TrimSpace(e.Host),
			Port:         port,
			User:         user,
			Auth:         auth,
			KeyPath:      keyPath,
			ProxyType:    strings.TrimSpace(e.ProxyType),
			ProxyHost:    strings.TrimSpace(e.ProxyHost),
			ProxyPort:    e.ProxyPort,
			ProxyUser:    strings.TrimSpace(e.ProxyUser),
			ForwardAgent: e.ForwardAgent,
			KeepAliveSec: e.KeepAliveSec,
			Notes:        strings.TrimSpace(e.Notes),
		}
		saved, err := SaveProfile(p)
		if err != nil {
			continue
		}
		// Immediately encrypt imported password or key passphrase into Windows DPAPI (never written in plaintext).
		if auth == "password" && e.Password != "" {
			_ = RememberSecret(saved, e.Password)
		} else if auth == "key" && e.Passphrase != "" {
			_ = RememberSecret(saved, e.Passphrase)
		}
		// Newly imported hosts take priority over older entries with the same alias.
		if saved.Name != "" {
			lookup[strings.ToLower(saved.Name)] = saved.ID
		}
		registerLookup(saved)
		pairs = append(pairs, savedEntry{entry: e, profile: saved})
	}

	resolveOrCreateJump := func(spec, defaultUser string) string {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			return ""
		}
		if id, ok := lookup[strings.ToLower(spec)]; ok {
			return id
		}
		// Try parsing inline jump syntax: [user@]host[:port] or [user@][ipv6]:port
		jUser := defaultUser
		rest := spec
		if u, h, hasAt := strings.Cut(rest, "@"); hasAt && h != "" {
			jUser = strings.TrimSpace(u)
			rest = strings.TrimSpace(h)
		}
		jHost := rest
		jPort := 22
		if strings.HasPrefix(rest, "[") {
			if closeIdx := strings.IndexByte(rest, ']'); closeIdx > 1 {
				jHost = rest[1:closeIdx]
				if tail := rest[closeIdx+1:]; strings.HasPrefix(tail, ":") {
					if pNum, err := strconv.Atoi(tail[1:]); err == nil && pNum >= 1 && pNum <= 65535 {
						jPort = pNum
					}
				}
			}
		} else if strings.Count(rest, ":") == 1 {
			if h, pStr, ok := strings.Cut(rest, ":"); ok {
				if pNum, err := strconv.Atoi(pStr); err == nil && pNum >= 1 && pNum <= 65535 {
					jHost = h
					jPort = pNum
				}
			}
		}
		if id, ok := lookup[strings.ToLower(jHost)]; ok {
			return id
		}
		if id, ok := lookup[strings.ToLower(fmt.Sprintf("%s:%d", jHost, jPort))]; ok {
			return id
		}
		// Only auto-create an inline jump profile if it looks like a real IP/FQDN (contains '.' or ':').
		if !strings.ContainsAny(jHost, ".:") {
			return ""
		}
		if _, err := cleanHost(jHost); err != nil {
			return ""
		}
		if jUser == "" {
			jUser = "root"
		}
		jp, err := SaveProfile(Profile{
			Name:  jHost,
			Group: "跳板机",
			Host:  jHost,
			Port:  jPort,
			User:  jUser,
			Auth:  "password",
		})
		if err != nil {
			return ""
		}
		registerLookup(jp)
		pairs = append(pairs, savedEntry{profile: jp})
		return jp.ID
	}

	// Second pass: link ProxyJump references (including multi-hop chains like "bastion1,bastion2")
	for i := range pairs {
		rawJump := strings.TrimSpace(pairs[i].entry.ProxyJump)
		if rawJump == "" {
			continue
		}
		hops := strings.FieldsFunc(rawJump, func(r rune) bool { return r == ',' || r == ';' })
		var prevID string
		for _, hop := range hops {
			jid := resolveOrCreateJump(hop, pairs[i].profile.User)
			if jid == "" || jid == pairs[i].profile.ID {
				continue
			}
			// Chain intermediate hops if multi-hop and hop doesn't already have a jump parent
			if prevID != "" && prevID != jid {
				if hp, ok, err := GetProfile(jid); err == nil && ok && hp.JumpHostID == "" {
					hp.JumpHostID = prevID
					_, _ = SaveProfile(hp)
				}
			}
			prevID = jid
		}
		if prevID != "" && prevID != pairs[i].profile.ID {
			pairs[i].profile.JumpHostID = prevID
			if updated, err := SaveProfile(pairs[i].profile); err == nil {
				pairs[i].profile = updated
			}
		}
	}

	created := make([]Profile, 0, len(pairs))
	for _, pr := range pairs {
		created = append(created, pr.profile)
	}
	return created, nil
}

func flagExistingHosts(hosts []ImportedHost) {
	existing, err := Profiles()
	if err != nil {
		return
	}
	byKey := make(map[string]bool, len(existing))
	for _, p := range existing {
		byKey[strings.ToLower(p.User+"@"+p.Host+":"+strconv.Itoa(p.Port))] = true
	}
	for i := range hosts {
		k := strings.ToLower(hosts[i].User + "@" + hosts[i].Host + ":" + strconv.Itoa(hosts[i].Port))
		hosts[i].AlreadySaved = byKey[k]
	}
}

// decodeTextAuto decodes UTF-16LE/UTF-16BE (common in Xshell .xsh & Windows .reg exports) or UTF-8 with BOM.
func decodeTextAuto(raw []byte) string {
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		return decodeUTF16(raw[2:], false)
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		return decodeUTF16(raw[2:], true)
	}
	// Strip UTF-8 BOM
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	return string(raw)
}

func decodeUTF16(b []byte, bigEndian bool) string {
	u16 := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u16 = append(u16, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u16 = append(u16, uint16(b[i])|uint16(b[i+1])<<8)
		}
	}
	return string(utf16.Decode(u16))
}

// --- 1. ZIP / Xshell .xts Archive Parser ---
func parseZipArchive(zipPath string) (ImportPreviewResult, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return ImportPreviewResult{}, fmt.Errorf("无法打开压缩包：%w", err)
	}
	defer r.Close()

	var out ImportPreviewResult
	formats := map[string]bool{}
	for _, f := range r.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 > 4<<20 {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name))
		if ext != ".xsh" && ext != ".ini" && ext != ".json" && ext != ".xml" && ext != ".csv" && ext != ".mxtsessions" && ext != ".reg" && ext != ".yaml" && ext != ".yml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		rc.Close()
		if err != nil {
			continue
		}
		folder := path.Dir(filepath.ToSlash(f.Name))
		if folder == "." || folder == "/" {
			folder = ""
		}
		folder = strings.Trim(strings.TrimPrefix(folder, "Sessions/"), "/")

		if ext == ".xsh" {
			hosts := parseXshellXSH(filepath.Base(f.Name), decodeTextAuto(data), folder)
			if len(hosts) > 0 {
				formats["Xshell 导出包"] = true
				out.Hosts = append(out.Hosts, hosts...)
			}
			continue
		}
		if ext == ".ini" && strings.Contains(decodeTextAuto(data), `S:"Hostname"=`) {
			hosts := parseSecureCRTINI(filepath.Base(f.Name), decodeTextAuto(data), folder)
			if len(hosts) > 0 {
				formats["SecureCRT 导出包"] = true
				out.Hosts = append(out.Hosts, hosts...)
			}
			continue
		}
		if sub, err := ParseImportBytes(filepath.Base(f.Name), data); err == nil && len(sub.Hosts) > 0 {
			for i := range sub.Hosts {
				if sub.Hosts[i].Group == "" && folder != "" {
					sub.Hosts[i].Group = folder
				}
			}
			if sub.DetectedFormat != "" {
				formats[sub.DetectedFormat] = true
			}
			out.Hosts = append(out.Hosts, sub.Hosts...)
		}
	}
	if len(formats) > 0 {
		names := make([]string, 0, len(formats))
		for k := range formats {
			names = append(names, k)
		}
		out.DetectedFormat = strings.Join(names, " + ")
	}
	return out, nil
}

// --- 2. Xshell .xsh ---
func parseXshellXSH(filename, text, group string) []ImportedHost {
	section := ""
	var host, user, proto, desc, authMethod, proxyRaw string
	port := 22
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToUpper(line[1 : len(line)-1])
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch section {
		case "CONNECTION":
			switch strings.ToLower(k) {
			case "host":
				host = v
			case "port":
				if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
					port = p
				}
			case "protocol":
				proto = strings.ToUpper(v)
			case "description":
				desc = v
			}
		case "CONNECTION:AUTHENTICATION":
			switch strings.ToLower(k) {
			case "username":
				user = v
			case "method":
				authMethod = strings.ToLower(v)
			}
		case "CONNECTION:PROXY":
			if strings.EqualFold(k, "proxy") && v != "" && !strings.EqualFold(v, "none") {
				proxyRaw = v
			}
		}
	}
	if host == "" || (proto != "" && proto != "SSH") {
		return nil
	}
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	if name == "" {
		name = host
	}
	if user == "" {
		user = "root"
	}
	auth := "password"
	if strings.Contains(authMethod, "public") || strings.Contains(authMethod, "key") {
		auth = "agent"
	}
	pType, pHost, pPort, pUser := parseProxyURL(proxyRaw)
	proxyJump := ""
	if pType == "" && proxyRaw != "" {
		proxyJump = proxyRaw
	}
	return []ImportedHost{{
		Name:         name,
		Host:         host,
		Port:         port,
		User:         user,
		Auth:         auth,
		Group:        group,
		ProxyType:    pType,
		ProxyHost:    pHost,
		ProxyPort:    pPort,
		ProxyUser:    pUser,
		ProxyJump:    proxyJump,
		Notes:        desc,
		SourceFormat: "Xshell",
	}}
}

// --- 3. MobaXterm .mxtsessions ---
func parseMobaXterm(text string) []ImportedHost {
	var out []ImportedHost
	currentGroup := ""
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentGroup = ""
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(k, "SubRep") {
			currentGroup = strings.ReplaceAll(strings.TrimSpace(v), `\`, `/`)
			continue
		}
		if strings.EqualFold(k, "ImgNum") {
			continue
		}
		// SSH entries in MobaXterm begin with `#109#0%<host>%<port>%<user>%...`
		if !strings.Contains(v, "#109#") {
			continue
		}
		parts := strings.Split(v, "%")
		if len(parts) < 4 {
			continue
		}
		// parts[0] is "#109#0" (protocol 0 = SSH)
		if !strings.HasSuffix(parts[0], "#0") {
			continue
		}
		host := strings.TrimSpace(parts[1])
		if host == "" {
			continue
		}
		port := 22
		if p, err := strconv.Atoi(strings.TrimSpace(parts[2])); err == nil && p > 0 && p <= 65535 {
			port = p
		}
		user := strings.TrimSpace(parts[3])
		if user == "" {
			user = "root"
		}
		keyPath := ""
		auth := "password"
		// Optional private key path is often in parts[14]
		if len(parts) > 14 && strings.TrimSpace(parts[14]) != "" && !strings.HasPrefix(parts[14], "_") {
			keyPath = strings.TrimSpace(parts[14])
			auth = "key"
		}
		// Optional MobaXterm SSH Gateway (parts[10]=gateway host, parts[11]=gateway port, parts[12]=gateway user)
		proxyJump := ""
		if len(parts) > 12 {
			gwHost := strings.TrimSpace(parts[10])
			if gwHost != "" && gwHost != "-1" && gwHost != "0" && strings.ContainsAny(gwHost, ".:") {
				gwPort := strings.TrimSpace(parts[11])
				gwUser := strings.TrimSpace(parts[12])
				if gwUser != "" && gwUser != "-1" {
					proxyJump = gwUser + "@" + gwHost
				} else {
					proxyJump = gwHost
				}
				if p, err := strconv.Atoi(gwPort); err == nil && p > 0 && p <= 65535 && p != 22 {
					proxyJump += ":" + strconv.Itoa(p)
				}
			}
		}
		out = append(out, ImportedHost{
			Name:         strings.TrimSpace(k),
			Host:         host,
			Port:         port,
			User:         user,
			Auth:         auth,
			KeyPath:      keyPath,
			Group:        currentGroup,
			ProxyJump:    proxyJump,
			SourceFormat: "MobaXterm",
		})
	}
	return out
}

// --- 4. PuTTY / KiTTY .reg ---
var puttyRegUnescape = strings.NewReplacer("\\\\", "\\", "\\\"", "\"")

func parsePuttyReg(text string) []ImportedHost {
	var out []ImportedHost
	var curName, host, user, proto, keyPath, proxyHost, proxyUser string
	port := 22
	proxyMethod, proxyPort := 0, 0
	pingMin, pingSec, forwardAgent := 0, 0, false
	inSession := false

	flush := func() {
		if inSession && host != "" && (proto == "" || strings.EqualFold(proto, "ssh")) {
			// PuTTY sometimes stores user@host in HostName
			if u, h, hasAt := strings.Cut(host, "@"); hasAt && h != "" {
				if user == "" {
					user = u
				}
				host = h
			}
			if user == "" {
				user = "root"
			}
			auth := "password"
			if keyPath != "" {
				auth = "key"
			}
			pType := ""
			if proxyHost != "" && proxyPort > 0 {
				if proxyMethod == 3 {
					pType = "http"
				} else if proxyMethod == 2 {
					pType = "socks5"
				}
			}
			out = append(out, ImportedHost{
				Name:         curName,
				Host:         host,
				Port:         port,
				User:         user,
				Auth:         auth,
				KeyPath:      keyPath,
				ProxyType:    pType,
				ProxyHost:    proxyHost,
				ProxyPort:    proxyPort,
				ProxyUser:    proxyUser,
				KeepAliveSec: pingMin*60 + pingSec,
				ForwardAgent: forwardAgent,
				SourceFormat: "PuTTY",
			})
		}
		inSession = false
		curName, host, user, proto, keyPath, proxyHost, proxyUser = "", "", "", "", "", "", ""
		port, proxyMethod, proxyPort = 22, 0, 0
		pingMin, pingSec, forwardAgent = 0, 0, false
	}

	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			sec := line[1 : len(line)-1]
			marker := `\Sessions\`
			if idx := strings.Index(strings.ToLower(sec), strings.ToLower(marker)); idx >= 0 {
				rawName := sec[idx+len(marker):]
				if rawName != "" && !strings.EqualFold(rawName, "Default%20Settings") {
					if decoded, err := url.PathUnescape(rawName); err == nil {
						curName = decodePuTTYName(decoded)
					} else {
						curName = rawName
					}
					inSession = true
				}
			}
			continue
		}
		if !inSession {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.Trim(strings.TrimSpace(k), `"`)
		v = strings.TrimSpace(v)
		unq := func(s string) string {
			if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
				return puttyRegUnescape.Replace(s[1 : len(s)-1])
			}
			return s
		}
		parseHexDword := func(s string) (int, bool) {
			if strings.HasPrefix(strings.ToLower(s), "dword:") {
				if p, err := strconv.ParseInt(s[6:], 16, 32); err == nil {
					return int(p), true
				}
			}
			return 0, false
		}
		switch strings.ToLower(k) {
		case "hostname":
			host = unq(v)
		case "username":
			user = unq(v)
		case "protocol":
			proto = unq(v)
		case "publickeyfile":
			keyPath = unq(v)
		case "portnumber":
			if p, ok := parseHexDword(v); ok && p > 0 && p <= 65535 {
				port = p
			}
		case "proxymethod":
			if m, ok := parseHexDword(v); ok {
				proxyMethod = m
			}
		case "proxyhost":
			proxyHost = unq(v)
		case "proxyport":
			if p, ok := parseHexDword(v); ok && p > 0 && p <= 65535 {
				proxyPort = p
			}
		case "proxyusername":
			proxyUser = unq(v)
		case "pinginterval":
			if n, ok := parseHexDword(v); ok && n <= 60 {
				pingMin = n
			}
		case "pingintervalsecs":
			if n, ok := parseHexDword(v); ok && n < 60 {
				pingSec = n
			}
		case "agentfwd":
			n, _ := parseHexDword(v)
			forwardAgent = n == 1
		}
	}
	flush()
	return out
}

// --- 5. SecureCRT XML & INI ---
func parseSecureCRTXML(text string) []ImportedHost {
	var out []ImportedHost
	dec := xml.NewDecoder(strings.NewReader(text))
	type node struct {
		name  string
		isKey bool
		vals  map[string]string
	}
	var stack []*node
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			nameAttr := ""
			for _, a := range t.Attr {
				if a.Name.Local == "name" {
					nameAttr = a.Value
				}
			}
			if t.Name.Local == "key" {
				stack = append(stack, &node{name: nameAttr, isKey: true, vals: map[string]string{}})
			} else if len(stack) > 0 && (t.Name.Local == "string" || t.Name.Local == "dword") {
				var content string
				_ = dec.DecodeElement(&content, &t)
				stack[len(stack)-1].vals[nameAttr] = strings.TrimSpace(content)
			}
		case xml.EndElement:
			if t.Name.Local == "key" && len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				host := top.vals["Hostname"]
				proto := top.vals["Protocol Name"]
				if host != "" && (proto == "" || strings.HasPrefix(strings.ToUpper(proto), "SSH")) {
					port := 22
					if pStr := top.vals["[SSH2] Port"]; pStr != "" {
						if p, err := strconv.Atoi(pStr); err == nil && p > 0 && p <= 65535 {
							port = p
						}
					}
					user := top.vals["Username"]
					if user == "" {
						user = "root"
					}
					var groups []string
					for _, anc := range stack {
						if anc.name != "" && anc.name != "Sessions" && anc.name != "VanDyke" {
							groups = append(groups, anc.name)
						}
					}
					fw := top.vals["Firewall Name"]
					if strings.EqualFold(fw, "None") {
						fw = ""
					}
					out = append(out, ImportedHost{
						Name:         top.name,
						Host:         host,
						Port:         port,
						User:         user,
						Auth:         "password",
						Group:        strings.Join(groups, "/"),
						ProxyJump:    fw,
						SourceFormat: "SecureCRT",
					})
				}
			}
		}
	}
	return out
}

func parseSecureCRTINI(filename, text, group string) []ImportedHost {
	var host, user, proto string
	port := 22
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, `S:"Hostname"=`) {
			host = strings.TrimSpace(strings.TrimPrefix(line, `S:"Hostname"=`))
		} else if strings.HasPrefix(line, `S:"Username"=`) {
			user = strings.TrimSpace(strings.TrimPrefix(line, `S:"Username"=`))
		} else if strings.HasPrefix(line, `S:"Protocol Name"=`) {
			proto = strings.TrimSpace(strings.TrimPrefix(line, `S:"Protocol Name"=`))
		} else if strings.HasPrefix(line, `D:"[SSH2] Port"=`) {
			hexVal := strings.TrimSpace(strings.TrimPrefix(line, `D:"[SSH2] Port"=`))
			if p, err := strconv.ParseInt(hexVal, 16, 32); err == nil && p > 0 && p <= 65535 {
				port = int(p)
			}
		}
	}
	if host == "" || (proto != "" && !strings.HasPrefix(strings.ToUpper(proto), "SSH")) {
		return nil
	}
	if user == "" {
		user = "root"
	}
	return []ImportedHost{{
		Name:         strings.TrimSuffix(filename, filepath.Ext(filename)),
		Host:         host,
		Port:         port,
		User:         user,
		Auth:         "password",
		Group:        group,
		SourceFormat: "SecureCRT",
	}}
}

// --- 6. JSON Formats (Electerm, WindTerm, FinalShell, Termius JSON, Shell native, generic JSON) ---
func parseJSONFormats(text string) (string, []ImportedHost, bool) {
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return "", nil, false
	}

	// Case A: Top-level JSON Object (Electerm / Shell / Termius / FinalShell single object)
	if obj, ok := raw.(map[string]any); ok {
		// Electerm bookmarks export: { "bookmarks": [...], "bookmarkGroups": [...] }
		if bms, ok := obj["bookmarks"].([]any); ok {
			groupMap := map[string]string{}
			if bgs, ok := obj["bookmarkGroups"].([]any); ok {
				for _, g := range bgs {
					if gm, ok := g.(map[string]any); ok {
						title := strField(gm, "title", "name")
						if ids, ok := gm["bookmarkIds"].([]any); ok {
							for _, idv := range ids {
								if idStr, ok := idv.(string); ok && title != "" && title != "default" {
									groupMap[idStr] = title
								}
							}
						}
					}
				}
			}
			var out []ImportedHost
			for _, item := range bms {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if h, ok := mapToHost(m, "Electerm"); ok {
					if id := strField(m, "id"); id != "" && h.Group == "" {
						h.Group = groupMap[id]
					}
					out = append(out, h)
				}
			}
			if len(out) > 0 {
				return "Electerm (.json)", out, true
			}
		}

		// Shell native export: { "profiles": [...] }
		if profs, ok := obj["profiles"].([]any); ok {
			var out []ImportedHost
			for _, item := range profs {
				if m, ok := item.(map[string]any); ok {
					if h, ok := mapToHost(m, "Shell JSON"); ok {
						out = append(out, h)
					}
				}
			}
			if len(out) > 0 {
				return "Shell 配置 (.json)", out, true
			}
		}

		// Termius / Generic wrapped list: { "hosts": [...] } or { "sessions": [...] } or { "connections": [...] }
		for _, key := range []string{"hosts", "sessions", "connections", "servers", "data", "items"} {
			if arr, ok := obj[key].([]any); ok {
				var out []ImportedHost
				for _, item := range arr {
					if m, ok := item.(map[string]any); ok {
						if h, ok := mapToHost(m, "JSON 配置"); ok {
							out = append(out, h)
						}
					}
				}
				if len(out) > 0 {
					label := "JSON 主机配置"
					if key == "hosts" {
						label = "Termius / JSON (.json)"
					}
					return label, out, true
				}
			}
		}

		// Single host JSON (e.g. FinalShell `connect_config_*.json`)
		if h, ok := mapToHost(obj, "FinalShell / JSON"); ok {
			label := "JSON 主机配置"
			if _, hasFS := obj["user_name"]; hasFS {
				label = "FinalShell (.json)"
			}
			return label, []ImportedHost{h}, true
		}
	}

	// Case B: Top-level JSON Array (WindTerm `user.sessions`, FinalShell array, generic array)
	if arr, ok := raw.([]any); ok {
		isWindTerm := false
		var out []ImportedHost
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if _, wt := m["session.target"]; wt {
				isWindTerm = true
				proto := strings.ToUpper(strField(m, "session.protocol"))
				if proto != "" && proto != "SSH" {
					continue
				}
				target := strField(m, "session.target")
				if target == "" {
					continue
				}
				user := strField(m, "session.user", "session.username")
				host := target
				if u, h, hasAt := strings.Cut(target, "@"); hasAt && h != "" {
					if user == "" {
						user = u
					}
					host = h
				}
				if user == "" {
					user = "root"
				}
				port := intField(m, 22, "session.port")
				label := strField(m, "session.label")
				if label == "" {
					label = host
				}
				grp := strings.TrimPrefix(strings.ReplaceAll(strField(m, "session.group"), ">", "/"), "/")
				jump := strField(m, "session.jumpServer", "session.proxyJump")
				pType, pHost, pPort, pUser := parseProxyURL(strField(m, "session.proxy"))
				out = append(out, ImportedHost{
					Name:         label,
					Host:         host,
					Port:         port,
					User:         user,
					Auth:         "password",
					Group:        grp,
					ProxyType:    pType,
					ProxyHost:    pHost,
					ProxyPort:    pPort,
					ProxyUser:    pUser,
					ProxyJump:    jump,
					SourceFormat: "WindTerm",
				})
				continue
			}
			if h, ok := mapToHost(m, "JSON"); ok {
				out = append(out, h)
			}
		}
		if len(out) > 0 {
			if isWindTerm {
				return "WindTerm (user.sessions)", out, true
			}
			return "JSON 主机配置", out, true
		}
	}
	return "", nil, false
}

func mapToHost(m map[string]any, source string) (ImportedHost, bool) {
	// Support nested "options" (Tabby JSON export)
	targetMap := m
	if opts, ok := m["options"].(map[string]any); ok {
		if strField(opts, "host", "hostname") != "" {
			targetMap = opts
		}
	}
	host := strField(targetMap, "host", "hostname", "hostName", "address", "ip", "server", "target")
	if host == "" {
		return ImportedHost{}, false
	}
	name := strField(m, "name", "title", "label", "alias", "displayName")
	if name == "" {
		name = host
	}
	user := strField(targetMap, "user", "username", "userName", "user_name", "login")
	if user == "" {
		user = strField(m, "user", "username", "userName", "user_name", "login")
	}
	if user == "" {
		user = "root"
	}
	port := intField(targetMap, 0, "port")
	if port == 0 {
		port = intField(m, 22, "port")
	}
	keyPath := strField(targetMap, "keyPath", "privateKeyPath", "identityFile", "privateKey", "key_path")
	if keyPath == "" {
		if pkArr, ok := targetMap["privateKeys"].([]any); ok && len(pkArr) > 0 {
			if s, ok := pkArr[0].(string); ok {
				keyPath = s
			}
		}
	}
	auth := strings.ToLower(strField(targetMap, "auth", "authType", "authentication_type"))
	if strings.Contains(auth, "key") || keyPath != "" {
		auth = "key"
	} else if strings.Contains(auth, "agent") {
		auth = "agent"
	} else {
		auth = "password"
	}
	group := strField(m, "group", "folder", "category", "parent_name", "groups")
	notes := strField(m, "notes", "description", "comment", "remark")
	password := strField(targetMap, "password", "passwd")
	if password == "" {
		password = strField(m, "password", "passwd")
	}
	passphrase := strField(targetMap, "passphrase", "keyPassphrase")
	proxyJump := strField(targetMap, "proxyJump", "proxy_jump", "jumpHost", "jump_host", "jumpHostId", "bastion")
	if proxyJump == "" {
		proxyJump = strField(m, "proxyJump", "proxy_jump", "jumpHost", "jump_host", "bastion")
	}
	// Electerm connectionHops: [{host, port, username}, ...]
	if proxyJump == "" {
		if hops, ok := m["connectionHops"].([]any); ok && len(hops) > 0 {
			var hopSpecs []string
			for _, hv := range hops {
				if hm, ok := hv.(map[string]any); ok {
					hh := strField(hm, "host", "hostname")
					if hh == "" {
						continue
					}
					hu := strField(hm, "username", "user")
					hp := intField(hm, 22, "port")
					spec := hh
					if hu != "" {
						spec = hu + "@" + spec
					}
					if hp != 22 {
						spec = fmt.Sprintf("%s:%d", spec, hp)
					}
					hopSpecs = append(hopSpecs, spec)
				}
			}
			proxyJump = strings.Join(hopSpecs, ",")
		}
	}
	pType := strings.ToLower(strField(targetMap, "proxyType", "proxy_type"))
	if pType == "" {
		pType = strings.ToLower(strField(m, "proxyType", "proxy_type"))
	}
	pHost := strField(targetMap, "proxyHost", "proxy_host")
	if pHost == "" {
		pHost = strField(m, "proxyHost", "proxy_host")
	}
	pPort := intField(targetMap, 0, "proxyPort", "proxy_port")
	if pPort == 0 {
		pPort = intField(m, 0, "proxyPort", "proxy_port")
	}
	pUser := strField(targetMap, "proxyUser", "proxy_user")
	if pUser == "" {
		pUser = strField(m, "proxyUser", "proxy_user")
	}
	if pType == "" {
		if rawProxy := strField(targetMap, "proxy", "proxyUrl", "proxy_url"); rawProxy != "" {
			pType, pHost, pPort, pUser = parseProxyURL(rawProxy)
		} else if rawProxy := strField(m, "proxy", "proxyUrl", "proxy_url"); rawProxy != "" {
			pType, pHost, pPort, pUser = parseProxyURL(rawProxy)
		} else if pm, ok := m["proxy"].(map[string]any); ok {
			// Electerm object proxy: {proxyIp, proxyPort, proxyType, proxyUsername}
			pt := strings.ToLower(strField(pm, "proxyType", "type"))
			ph := strField(pm, "proxyIp", "host")
			pp := intField(pm, 0, "proxyPort", "port")
			pu := strField(pm, "proxyUsername", "user")
			if strings.HasPrefix(pt, "socks") {
				pType, pHost, pPort, pUser = "socks5", ph, pp, pu
			} else if pt == "http" || pt == "https" {
				pType, pHost, pPort, pUser = "http", ph, pp, pu
			}
		}
	}
	return ImportedHost{
		Name:         name,
		Host:         host,
		Port:         port,
		User:         user,
		Auth:         auth,
		Password:     password,
		KeyPath:      keyPath,
		Passphrase:   passphrase,
		Group:        group,
		ProxyType:    pType,
		ProxyHost:    pHost,
		ProxyPort:    pPort,
		ProxyUser:    pUser,
		ProxyJump:    proxyJump,
		Notes:        notes,
		SourceFormat: source,
	}, true
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch tv := v.(type) {
			case string:
				if s := strings.TrimSpace(tv); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func intField(m map[string]any, def int, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch tv := v.(type) {
			case float64:
				if int(tv) >= 1 && int(tv) <= 65535 {
					return int(tv)
				}
			case string:
				if p, err := strconv.Atoi(strings.TrimSpace(tv)); err == nil && p >= 1 && p <= 65535 {
					return p
				}
			}
		}
	}
	return def
}

// --- 7. Tabby YAML Config ---
func parseTabbyYAML(text string) []ImportedHost {
	var out []ImportedHost
	inProfiles := false
	var cur *ImportedHost
	curType := ""

	flush := func() {
		if cur != nil && cur.Host != "" && (curType == "" || strings.EqualFold(curType, "ssh")) {
			if cur.Name == "" {
				cur.Name = cur.Host
			}
			if cur.User == "" {
				cur.User = "root"
			}
			if cur.Port == 0 {
				cur.Port = 22
			}
			out = append(out, *cur)
		}
		cur = nil
		curType = ""
	}

	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(raw, " ") && !strings.HasPrefix(raw, "\t") {
			if strings.HasPrefix(trimmed, "profiles:") {
				inProfiles = true
				continue
			}
			if inProfiles {
				flush()
				inProfiles = false
			}
			continue
		}
		if !inProfiles {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			flush()
			cur = &ImportedHost{Port: 22, Auth: "password", SourceFormat: "Tabby"}
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "type":
			curType = v
		case "name":
			cur.Name = v
		case "group":
			cur.Group = v
		case "host":
			cur.Host = v
		case "user":
			cur.User = v
		case "port":
			if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
				cur.Port = p
			}
		case "privatekey":
			if v != "" {
				cur.KeyPath = v
				cur.Auth = "key"
			}
		case "jumphost", "proxyjump":
			if v != "" {
				cur.ProxyJump = v
			}
		case "socksproxyhost":
			if v != "" {
				cur.ProxyType = "socks5"
				cur.ProxyHost = v
			}
		case "socksproxyport":
			if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
				cur.ProxyPort = p
			}
		case "httpproxyhost":
			if v != "" {
				cur.ProxyType = "http"
				cur.ProxyHost = v
			}
		case "httpproxyport":
			if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
				cur.ProxyPort = p
			}
		}
	}
	flush()
	return out
}

// --- 8. Netcatty / Termius / Royal TS / Generic CSV ---
func looksLikeCSV(text string) bool {
	first, _, _ := strings.Cut(text, "\n")
	lower := strings.ToLower(first)
	return (strings.Contains(first, ",") || strings.Contains(first, "\t")) &&
		(strings.Contains(lower, "host") || strings.Contains(lower, "address") || strings.Contains(lower, "ip") || strings.Contains(first, "主机"))
}

func parseProxyURL(raw string) (pType, pHost string, pPort int, pUser string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	lower := strings.ToLower(raw)
	switch lower {
	case "system", "sys", "auto", "系统代理":
		return "system", "", 0, ""
	case "direct", "none", "off", "false", "直连":
		return "direct", "", 0, ""
	}
	if !strings.Contains(raw, "://") {
		raw = "socks5://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case strings.HasPrefix(scheme, "socks"):
		pType = "socks5"
	case scheme == "http" || scheme == "https":
		pType = "http"
	default:
		return
	}
	pHost = u.Hostname()
	if p, err := strconv.Atoi(u.Port()); err == nil && p >= 1 && p <= 65535 {
		pPort = p
	} else if pType == "socks5" {
		pPort = 1080
	} else {
		pPort = 8080
	}
	if u.User != nil {
		pUser = u.User.Username()
	}
	return
}

func parseCSVDetailed(text string) (string, []ImportedHost, bool) {
	norm := strings.ReplaceAll(text, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")
	r := csv.NewReader(strings.NewReader(norm))
	if firstLine, _, _ := strings.Cut(norm, "\n"); !strings.Contains(firstLine, ",") && strings.Contains(firstLine, "\t") {
		r.Comma = '\t'
	}
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 1 {
		return "", nil, false
	}
	header := rows[0]
	normHeader := func(s string) string {
		s = strings.TrimPrefix(s, "\uFEFF")
		s = strings.ToLower(strings.TrimSpace(s))
		return s
	}
	col := func(candidates ...string) int {
		for idx, h := range header {
			hl := normHeader(h)
			for _, c := range candidates {
				if hl == c {
					return idx
				}
			}
		}
		return -1
	}
	iName := col("label", "name", "title", "alias", "display name", "displayname", "名称", "别名", "主机名")
	iHost := col("hostname/ip", "host/ip", "hostname", "host", "address", "ip", "ip address", "ip_address", "host_name", "server", "server_address", "uri", "主机", "主机地址", "地址")
	iPort := col("port", "ssh_port", "ssh port", "端口")
	iUser := col("username", "user", "user_name", "login", "account", "用户名", "账号")
	iPass := col("password", "passwd", "pwd", "密码")
	iGroup := col("groups", "group", "folder", "category", "path", "分组", "目录")
	iTags := col("tags", "tag", "标签")
	iProto := col("protocol", "type", "协议")
	iKey := col("identityfile", "keypath", "key_path", "privatekey", "private_key", "key", "私钥", "私钥路径")
	iKeyPass := col("passphrase", "keypassphrase", "key_passphrase", "私钥口令")
	iProxy := col("proxy", "proxy_url", "代理")
	iJump := col("proxyjump", "proxy_jump", "jump", "jumphost", "跳板机")
	iNotes := col("notes", "comment", "description", "remark", "备注")

	if iHost < 0 {
		return "", nil, false
	}

	firstLower := strings.ToLower(strings.Join(header, ","))
	fmtName := "CSV 主机列表"
	if strings.Contains(firstLower, "hostname/ip") && strings.Contains(firstLower, "sshinputcompatibility") {
		fmtName = "Netcatty (.csv)"
	} else if strings.Contains(firstLower, "hostname/ip") {
		fmtName = "Netcatty / CSV (.csv)"
	} else if strings.Contains(firstLower, "label") && strings.Contains(firstLower, "hostname") {
		fmtName = "Termius / CSV (.csv)"
	}

	if len(rows) < 2 {
		return fmtName, nil, true
	}

	get := func(row []string, idx int) string {
		if idx < 0 || idx >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[idx])
	}
	var out []ImportedHost
	for _, row := range rows[1:] {
		host := get(row, iHost)
		if host == "" {
			continue
		}
		if proto := strings.ToLower(get(row, iProto)); proto != "" && proto != "ssh" && proto != "ssh2" && proto != "sftp" {
			continue
		}
		name := get(row, iName)
		if name == "" {
			name = host
		}
		user := get(row, iUser)
		if user == "" {
			user = "root"
		}
		port := 22
		if p, err := strconv.Atoi(get(row, iPort)); err == nil && p > 0 && p <= 65535 {
			port = p
		}
		keyPath := get(row, iKey)
		pass := get(row, iPass)
		keyPass := get(row, iKeyPass)
		auth := "password"
		if keyPath != "" {
			auth = "key"
		}
		var tags []string
		if rawTags := get(row, iTags); rawTags != "" {
			for _, t := range strings.FieldsFunc(rawTags, func(r rune) bool { return r == ',' || r == ';' || r == '|' }) {
				if tt := strings.TrimSpace(t); tt != "" {
					tags = append(tags, tt)
				}
			}
		}
		pType, pHost, pPort, pUser := parseProxyURL(get(row, iProxy))
		out = append(out, ImportedHost{
			Name:         name,
			Host:         host,
			Port:         port,
			User:         user,
			Auth:         auth,
			Password:     pass,
			KeyPath:      keyPath,
			Passphrase:   keyPass,
			Group:        get(row, iGroup),
			Tags:         tags,
			ProxyType:    pType,
			ProxyHost:    pHost,
			ProxyPort:    pPort,
			ProxyUser:    pUser,
			ProxyJump:    get(row, iJump),
			Notes:        get(row, iNotes),
			SourceFormat: fmtName,
		})
	}
	return fmtName, out, true
}

// --- 9. OpenSSH Config from text ---
func containsOpenSSHDirectives(text string) bool {
	lower := strings.ToLower(text)
	return (strings.Contains(lower, "host ") || strings.Contains(lower, "host\t")) &&
		(strings.Contains(lower, "hostname") || strings.Contains(lower, "identityfile") || strings.Contains(lower, "proxyjump"))
}

func parseOpenSSHConfigText(text string) []ImportedHost {
	tmp, err := os.CreateTemp("", "shell-sshcfg-*")
	if err != nil {
		return nil
	}
	defer os.Remove(tmp.Name())
	_, _ = tmp.WriteString(text)
	tmp.Close()
	entries, err := ParseSSHConfig(tmp.Name())
	if err != nil {
		return nil
	}
	out := make([]ImportedHost, 0, len(entries))
	for _, e := range entries {
		auth := "agent"
		if e.PasswordAuth {
			auth = "password"
		}
		if e.IdentityFile != "" {
			auth = "key"
		}
		out = append(out, ImportedHost{
			Name:         e.Alias,
			Host:         e.HostName,
			Port:         e.Port,
			User:         e.User,
			Auth:         auth,
			KeyPath:      e.IdentityFile,
			ProxyJump:    e.ProxyJump,
			ForwardAgent: e.ForwardAgent,
			KeepAliveSec: e.KeepAliveSec,
			SourceFormat: "OpenSSH",
		})
	}
	return out
}

// --- 10. Plain text lines (`user@host:port` or `ssh -p 2222 user@host`) ---
func parsePlainHostLines(text string) []ImportedHost {
	var out []ImportedHost
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s := strings.TrimPrefix(line, "ssh ")
		port := 22
		fields := strings.Fields(s)
		target := ""
		for i := 0; i < len(fields); i++ {
			if fields[i] == "-p" && i+1 < len(fields) {
				if p, err := strconv.Atoi(fields[i+1]); err == nil && p > 0 && p <= 65535 {
					port = p
				}
				i++
				continue
			}
			if !strings.HasPrefix(fields[i], "-") {
				target = fields[i]
			}
		}
		if target == "" {
			continue
		}
		user := "root"
		hostPart := target
		if u, h, ok := strings.Cut(target, "@"); ok && h != "" {
			user = u
			hostPart = h
		}
		if h, pStr, ok := strings.Cut(hostPart, ":"); ok && !strings.Contains(pStr, ":") {
			if p, err := strconv.Atoi(pStr); err == nil && p > 0 && p <= 65535 {
				hostPart = h
				port = p
			}
		}
		if _, err := cleanHost(hostPart); err != nil {
			continue
		}
		out = append(out, ImportedHost{
			Name:         hostPart,
			Host:         hostPart,
			Port:         port,
			User:         user,
			Auth:         "password",
			SourceFormat: "文本列表",
		})
	}
	return out
}
