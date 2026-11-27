package local

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SSHConfigHost represents a parsed host stanza from ~/.ssh/config.
type SSHConfigHost struct {
	Alias        string `json:"alias"`
	HostName     string `json:"hostName"`
	User         string `json:"user"`
	Port         int    `json:"port"`
	IdentityFile string `json:"identityFile"`
	PasswordAuth bool   `json:"passwordAuth,omitempty"`
	ProxyJump    string `json:"proxyJump"`
	ForwardAgent bool   `json:"forwardAgent"`
	KeepAliveSec int    `json:"keepAliveSec"`
	AlreadySaved bool   `json:"alreadySaved"`
}

func expandHome(p string) string {
	p = strings.Trim(strings.TrimSpace(p), `"'`)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return filepath.Clean(p)
}

// sshConfigArgs follows OpenSSH's quoting and comment rules for directive arguments.
func sshConfigArgs(s string) ([]string, bool) {
	var args []string
	for i := 0; i < len(s); {
		if s[i] == ' ' || s[i] == '\t' {
			i++
			continue
		}
		if s[i] == '#' {
			break
		}
		var token strings.Builder
		var quote byte
	tokenLoop:
		for i < len(s) {
			c := s[i]
			i++
			switch {
			case c == '\\' && i < len(s) && (strings.ContainsRune(`\'"`, rune(s[i])) || quote == 0 && s[i] == ' '):
				token.WriteByte(s[i])
				i++
			case quote == 0 && (c == ' ' || c == '\t'):
				break tokenLoop
			case quote == 0 && (c == '"' || c == '\''):
				quote = c
			case quote != 0 && c == quote:
				quote = 0
			default:
				token.WriteByte(c)
			}
		}
		if quote != 0 {
			return nil, false
		}
		args = append(args, token.String())
	}
	return args, true
}

// ParseSSHConfig parses an OpenSSH client config file into concrete host entries.
func ParseSSHConfig(configPath string) ([]SSHConfigHost, error) {
	f, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []SSHConfigHost{}, nil
		}
		return nil, err
	}
	defer f.Close()

	type stanza struct {
		patterns []string
		props    map[string]string
	}
	var stanzas []stanza
	var current *stanza
	globalDefaults := make(map[string]string)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var key, val string
		if eqIdx := strings.IndexAny(line, " \t="); eqIdx > 0 {
			key = strings.ToLower(strings.TrimSpace(line[:eqIdx]))
			val = strings.TrimSpace(strings.TrimLeft(line[eqIdx:], " \t="))
		} else {
			continue
		}
		args, valid := sshConfigArgs(val)
		if key == "" || !valid || len(args) == 0 {
			continue
		}
		if key == "host" {
			stanzas = append(stanzas, stanza{patterns: args, props: make(map[string]string)})
			current = &stanzas[len(stanzas)-1]
			continue
		}
		if key == "match" {
			// Skip Match blocks; resume on next Host.
			current = nil
			continue
		}
		val = strings.Join(args, " ")
		if current == nil {
			if _, exists := globalDefaults[key]; !exists {
				globalDefaults[key] = val
			}
			continue
		}
		// In OpenSSH config, first value wins for each directive.
		if _, exists := current.props[key]; !exists {
			current.props[key] = val
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Collect wildcard defaults from `Host *` blocks.
	wildcard := make(map[string]string)
	for k, v := range globalDefaults {
		wildcard[k] = v
	}
	for _, st := range stanzas {
		for _, pat := range st.patterns {
			if pat == "*" {
				for k, v := range st.props {
					if _, exists := wildcard[k]; !exists {
						wildcard[k] = v
					}
				}
			}
		}
	}

	defaultUser := os.Getenv("USERNAME")
	if defaultUser == "" {
		defaultUser = "root"
	}

	seen := make(map[string]bool)
	var out []SSHConfigHost
	for _, st := range stanzas {
		for _, pat := range st.patterns {
			if strings.ContainsAny(pat, "*?!") {
				continue
			}
			key := strings.ToLower(pat)
			if seen[key] {
				continue
			}
			seen[key] = true

			get := func(k string) string {
				if v, ok := st.props[k]; ok {
					return v
				}
				return wildcard[k]
			}
			hostName := strings.ReplaceAll(get("hostname"), "%%", "%")
			if hostName == "" {
				hostName = pat
			}
			user := get("user")
			if user == "" {
				user = defaultUser
			}
			port := 22
			if ps := get("port"); ps != "" {
				if p, err := strconv.Atoi(ps); err == nil && p >= 1 && p <= 65535 {
					port = p
				}
			}
			ident := expandHome(strings.ReplaceAll(get("identityfile"), "%%", "%"))
			preferred := strings.ToLower(get("preferredauthentications"))
			passwordAuth := preferred != ""
			for _, method := range strings.Split(preferred, ",") {
				if method != "password" && method != "keyboard-interactive" {
					passwordAuth = false
				}
			}
			fwd := strings.EqualFold(get("forwardagent"), "yes")
			ka := 30
			if kas := get("serveraliveinterval"); kas != "" {
				if n, err := strconv.Atoi(kas); err == nil && n >= 0 && n <= 3600 {
					ka = n
				}
			}
			out = append(out, SSHConfigHost{
				Alias:        pat,
				HostName:     hostName,
				User:         user,
				Port:         port,
				IdentityFile: ident,
				PasswordAuth: passwordAuth,
				ProxyJump:    strings.TrimSpace(get("proxyjump")),
				ForwardAgent: fwd,
				KeepAliveSec: ka,
			})
		}
	}
	return out, nil
}

