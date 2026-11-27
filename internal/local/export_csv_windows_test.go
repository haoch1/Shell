package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetcattyCSVExportFullRoundTripWithSecrets(t *testing.T) {
	isolated(t)

	bastion, err := SaveProfile(Profile{
		Name:  "HK - Bastion",
		Group: "线路机",
		Tags:  []string{"edge", "hk"},
		Host:  "203.0.113.10",
		Port:  58222,
		User:  "ops",
		Auth:  "password",
		Notes: "Primary jump box",
	})
	if err != nil {
		t.Fatal(err)
	}
	bastionPass := "=P@ss-with,+comma\"quote!"
	if err := RememberSecret(bastion, bastionPass); err != nil {
		t.Fatal(err)
	}

	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, []byte("dummy-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := SaveProfile(Profile{
		Name:       "SG - IPv6 App",
		Group:      "落地机",
		Tags:       []string{"prod", "ipv6"},
		Host:       "2406:da18::4e21",
		Port:       22,
		User:       "root",
		Auth:       "key",
		KeyPath:    keyFile,
		ProxyType:  "socks5",
		ProxyHost:  "127.0.0.1",
		ProxyPort:  7890,
		ProxyUser:  "alice",
		JumpHostID: bastion.ID,
		Notes:      "Production API",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RememberSecret(target, "+KeyPass@2026"); err != nil {
		t.Fatal(err)
	}

	// Export only target: dependent bastion must be auto-included with its verbatim DPAPI password.
	dest := filepath.Join(t.TempDir(), "Shell-hosts.csv")
	count, warnings, err := ExportHostFile(dest, []string{target.ID}, "csv")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 exported hosts (target + jump), got %d", count)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "跳板机") {
		t.Fatalf("expected dependent jump host warning, got %v", warnings)
	}

	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), utf8BOM+"Groups,Label,Tags,Notes,Hostname/IP,Protocol,Port,Username,Password,KeyPath,Passphrase,Proxy,ProxyJump,LocalShell,SshInputCompatibility\r\n") {
		t.Fatalf("expected UTF-8 BOM + Netcatty 15-column CRLF header, got %q", string(raw[:min(180, len(raw))]))
	}
	if !strings.Contains(string(raw), "[2406:da18::4e21]") {
		t.Fatalf("expected bracketed IPv6 in CSV: %s", string(raw))
	}
	if !strings.Contains(string(raw), "ops@203.0.113.10:58222") {
		t.Fatalf("expected ProxyJump hop in CSV: %s", string(raw))
	}
	if !strings.Contains(string(raw), "socks5://alice@127.0.0.1:7890") {
		t.Fatalf("expected socks5 proxy URL in CSV: %s", string(raw))
	}

	// Round-trip into a brand-new clean store via Shell's universal importer.
	isolated(t)
	parsed, err := ParseImportBytes("Shell-hosts.csv", raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.DetectedFormat != "Netcatty (.csv)" {
		t.Fatalf("expected Netcatty (.csv) format detection, got %q", parsed.DetectedFormat)
	}
	if len(parsed.Hosts) != 2 {
		t.Fatalf("expected 2 parsed hosts, got %d", len(parsed.Hosts))
	}
	saved, err := ImportParsedHosts(parsed.Hosts, "")
	if err != nil {
		t.Fatal(err)
	}
	all, err := Profiles()
	if err != nil || len(all) != 2 {
		t.Fatalf("expected 2 re-imported profiles, got %d (%v)", len(all), err)
	}
	byName := make(map[string]Profile, len(all))
	for _, p := range all {
		byName[p.Name] = p
	}
	reBastion := byName["HK - Bastion"]
	reTarget := byName["SG - IPv6 App"]
	if reBastion.Group != "线路机" || reBastion.Port != 58222 || reBastion.User != "ops" {
		t.Fatalf("bastion metadata lost on round-trip: %+v", reBastion)
	}
	gotPass, err := LoadSecret(reBastion)
	if err != nil || gotPass != bastionPass {
		t.Fatalf("expected verbatim password %q after round-trip, got %q (err=%v)", bastionPass, gotPass, err)
	}
	if reTarget.Host != "2406:da18::4e21" || reTarget.JumpHostID != reBastion.ID || reTarget.ProxyType != "socks5" || reTarget.ProxyPort != 7890 || reTarget.ProxyUser != "alice" {
		t.Fatalf("target connection fields lost on round-trip: %+v (bastion=%s)", reTarget, reBastion.ID)
	}
	gotKeyPass, err := LoadSecret(reTarget)
	if err != nil || gotKeyPass != "+KeyPass@2026" {
		t.Fatalf("expected verbatim key passphrase after round-trip, got %q (err=%v)", gotKeyPass, err)
	}
	_ = saved
}

func TestNetcattyCSVExportSanitizedStripsSecrets(t *testing.T) {
	isolated(t)

	p, err := SaveProfile(Profile{
		Name:  "=FormulaName",
		Group: "测试",
		Host:  "10.0.0.5",
		Port:  22,
		User:  "root",
		Auth:  "password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RememberSecret(p, "SuperSecret123!"); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "sanitized.csv")
	count, _, err := ExportHostFile(dest, nil, "csv-sanitized")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 host, got %d", count)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "SuperSecret123!") {
		t.Fatalf("sanitized CSV must not leak secrets: %s", text)
	}
	if !strings.Contains(text, "'=FormulaName") {
		t.Fatalf("expected formula guard on metadata Label cell: %s", text)
	}
}
