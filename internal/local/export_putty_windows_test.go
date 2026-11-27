package local

import (
	"bytes"
	"strings"
	"testing"
)

func TestPuTTYNamingCodePages(t *testing.T) {
	for _, tc := range []struct {
		name string
		cp   uint32
		key  string
	}{
		{".A+B /%\\*?", 1252, "%2EA+B%20/%25%5C%2A%3F"},
		{"测试", 936, "%B2%E2%CA%D4"},
		{"café", 1252, "caf%E9"},
		{"测试", 65001, "%E6%B5%8B%E8%AF%95"},
	} {
		raw, ok := puttyNativeBytes(tc.name, tc.cp)
		if !ok || puttyKeyName(raw) != tc.key || puttyNativeString(raw, tc.cp) != tc.name {
			t.Fatalf("bad PuTTY name mapping for code page %d, key %q", tc.cp, puttyKeyName(raw))
		}
	}
	if _, ok := puttyNativeBytes("😀", 1252); ok {
		t.Fatal("accepted a lossy substitution")
	}
}

func TestPuTTYExportRoundTrip(t *testing.T) {
	p := Profile{Name: ".Build + 1", Host: "example.test", Port: 2222, User: "user\"one", Auth: "key",
		KeyPath: "D:\\keys\\test.PPK", ProxyType: "http", ProxyHost: "proxy.test", ProxyPort: 9080,
		ProxyUser: "proxy-user", KeepAliveSec: 125, ForwardAgent: true,
		StartupCommand: "sensitive-startup", AgentAccess: "allow", Notes: "sensitive-note"}
	raw, warnings, err := renderPuTTY([]Profile{p}, nil)
	if err != nil || len(warnings) == 0 || !bytes.HasPrefix(raw, []byte{0xff, 0xfe}) {
		t.Fatalf("invalid export: %v, warnings=%v", err, warnings)
	}
	text := decodeTextAuto(raw)
	if !strings.HasPrefix(text, "Windows Registry Editor Version 5.00\r\n\r\n[HKEY_CURRENT_USER\\Software\\SimonTatham\\PuTTY\\Sessions\\%2EBuild%20+%201]\r\n") ||
		!strings.Contains(text, "\"ProxyMethod\"=dword:00000003\r\n") || strings.Contains(text, "sensitive-") || strings.Contains(text, "Password") {
		t.Fatal("unexpected registry header, proxy encoding or private fields")
	}
	result, err := ParseImportBytes("test.reg", raw)
	if err != nil || len(result.Hosts) != 1 {
		t.Fatalf("round trip host count: %v (%d)", err, len(result.Hosts))
	}
	h := result.Hosts[0]
	if h.Name != p.Name || h.Host != p.Host || h.Port != p.Port || h.User != p.User || h.KeyPath != p.KeyPath ||
		h.Auth != p.Auth || h.ProxyType != p.ProxyType || h.ProxyHost != p.ProxyHost || h.ProxyPort != p.ProxyPort ||
		h.ProxyUser != p.ProxyUser || h.KeepAliveSec != p.KeepAliveSec || h.ForwardAgent != p.ForwardAgent {
		t.Fatalf("unexpected field mapping: %+v", h)
	}
}

func TestPuTTYExportCollisionsAndUnsupportedKeys(t *testing.T) {
	base := Profile{Host: "ssh.example", Port: 22, User: "用户", Auth: "key", KeyPath: "D:\\keys\\id_ed25519"}
	profiles := make([]Profile, 0, 5)
	for _, name := range []string{"prod", "PROD", "Default Settings", "prod (2)", "开发🚀"} {
		p := base
		p.Name = name
		profiles = append(profiles, p)
	}
	raw, warnings, err := renderPuTTY(profiles, nil)
	if err != nil || len(warnings) == 0 {
		t.Fatalf("unexpected result: %v, %v", err, warnings)
	}
	text := decodeTextAuto(raw)
	for _, key := range []string{"\\prod]", "\\PROD%20(2)]", "\\Default%20Settings%20(2)]", "\\prod%20(2)%20(2)]"} {
		if !strings.Contains(text, key+"\r\n") {
			t.Fatalf("missing unique session %q", key)
		}
	}
	if strings.Contains(text, "id_ed25519") || strings.Contains(text, "\\Default%20Settings]\r\n") ||
		!strings.Contains(text, "\"UserName\"=\"用户\"\r\n") || !strings.Contains(strings.Join(warnings, "\n"), "PuTTYgen") {
		t.Fatal("invalid key mapping, encoding or reserved settings key")
	}
	p := base
	p.User = "bad\r\n[other]"
	if data, _, err := renderPuTTY([]Profile{p}, nil); err == nil || data != nil {
		t.Fatal("unsafe control characters in a quoted .reg value were accepted")
	}
}
