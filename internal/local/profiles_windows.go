package local

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// TunnelRule describes a saved port-forwarding rule attached to a host profile.
type TunnelRule struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Mode       string `json:"mode"` // "local", "remote", "dynamic"
	BindHost   string `json:"bindHost"`
	BindPort   int    `json:"bindPort"`
	TargetHost string `json:"targetHost,omitempty"`
	TargetPort int    `json:"targetPort,omitempty"`
	AutoStart  bool   `json:"autoStart,omitempty"`
}

// Profile stores a saved SSH host configuration (without plaintext secrets).
type Profile struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Group          string       `json:"group,omitempty"`
	Tags           []string     `json:"tags,omitempty"`
	Color          string       `json:"color,omitempty"` // "", "blue", "green", "orange", "red", "purple", "gray"
	Icon           string       `json:"icon,omitempty"`  // "", "server", "linux", "docker", "database", "cloud", "gpu", "bastion", "terminal", "globe"
	Pinned         bool         `json:"pinned,omitempty"`
	Host           string       `json:"host"`
	Port           int          `json:"port"`
	User           string       `json:"user"`
	Auth           string       `json:"auth"` // "password", "key", "agent"
	KeyPath        string       `json:"keyPath,omitempty"`
	JumpHostID     string       `json:"jumpHostId,omitempty"`
	ProxyType      string       `json:"proxyType,omitempty"` // "", "none", "socks5", "http"
	ProxyHost      string       `json:"proxyHost,omitempty"`
	ProxyPort      int          `json:"proxyPort,omitempty"`
	ProxyUser      string       `json:"proxyUser,omitempty"`
	ForwardAgent   bool         `json:"forwardAgent,omitempty"`
	KeepAliveSec   int          `json:"keepAliveSec,omitempty"`
	StartupCommand string       `json:"startupCommand,omitempty"`
	Notes          string       `json:"notes,omitempty"`
	AgentAccess    string       `json:"agentAccess,omitempty"` // "", "readonly", "allow", "deny"
	Tunnels        []TunnelRule `json:"tunnels,omitempty"`
	LastUsedAt     int64        `json:"lastUsedAt,omitempty"`
}

const profileLimit = 4 << 20

var validColors = map[string]bool{
	"": true, "blue": true, "green": true, "orange": true, "red": true, "purple": true, "gray": true, "teal": true,
}

func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func cleanHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	raw := host
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		raw = host[1 : len(host)-1]
	}
	if ip, err := netip.ParseAddr(raw); err == nil && (raw == host || ip.Is6()) &&
		len(raw) <= 255 && strings.IndexFunc(raw, unicode.IsControl) < 0 {
		return raw, nil
	}
	invalid := errors.New("请输入有效的主机名、IPv4 或 IPv6 地址，端口请单独填写")
	name := strings.TrimSuffix(host, ".")
	if len(name) == 0 || len(name) > 253 {
		return "", invalid
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", invalid
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", invalid
			}
		}
	}
	return host, nil
}

func normalizeTunnel(r TunnelRule) (TunnelRule, error) {
	r.Name = strings.TrimSpace(r.Name)
	r.Mode = strings.ToLower(strings.TrimSpace(r.Mode))
	r.BindHost = strings.TrimSpace(r.BindHost)
	r.TargetHost = strings.TrimSpace(r.TargetHost)
	if r.ID == "" {
		id, err := newID()
		if err != nil {
			return r, err
		}
		r.ID = id
	}
	if r.BindHost == "" {
		r.BindHost = "127.0.0.1"
	}
	if r.BindPort < 1 || r.BindPort > 65535 {
		return r, errors.New("转发监听端口必须在 1 到 65535 之间")
	}
	switch r.Mode {
	case "local", "remote":
		if r.TargetHost == "" {
			r.TargetHost = "127.0.0.1"
		}
		if r.TargetPort < 1 || r.TargetPort > 65535 {
			return r, errors.New("转发目标端口必须在 1 到 65535 之间")
		}
	case "dynamic":
		r.TargetHost = ""
		r.TargetPort = 0
	default:
		return r, errors.New("转发模式仅支持 local、remote 或 dynamic")
	}
	if r.Name == "" {
		switch r.Mode {
		case "dynamic":
			r.Name = fmt.Sprintf("SOCKS5 :%d", r.BindPort)
		case "local":
			r.Name = fmt.Sprintf("L :%d -> %s:%d", r.BindPort, r.TargetHost, r.TargetPort)
		case "remote":
			r.Name = fmt.Sprintf("R :%d -> %s:%d", r.BindPort, r.TargetHost, r.TargetPort)
		}
	}
	return r, nil
}

