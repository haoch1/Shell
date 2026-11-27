package local

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

var wideCharToMultiByte = windows.NewLazySystemDLL("kernel32.dll").NewProc("WideCharToMultiByte")
var puttyRegEscape = strings.NewReplacer("\\", "\\\\", "\"", "\\\"")

// PuTTY 0.85 windows/controls.c and windows/storage.c use native ANSI strings for session names.
// Check that the conversion round trips, since Windows may silently best-fit unsupported characters.
func puttyNativeBytes(s string, cp uint32) (string, bool) {
	if cp == 65001 || s == "" {
		return s, utf8.ValidString(s)
	}
	wide := utf16.Encode([]rune(s))
	out := make([]byte, len(wide)*4)
	n, _, _ := wideCharToMultiByte.Call(uintptr(cp), 0, uintptr(unsafe.Pointer(&wide[0])), uintptr(len(wide)),
		uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), 0, 0)
	if n == 0 {
		return "", false
	}
	raw := string(out[:n])
	return raw, puttyNativeString(raw, cp) == s
}

func puttyNativeString(raw string, cp uint32) string {
	if raw == "" || cp == 65001 {
		return raw
	}
	b := []byte(raw)
	wide := make([]uint16, len(b))
	n, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), &wide[0], int32(len(wide)))
	if err != nil {
		return raw
	}
	return string(utf16.Decode(wide[:n]))
}

func decodePuTTYName(raw string) string { return puttyNativeString(raw, windows.GetACP()) }

// Mirrors PuTTY's escape_registry_key byte rules, including leading dot and literal plus.
func puttyKeyName(raw string) string {
	const digits = "0123456789ABCDEF"
	var key strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c <= ' ' || c > '~' || strings.IndexByte("\\*?%", c) >= 0 || (i == 0 && c == '.') {
			key.WriteByte('%')
			key.WriteByte(digits[c>>4])
			key.WriteByte(digits[c&15])
		} else {
			key.WriteByte(c)
		}
	}
	return key.String()
}

