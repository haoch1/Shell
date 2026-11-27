package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ExportHostFile exports selected host profiles to CSV (Netcatty/Termius compatible), OpenSSH config, or PuTTY registry.
// Only the "csv" format reads DPAPI secrets so users can migrate complete credentials to other SSH clients.
func ExportHostFile(dest string, ids []string, format string) (int, []string, error) {
	var profiles []Profile
	settings := DefaultSettings()
	var secrets map[string]string
	includeSecrets := format == "csv"
	if err := withStore(func(dir string) error {
		var err error
		profiles, err = readProfiles(dir)
		if err != nil {
			return err
		}
		if includeSecrets {
			secrets = make(map[string]string, len(profiles))
			for _, p := range profiles {
				var item credential
				err := readProtected(secretPath(dir, p.ID), &item)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err == nil && item.For == identityKey(p) && item.Secret != "" {
					secrets[p.ID] = item.Secret
				}
			}
		}
		return readSettings(dir, &settings)
	}); err != nil {
		return 0, nil, err
	}
	defer clear(secrets)

	if format != "csv" && format != "csv-sanitized" {
		for i := range profiles {
			p := &profiles[i]
			if p.KeepAliveSec == 0 {
				p.KeepAliveSec = settings.DefaultKeepAlive
			}
			if p.ProxyType == "" || p.ProxyType == "none" {
				p.ProxyType = settings.ProxyMode
				p.ProxyHost, p.ProxyPort, p.ProxyUser = settings.ProxyHost, settings.ProxyPort, settings.ProxyUser
			}
		}
	}
	data, count, warnings, err := renderHostExportWithSecrets(profiles, ids, format, secrets)
	if err != nil {
		return 0, nil, err
	}
	if includeSecrets {
		defer clear(data)
	}
	if err := writeAtomic(dest, data); err != nil {
		return 0, nil, err
	}
	return count, warnings, nil
}

func renderHostExport(all []Profile, ids []string, format string) ([]byte, int, []string, error) {
	return renderHostExportWithSecrets(all, ids, format, nil)
}

func renderHostExportWithSecrets(all []Profile, ids []string, format string, secrets map[string]string) ([]byte, int, []string, error) {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	var selected []Profile
	for _, p := range all {
		if len(ids) == 0 || wanted[p.ID] {
			selected = append(selected, p)
			delete(wanted, p.ID)
		}
	}
	if len(wanted) != 0 {
		return nil, 0, nil, errors.New("选中的服务器已不存在，请刷新后重试")
	}
	if len(selected) == 0 {
		return nil, 0, nil, errors.New("没有可导出的服务器")
	}
	switch format {
	case "csv":
		return renderNetcattyCSV(selected, all, secrets, true)
	case "csv-sanitized":
		return renderNetcattyCSV(selected, all, nil, false)
	case "openssh":
		return renderOpenSSH(selected, all)
	case "putty":
		data, warnings, err := renderPuTTY(selected, all)
		return data, len(selected), warnings, err
	default:
		return nil, 0, nil, errors.New("未知的服务器导出格式")
	}
}

var sshAliasSeparator = regexp.MustCompile(`[^a-z0-9._-]+`)
var sshHostLiteral = regexp.MustCompile(`^[a-zA-Z0-9:._%-]+$`)

func sshAliasBase(p Profile) string {
	slug := func(s string) string {
		return strings.Trim(sshAliasSeparator.ReplaceAllString(strings.ToLower(s), "-"), ".-_")
	}
	name := slug(p.Name)
	if name == "" {
		name = slug(p.Host)
	}
	switch name {
	case "":
		return "host"
	case "none":
		return "host-none" // ProxyJump reserves "none".
	default:
		return name
	}
}