func normalize(p Profile) (Profile, error) {
	host, err := cleanHost(p.Host)
	if err != nil {
		return p, err
	}
	p.Host = host
	p.Name = strings.TrimSpace(p.Name)
	p.Group = strings.TrimSpace(p.Group)
	p.Icon = strings.ToLower(strings.TrimSpace(p.Icon))
	p.User = strings.TrimSpace(p.User)
	p.KeyPath = strings.TrimSpace(p.KeyPath)
	p.JumpHostID = strings.TrimSpace(p.JumpHostID)
	p.ProxyType = strings.ToLower(strings.TrimSpace(p.ProxyType))
	p.ProxyHost = strings.TrimSpace(p.ProxyHost)
	p.ProxyUser = strings.TrimSpace(p.ProxyUser)
	switch p.ProxyType {
	case "", "none":
		p.ProxyType = ""
		p.ProxyHost = ""
		p.ProxyPort = 0
		p.ProxyUser = ""
	case "direct", "system":
		p.ProxyHost = ""
		p.ProxyPort = 0
		p.ProxyUser = ""
	case "socks5", "http":
		if p.ProxyHost == "" || p.ProxyPort < 1 || p.ProxyPort > 65535 {
			return p, errors.New("启用 SOCKS5/HTTP 代理时请填写有效代理地址和端口")
		}
	default:
		return p, errors.New("代理类型仅支持直连、系统代理、socks5 或 http")
	}
	p.Color = strings.ToLower(strings.TrimSpace(p.Color))
	p.Notes = strings.TrimSpace(p.Notes)
	p.StartupCommand = strings.TrimSpace(p.StartupCommand)
	switch strings.ToLower(strings.TrimSpace(p.AgentAccess)) {
	case "", "readonly", "allow", "deny":
		p.AgentAccess = strings.ToLower(strings.TrimSpace(p.AgentAccess))
	default:
		p.AgentAccess = ""
	}

	if p.Name == "" {
		p.Name = host
	}
	if p.Port == 0 {
		p.Port = 22
	}
	if p.Port < 1 || p.Port > 65535 {
		return p, errors.New("端口必须在 1 到 65535 之间")
	}
	if p.User == "" || utf8.RuneCountInString(p.User) > 256 || utf8.RuneCountInString(p.Name) > 256 || utf8.RuneCountInString(p.Group) > 128 {
		return p, errors.New("请填写用户名，名称及用户名最多 256 字符")
	}
	if !validColors[p.Color] {
		p.Color = ""
	}
	if p.KeepAliveSec < 0 {
		p.KeepAliveSec = 0
	}
	if p.KeepAliveSec > 3600 {
		p.KeepAliveSec = 3600
	}
	if p.JumpHostID != "" && p.JumpHostID == p.ID {
		return p, errors.New("跳板机不能指向主机自身")
	}
	for _, s := range []string{p.Name, p.Group, p.User, p.KeyPath, p.JumpHostID} {
		if strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return p, errors.New("连接资料中不能包含控制字符")
		}
	}
	if utf8.RuneCountInString(p.StartupCommand) > 2048 || utf8.RuneCountInString(p.Notes) > 4096 {
		return p, errors.New("启动命令或备注过长")
	}
	if len(p.Tags) > 0 {
		seen := make(map[string]bool, len(p.Tags))
		cleaned := make([]string, 0, len(p.Tags))
		for _, tag := range p.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" || utf8.RuneCountInString(tag) > 48 || strings.IndexFunc(tag, unicode.IsControl) >= 0 {
				continue
			}
			key := strings.ToLower(tag)
			if !seen[key] {
				seen[key] = true
				cleaned = append(cleaned, tag)
			}
			if len(cleaned) >= 16 {
				break
			}
		}
		p.Tags = cleaned
	}
	if len(p.Tunnels) > 0 {
		if len(p.Tunnels) > 64 {
			return p, errors.New("单个主机最多配置 64 条转发规则")
		}
		tunnels := make([]TunnelRule, 0, len(p.Tunnels))
		for _, r := range p.Tunnels {
			nr, err := normalizeTunnel(r)
			if err != nil {
				return p, err
			}
			tunnels = append(tunnels, nr)
		}
		p.Tunnels = tunnels
	}
	switch p.Auth {
	case "", "password":
		p.Auth = "password"
		p.KeyPath = ""
	case "agent":
		p.KeyPath = ""
	case "key":
		if p.KeyPath == "" || len(p.KeyPath) > 32767 {
			return p, errors.New("密钥认证需要有效的私钥文件路径")
		}
	default:
		return p, errors.New("请选择密码、私钥文件或 SSH Agent 认证")
	}
	return p, nil
}