func renderPuTTY(selected, all []Profile) ([]byte, []string, error) {
	var out strings.Builder
	out.WriteString("Windows Registry Editor Version 5.00\r\n")
	used := map[string]bool{"default%20settings": true}
	var warnings []string
	cp := windows.GetACP()
	wanted := make(map[string]bool, len(selected))
	for _, p := range selected {
		wanted[p.ID] = true
	}
	if len(all) == 0 {
		all = selected
	}
	// Allocate duplicate names in stable profile ID order regardless of selection or display order.
	all = slices.Clone(all)
	slices.SortStableFunc(all, func(a, b Profile) int { return strings.Compare(a.ID, b.ID) })
	for _, p := range all {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = p.Host
		}
		native, representable := puttyNativeBytes(name, cp)
		base := puttyKeyName(native)
		fallback := !representable || base == "" || len(base) > 255 || strings.IndexFunc(name, unicode.IsControl) >= 0
		if fallback {
			nativeHost, ok := puttyNativeBytes(p.Host, cp)
			base = puttyKeyName(nativeHost)
			if !ok || base == "" || len(base) > 255 || strings.IndexFunc(p.Host, unicode.IsControl) >= 0 {
				base = "Shell"
			}
		}
		key := base
		for suffix := 2; used[strings.ToLower(key)]; suffix++ {
			key = fmt.Sprintf("%s%%20(%d)", base, suffix)
			if len(key) > 255 {
				key = fmt.Sprintf("Shell%%20(%d)", suffix)
			}
		}
		used[strings.ToLower(key)] = true
		if !wanted[p.ID] {
			continue
		}
		if p.Host == "" || p.Port < 1 || p.Port > 65535 || p.KeepAliveSec < 0 ||
			((p.ProxyType == "http" || p.ProxyType == "socks5") && (p.ProxyHost == "" || p.ProxyPort < 1 || p.ProxyPort > 65535)) {
			return nil, nil, errors.New("PuTTY 会话的主机、端口或代理设置无效")
		}
		if fallback {
			warnings = append(warnings, fmt.Sprintf("“%s”的会话名超出 PuTTY 本机字符集或长度限制，已使用备用名称", name))
		}
		if key != base {
			warnings = append(warnings, fmt.Sprintf("“%s”的 PuTTY 会话名重名或保留，已添加序号", name))
		}
		fmt.Fprintf(&out, "\r\n[HKEY_CURRENT_USER\\Software\\SimonTatham\\PuTTY\\Sessions\\%s]\r\n", key)
		text := func(k, value string) {
			fmt.Fprintf(&out, "\"%s\"=\"%s\"\r\n", k, puttyRegEscape.Replace(value))
		}
		dword := func(k string, n int) { fmt.Fprintf(&out, "\"%s\"=dword:%08x\r\n", k, n) }
		for _, v := range []string{p.Host, p.User, p.KeyPath, p.ProxyHost, p.ProxyUser} {
			if strings.IndexFunc(v, unicode.IsControl) >= 0 {
				return nil, nil, errors.New("PuTTY 字符串设置包含不支持的控制字符")
			}
		}
		dword("Present", 1)
		text("Protocol", "ssh")
		text("HostName", p.Host)
		dword("PortNumber", p.Port)
		text("UserName", p.User)
		for _, v := range []string{p.Host, p.User, p.ProxyHost, p.ProxyUser} {
			if _, ok := puttyNativeBytes(v, cp); !ok {
				warnings = append(warnings, fmt.Sprintf("“%s”的连接字段包含 PuTTY 本机字符集无法显示的字符，请在导入后核对", name))
				break
			}
		}
		dword("PingInterval", p.KeepAliveSec/60)
		dword("PingIntervalSecs", p.KeepAliveSec%60)
		if p.ForwardAgent {
			dword("AgentFwd", 1)
		} else {
			dword("AgentFwd", 0)
		}
		if p.Auth == "agent" {
			dword("TryAgent", 1)
			warnings = append(warnings, fmt.Sprintf("“%s”使用代理认证，请在 PuTTY 的 Pageant 中准备相应密钥", name))
		} else {
			dword("TryAgent", 0)
		}
		keyPath := ""
		if p.Auth == "key" {
			if _, ok := puttyNativeBytes(p.KeyPath, cp); ok && strings.EqualFold(filepath.Ext(p.KeyPath), ".ppk") {
				keyPath = p.KeyPath
			} else {
				warnings = append(warnings, fmt.Sprintf("“%s”需要选择本机可读的 .ppk 文件；OpenSSH 私钥可先用 PuTTYgen 转换", name))
			}
		}
		text("PublicKeyFile", keyPath)
		method := 0
		switch p.ProxyType {
		case "socks5":
			method = 2
		case "http":
			method = 3
		case "", "none", "direct":
		default:
			warnings = append(warnings, fmt.Sprintf("“%s”的代理方式需要在 PuTTY 中手动配置", name))
		}
		dword("ProxyMethod", method)
		if method != 0 {
			text("ProxyHost", p.ProxyHost)
			dword("ProxyPort", p.ProxyPort)
			text("ProxyUsername", p.ProxyUser)
		}
		if p.JumpHostID != "" || p.StartupCommand != "" || len(p.Tunnels) != 0 {
			warnings = append(warnings, fmt.Sprintf("“%s”的跳板、启动命令或转发规则需要在 PuTTY 中手动配置", name))
		}
	}
	units := utf16.Encode([]rune(out.String()))
	data := make([]byte, 2+len(units)*2)
	binary.LittleEndian.PutUint16(data, 0xfeff)
	for i, c := range units {
		binary.LittleEndian.PutUint16(data[2+i*2:], c)
	}
	return data, warnings, nil
}