// Allocate aliases across all saved hosts so selecting a subset never renames a jump host.
func sshAliases(all []Profile) map[string]string {
	sorted := append([]Profile(nil), all...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	reserved := make(map[string]bool, len(all))
	for _, p := range sorted {
		reserved[sshAliasBase(p)] = true
	}
	counts := make(map[string]int)
	aliases := make(map[string]string, len(all))
	for _, p := range sorted {
		base := sshAliasBase(p)
		counts[base]++
		alias := base
		if counts[base] > 1 {
			alias = base + "-" + strconv.Itoa(counts[base])
			for reserved[alias] {
				counts[base]++
				alias = base + "-" + strconv.Itoa(counts[base])
			}
		}
		aliases[p.ID] = alias
	}
	return aliases
}

func sshSelection(selected, all []Profile) (map[string]Profile, error) {
	byID := make(map[string]Profile, len(all))
	for _, p := range all {
		byID[p.ID] = p
	}
	needed := make(map[string]Profile, len(selected))
	for _, p := range selected {
		chain := make(map[string]bool)
		for {
			if chain[p.ID] || len(chain) > 5 {
				return nil, errors.New("跳板机引用循环或超过五级，请检查后导出")
			}
			chain[p.ID] = true
			needed[p.ID] = p
			if p.JumpHostID == "" {
				break
			}
			var ok bool
			p, ok = byID[p.JumpHostID]
			if !ok {
				return nil, errors.New("未找到所配置的跳板机，请检查后导出")
			}
		}
	}
	return needed, nil
}

// OpenSSH argv_split handles quoted arguments and three backslash escapes.
func sshArgument(s string) string {
	if !strings.ContainsAny(s, " \\#\"'") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func renderOpenSSH(selected, all []Profile) ([]byte, int, []string, error) {
	needed, err := sshSelection(selected, all)
	if err != nil {
		return nil, 0, nil, err
	}
	aliases := sshAliases(all)
	hosts := make([]Profile, 0, len(needed))
	for _, p := range needed {
		hosts = append(hosts, p)
	}
	sort.Slice(hosts, func(i, j int) bool { return aliases[hosts[i].ID] < aliases[hosts[j].ID] })
	var config strings.Builder
	warnings := make(map[string]bool)
	if extra := len(hosts) - len(selected); extra > 0 {
		warnings[fmt.Sprintf("已一并导出 %d 台依赖的跳板机。", extra)] = true
	}
	for _, p := range hosts {
		alias := aliases[p.ID]
		host, err := cleanHost(p.Host)
		if err != nil || !sshHostLiteral.MatchString(host) || p.Port < 1 || p.Port > 65535 {
			return nil, 0, nil, fmt.Errorf("服务器 %s 的地址或端口无法用于 OpenSSH", alias)
		}
		if p.User == "" || strings.IndexFunc(p.User, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(`@.\_-+# `, r)
		}) >= 0 {
			return nil, 0, nil, fmt.Errorf("服务器 %s 的用户名包含无法安全导出的字符", alias)
		}
		if config.Len() > 0 {
			config.WriteByte('\n')
		}
		if p.Name != alias && p.Name != "" {
			// Preserve non-ASCII display names without turning them into Host patterns.
			comment := strings.Map(func(r rune) rune {
				if !unicode.IsPrint(r) {
					return ' '
				}
				return r
			}, p.Name)
			fmt.Fprintf(&config, "# %s\n", comment)
		}
		fmt.Fprintf(&config, "Host %s\n    HostName %s\n    User %s\n    Port %d\n", alias, strings.ReplaceAll(host, "%", "%%"), sshArgument(p.User), p.Port)
		switch p.Auth {
		case "password":
			config.WriteString("    PreferredAuthentications keyboard-interactive,password\n")
		case "key", "agent":
			config.WriteString("    PreferredAuthentications publickey\n")
		}
		if p.Auth == "key" {
			path := p.KeyPath
			switch {
			case path == "" || strings.IndexFunc(path, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 || strings.Contains(path, "${") || strings.Contains(path, "-----BEGIN ") || strings.EqualFold(path, "none"):
				warnings["部分私钥路径无法表示，请在目标客户端重新选择私钥文件。"] = true
			case strings.EqualFold(filepath.Ext(path), ".ppk"):
				warnings["PuTTY PPK 私钥需先转换为 OpenSSH 格式，再设置 IdentityFile。"] = true
			default:
				path = strings.ReplaceAll(filepath.ToSlash(path), "%", "%%")
				fmt.Fprintf(&config, "    IdentityFile %s\n    IdentitiesOnly yes\n", sshArgument(path))
			}
		}
		if p.ForwardAgent {
			config.WriteString("    ForwardAgent yes\n")
		} else {
			config.WriteString("    ForwardAgent no\n")
		}
		fmt.Fprintf(&config, "    ServerAliveInterval %d\n", p.KeepAliveSec)
		if p.JumpHostID != "" {
			fmt.Fprintf(&config, "    ProxyJump %s\n", aliases[p.JumpHostID])
		}
		switch p.ProxyType {
		case "system", "http", "socks5":
			warnings["系统代理及 HTTP/SOCKS 代理需在目标客户端重新配置。"] = true
		}
		if p.StartupCommand != "" || len(p.Tunnels) > 0 {
			warnings["启动命令及端口转发规则未导出。"] = true
		}
	}
	messages := make([]string, 0, len(warnings))
	for msg := range warnings {
		messages = append(messages, msg)
	}
	sort.Strings(messages)
	return []byte(config.String()), len(hosts), messages, nil
}