func readProfiles(dir string) ([]Profile, error) {
	data, err := readLimited(filepath.Join(dir, "profiles.json"), profileLimit)
	if errors.Is(err, os.ErrNotExist) {
		return []Profile{}, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Profile
	if !json.Valid(data) || json.NewDecoder(bytes.NewReader(data)).Decode(&list) != nil || list == nil {
		return nil, errors.New("连接配置文件格式无效，原文件已保留")
	}
	ids := make(map[string]bool, len(list))
	for i, p := range list {
		if p.ID == "" || ids[p.ID] {
			return nil, errors.New("连接配置文件中存在无效或重复的 ID")
		}
		ids[p.ID] = true
		list[i], err = normalize(p)
		if err != nil {
			return nil, fmt.Errorf("连接配置无效：%w", err)
		}
	}
	return list, nil
}

func writeProfiles(dir string, profiles []Profile) error {
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > profileLimit {
		return errors.New("连接配置超过 4 MiB 容量")
	}
	return writeAtomic(filepath.Join(dir, "profiles.json"), data)
}

func Profiles() ([]Profile, error) {
	var list []Profile
	err := withStore(func(dir string) error {
		var err error
		list, err = readProfiles(dir)
		return err
	})
	return list, err
}

func GetProfile(id string) (Profile, bool, error) {
	var found Profile
	var ok bool
	err := withStore(func(dir string) error {
		list, err := readProfiles(dir)
		if err != nil {
			return err
		}
		for _, item := range list {
			if item.ID == id {
				found = item
				ok = true
				return nil
			}
		}
		return nil
	})
	return found, ok, err
}

func SaveProfile(p Profile) (Profile, error) {
	p, err := normalize(p)
	if err != nil {
		return Profile{}, err
	}
	err = withStore(func(dir string) error {
		list, err := readProfiles(dir)
		if err != nil {
			return err
		}
		if p.ID == "" {
			id, err := newID()
			if err != nil {
				return err
			}
			p.ID = id
			return writeProfiles(dir, append(list, p))
		}
		for i := range list {
			if list[i].ID == p.ID {
				if identityKey(list[i]) != identityKey(p) {
					if err := removeSecret(dir, p.ID); err != nil {
						return err
					}
				}
				if p.LastUsedAt == 0 {
					p.LastUsedAt = list[i].LastUsedAt
				}
				list[i] = p
				return writeProfiles(dir, list)
			}
		}
		return errors.New("连接资料已被删除，请刷新列表")
	})
	if err != nil {
		return Profile{}, err
	}
	return p, nil
}

// TouchProfile updates the last-connected timestamp of a profile.
func TouchProfile(id string) error {
	if id == "" {
		return nil
	}
	return withStore(func(dir string) error {
		list, err := readProfiles(dir)
		if err != nil {
			return err
		}
		for i := range list {
			if list[i].ID == id {
				list[i].LastUsedAt = time.Now().Unix()
				return writeProfiles(dir, list)
			}
		}
		return nil
	})
}

// DuplicateProfile clones an existing profile under a new ID (without copying DPAPI secrets).
func DuplicateProfile(id string) (Profile, error) {
	var clone Profile
	err := withStore(func(dir string) error {
		list, err := readProfiles(dir)
		if err != nil {
			return err
		}
		for _, item := range list {
			if item.ID == id {
				newid, err := newID()
				if err != nil {
					return err
				}
				clone = item
				clone.ID = newid
				clone.Name = item.Name + " 副本"
				clone.Pinned = false
				clone.LastUsedAt = 0
				if len(item.Tags) > 0 {
					clone.Tags = append([]string(nil), item.Tags...)
				}
				if len(item.Tunnels) > 0 {
					clone.Tunnels = make([]TunnelRule, len(item.Tunnels))
					for idx, tr := range item.Tunnels {
						tid, err := newID()
						if err != nil {
							return err
						}
						tr.ID = tid
						clone.Tunnels[idx] = tr
					}
				}
				return writeProfiles(dir, append(list, clone))
			}
		}
		return errors.New("未找到要复制的连接配置")
	})
	return clone, err
}

func DeleteProfile(id string) error {
	return withStore(func(dir string) error {
		list, err := readProfiles(dir)
		if err != nil {
			return err
		}
		for i := range list {
			if list[i].ID == id {
				if err := removeSecret(dir, id); err != nil {
					return err
				}
				next := append(list[:i], list[i+1:]...)
				// Clear any jumpHostId references pointing to the deleted profile.
				for j := range next {
					if next[j].JumpHostID == id {
						next[j].JumpHostID = ""
					}
				}
				return writeProfiles(dir, next)
			}
		}
		return nil
	})
}

// DeleteProfiles removes multiple host profiles and their DPAPI secrets in one atomic lock.
func DeleteProfiles(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			drop[id] = true
		}
	}
	if len(drop) == 0 {
		return nil
	}
	return withStore(func(dir string) error {
		list, err := readProfiles(dir)
		if err != nil {
			return err
		}
		next := make([]Profile, 0, len(list))
		for _, item := range list {
			if drop[item.ID] {
				_ = removeSecret(dir, item.ID)
				continue
			}
			if drop[item.JumpHostID] {
				item.JumpHostID = ""
			}
			next = append(next, item)
		}
		return writeProfiles(dir, next)
	})
}