// DiscoverSSHConfig reads %USERPROFILE%\.ssh\config and flags hosts already present in Shell profiles.
func DiscoverSSHConfig() ([]SSHConfigHost, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	entries, err := ParseSSHConfig(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil, err
	}
	existing, err := Profiles()
	if err != nil {
		return entries, nil
	}
	byKey := make(map[string]bool, len(existing))
	for _, p := range existing {
		byKey[strings.ToLower(p.User+"@"+p.Host+":"+strconv.Itoa(p.Port))] = true
	}
	for i := range entries {
		k := strings.ToLower(entries[i].User + "@" + entries[i].HostName + ":" + strconv.Itoa(entries[i].Port))
		entries[i].AlreadySaved = byKey[k]
	}
	return entries, nil
}

// ImportSSHConfigEntries imports selected OpenSSH config entries into Shell profiles (linking ProxyJump where matched).
func ImportSSHConfigEntries(entries []SSHConfigHost, group string) ([]Profile, error) {
	if group == "" {
		group = "SSH Config"
	}
	type pendingJump struct {
		idx   int
		alias string
	}
	created := make([]Profile, 0, len(entries))
	aliasToID := make(map[string]string, len(entries))
	var pendingJumps []pendingJump
	for _, e := range entries {
		auth := "agent"
		if e.PasswordAuth {
			auth = "password"
		}
		keyPath := ""
		if e.IdentityFile != "" {
			if _, err := os.Stat(e.IdentityFile); err == nil {
				auth = "key"
				keyPath = e.IdentityFile
			}
		}
		p := Profile{
			Name:         e.Alias,
			Group:        group,
			Host:         e.HostName,
			Port:         e.Port,
			User:         e.User,
			Auth:         auth,
			KeyPath:      keyPath,
			ForwardAgent: e.ForwardAgent,
			KeepAliveSec: e.KeepAliveSec,
			Tags:         []string{"ssh-config"},
		}
		saved, err := SaveProfile(p)
		if err != nil {
			continue
		}
		aliasToID[strings.ToLower(e.Alias)] = saved.ID
		created = append(created, saved)
		if e.ProxyJump != "" {
			pendingJumps = append(pendingJumps, pendingJump{idx: len(created) - 1, alias: e.ProxyJump})
		}
	}
	// Second pass: resolve ProxyJump alias references using exact created indices.
	for _, pj := range pendingJumps {
		if jumpID, ok := aliasToID[strings.ToLower(pj.alias)]; ok && jumpID != created[pj.idx].ID {
			created[pj.idx].JumpHostID = jumpID
			if updated, err := SaveProfile(created[pj.idx]); err == nil {
				created[pj.idx] = updated
			}
		}
	}
	return created, nil
}
