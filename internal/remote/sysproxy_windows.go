package remote

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// SystemProxyInfo describes the currently active Windows system proxy or environment proxy.
type SystemProxyInfo struct {
	Enabled  bool   `json:"enabled"`
	Protocol string `json:"protocol,omitempty"` // "socks5" or "http"
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"-"`
	Bypass   string `json:"bypass,omitempty"`
	Source   string `json:"source,omitempty"`  // "windows", "env", or ""
	Summary  string `json:"summary,omitempty"` // e.g. "http://127.0.0.1:7897"
}

// DetectSystemProxy inspects Windows Internet Settings (Registry) and standard proxy environment variables.
func DetectSystemProxy() SystemProxyInfo {
	if info, ok := readWindowsRegistryProxy(); ok {
		return info
	}
	if info, ok := readEnvProxy(); ok {
		return info
	}
	return SystemProxyInfo{}
}

func readWindowsRegistryProxy() (SystemProxyInfo, bool) {
	k, err := registry.OpenKey(
		registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return SystemProxyInfo{}, false
	}
	defer k.Close()

	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return SystemProxyInfo{}, false
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(server) == "" {
		return SystemProxyInfo{}, false
	}
	bypass, _, _ := k.GetStringValue("ProxyOverride")
	info, ok := parseWindowsProxyServer(server)
	if !ok {
		return SystemProxyInfo{}, false
	}
	info.Enabled = true
	info.Bypass = strings.TrimSpace(bypass)
	info.Source = "windows"
	info.Summary = fmt.Sprintf("%s://%s", info.Protocol, net.JoinHostPort(info.Host, strconv.Itoa(info.Port)))
	return info, true
}

// parseWindowsProxyServer parses Windows ProxyServer strings such as:
// - "127.0.0.1:7897"
// - "http://127.0.0.1:7897"
// - "http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7891"
// - "socks=127.0.0.1:10808"
// For raw SSH TCP traffic, socks= is preferred when explicitly configured; otherwise https=/http=/bare host:port uses HTTP CONNECT.
func parseWindowsProxyServer(raw string) (SystemProxyInfo, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SystemProxyInfo{}, false
	}
	if !strings.Contains(raw, "=") {
		return parseSingleProxyEndpoint(raw, "http")
	}

	entries := map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		entries[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	for _, key := range []string{"socks", "socks5", "socks4", "https", "http"} {
		if val := entries[key]; val != "" {
			defProto := "http"
			if strings.HasPrefix(key, "socks") {
				defProto = "socks5"
			}
			if info, ok := parseSingleProxyEndpoint(val, defProto); ok {
				return info, true
			}
		}
	}
	return SystemProxyInfo{}, false
}

func parseSingleProxyEndpoint(raw, defaultProto string) (SystemProxyInfo, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SystemProxyInfo{}, false
	}
	if !strings.Contains(raw, "://") {
		raw = defaultProto + "://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return SystemProxyInfo{}, false
	}
	scheme := strings.ToLower(u.Scheme)
	proto := defaultProto
	switch {
	case strings.HasPrefix(scheme, "socks"):
		proto = "socks5"
	case scheme == "http" || scheme == "https":
		proto = "http"
	default:
		return SystemProxyInfo{}, false
	}
	port := 0
	if pStr := u.Port(); pStr != "" {
		p, err := strconv.Atoi(pStr)
		if err != nil || p < 1 || p > 65535 {
			return SystemProxyInfo{}, false
		}
		port = p
	} else if proto == "socks5" {
		port = 1080
	} else {
		port = 8080
	}
	var user, pass string
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	return SystemProxyInfo{
		Enabled:  true,
		Protocol: proto,
		Host:     u.Hostname(),
		Port:     port,
		User:     user,
		Password: pass,
	}, true
}

func readEnvProxy() (SystemProxyInfo, bool) {
	for _, key := range []string{"ALL_PROXY", "all_proxy", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		val := strings.TrimSpace(os.Getenv(key))
		if val == "" {
			continue
		}
		defProto := "http"
		if strings.HasPrefix(strings.ToLower(val), "socks") {
			defProto = "socks5"
		}
		if info, ok := parseSingleProxyEndpoint(val, defProto); ok {
			info.Source = "env"
			info.Bypass = strings.TrimSpace(os.Getenv("NO_PROXY"))
			if info.Bypass == "" {
				info.Bypass = strings.TrimSpace(os.Getenv("no_proxy"))
			}
			info.Summary = fmt.Sprintf("%s://%s", info.Protocol, net.JoinHostPort(info.Host, strconv.Itoa(info.Port)))
			return info, true
		}
	}
	return SystemProxyInfo{}, false
}

// ResolveSystemProxy returns the ProxyConfig for targetAddr when "system" routing is selected.
// Returns (ProxyConfig{}, false) if no system proxy is active or if targetAddr matches bypass rules (direct).
func ResolveSystemProxy(targetAddr string) (ProxyConfig, bool) {
	info := DetectSystemProxy()
	if !info.Enabled {
		return ProxyConfig{}, false
	}
	host := targetAddr
	if h, _, err := net.SplitHostPort(targetAddr); err == nil {
		host = h
	}
	if shouldBypassProxy(host, info.Bypass) {
		return ProxyConfig{}, false
	}
	return ProxyConfig{
		Type:     info.Protocol,
		Host:     info.Host,
		Port:     info.Port,
		User:     info.User,
		Password: info.Password,
	}, true
}

// ShouldBypass reports whether host matches the detected Windows system proxy bypass list.
func (s SystemProxyInfo) ShouldBypass(host string) bool {
	return shouldBypassProxy(host, s.Bypass)
}

func shouldBypassProxy(host, bypassList string) bool {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if host == "" {
		return true
	}
	if host == "localhost" {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
		return true
	}
	if strings.TrimSpace(bypassList) == "" {
		return false
	}
	ip, ipErr := netip.ParseAddr(host)
	isIP := ipErr == nil
	tokens := strings.FieldsFunc(bypassList, func(r rune) bool { return r == ';' || r == ',' || r == ' ' })
	for _, rawRule := range tokens {
		rule := strings.ToLower(strings.TrimSpace(rawRule))
		if rule == "" {
			continue
		}
		if rule == "<local>" {
			if !strings.Contains(host, ".") && !isIP {
				return true
			}
			continue
		}
		if rule == "*" {
			return true
		}
		// CIDR rule (e.g. 10.0.0.0/8, 192.168.0.0/16)
		if isIP && strings.Contains(rule, "/") {
			if pfx, err := netip.ParsePrefix(rule); err == nil && pfx.Contains(ip) {
				return true
			}
			continue
		}
		// Wildcard match (e.g. 192.168.*, *.local, 10.*)
		if strings.Contains(rule, "*") {
			if matchWildcardSimple(rule, host) {
				return true
			}
			continue
		}
		// Leading dot domain suffix (e.g. .corp.local)
		if strings.HasPrefix(rule, ".") {
			if strings.HasSuffix(host, rule) || host == rule[1:] {
				return true
			}
			continue
		}
		if host == rule || strings.HasSuffix(host, "."+rule) {
			return true
		}
	}
	return false
}

func matchWildcardSimple(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	rest := s[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(rest, parts[i])
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(parts[i]):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}
