package local

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHConfigParserAndImport(t *testing.T) {
	isolated(t)
	tmp := t.TempDir()
	keyFile := filepath.Join(tmp, "id_test")
	if err := os.WriteFile(keyFile, []byte("dummy"), 0600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, "config")
	cfg := `
# Global defaults
Host *
  ServerAliveInterval 45
  User defaultuser

Host bastion
  HostName 203.0.113.10
  Port 2222
  User ops
  IdentityFile ` + keyFile + `

Host prod-web
  HostName 10.0.1.50
  ProxyJump bastion
  ForwardAgent yes

Host *.ignored
  HostName 10.0.0.1
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	hosts, err := ParseSSHConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 concrete hosts, got %d: %+v", len(hosts), hosts)
	}
	if hosts[0].Alias != "bastion" || hosts[0].Port != 2222 || hosts[0].User != "ops" || hosts[0].KeepAliveSec != 45 {
		t.Fatalf("unexpected bastion entry: %+v", hosts[0])
	}
	if hosts[1].Alias != "prod-web" || hosts[1].ProxyJump != "bastion" || !hosts[1].ForwardAgent || hosts[1].User != "defaultuser" {
		t.Fatalf("unexpected prod-web entry: %+v", hosts[1])
	}

	imported, err := ImportSSHConfigEntries(hosts, "Imported")
	if err != nil || len(imported) != 2 {
		t.Fatalf("import failed: %+v, %v", imported, err)
	}
	if imported[0].Auth != "key" || imported[1].JumpHostID != imported[0].ID {
		t.Fatalf("expected key auth and linked ProxyJump ID, got %+v", imported)
	}
}

func TestSnippetsAndSettingsLifecycle(t *testing.T) {
	isolated(t)
	seed, err := Snippets()
	if err != nil || len(seed) < 5 {
		t.Fatalf("expected starter snippets, got %d, %v", len(seed), err)
	}
	saved, err := SaveSnippet(Snippet{Name: "Ping", Category: "网络", Command: "ping -c 4 {{host}}"})
	if err != nil || saved.ID == "" {
		t.Fatalf("save snippet failed: %+v, %v", saved, err)
	}
	if err := DeleteSnippet(saved.ID); err != nil {
		t.Fatal(err)
	}

	s, err := LoadSettings()
	if err != nil || s.Theme != "light" || s.FontSize != 14 {
		t.Fatalf("unexpected default settings: %+v, %v", s, err)
	}
	s.Theme = "dark"
	s.FontSize = 16
	s.CopyOnSelect = true
	savedSet, err := SaveSettings(s)
	if err != nil || savedSet.Theme != "dark" || savedSet.FontSize != 16 || !savedSet.CopyOnSelect {
		t.Fatalf("settings round-trip failed: %+v, %v", savedSet, err)
	}
}

func TestKeyGenerationAndKnownHosts(t *testing.T) {
	dir := isolated(t)
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)

	info, err := GenerateKey("id_ed25519_unit", "ed25519", "top-secret", "unit@shell")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasPublic || !info.Encrypted || !strings.HasPrefix(info.PublicKey, "ssh-ed25519 ") || !strings.HasPrefix(info.Fingerprint, "SHA256:") {
		t.Fatalf("unexpected key info: %+v", info)
	}
	// Refuses overwrite
	if _, err := GenerateKey("id_ed25519_unit", "ed25519", "", ""); err == nil {
		t.Fatal("expected duplicate key generation to fail")
	}
	list, err := ListLocalKeys()
	if err != nil || len(list) != 1 || list[0].Name != "id_ed25519_unit" {
		t.Fatalf("ListLocalKeys = %+v, %v", list, err)
	}

	// Known hosts list & remove
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	khLine := "example.com " + info.PublicKey + "\n"
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(khLine), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := ListKnownHosts()
	if err != nil || len(entries) != 1 || entries[0].Hosts[0] != "example.com" {
		t.Fatalf("ListKnownHosts = %+v, %v", entries, err)
	}
	if err := RemoveKnownHost(entries[0].Line); err != nil {
		t.Fatal(err)
	}
	entries, err = ListKnownHosts()
	if err != nil || len(entries) != 0 {
		t.Fatalf("expected empty known_hosts after remove, got %+v, %v", entries, err)
	}
}

func TestEncryptedVaultExportImport(t *testing.T) {
	isolated(t)
	p, err := SaveProfile(Profile{Name: "Prod", Group: "Cloud", Host: "10.0.0.2", Port: 22, User: "deploy", Auth: "password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RememberSecret(p, "super-secret-pw"); err != nil {
		t.Fatal(err)
	}
	raw, err := ExportData(true, "vault-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret-pw") {
		t.Fatal("secret leaked in encrypted export")
	}
	// Fresh isolated store
	isolated(t)
	if _, err := ImportData(raw, "wrong-pass"); err == nil {
		t.Fatal("expected wrong passphrase to fail")
	}
	sum, err := ImportData(raw, "vault-pass-123")
	if err != nil || sum.Profiles != 1 || sum.Secrets != 1 {
		t.Fatalf("unexpected import summary: %+v, %v", sum, err)
	}
	profiles, err := Profiles()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profiles after import: %+v, %v", profiles, err)
	}
	sec, err := LoadSecret(profiles[0])
	if err != nil || sec != "super-secret-pw" {
		t.Fatalf("restored secret mismatch: %q, %v", sec, err)
	}
}

func TestStructuredAIStreamingAndRiskClassifier(t *testing.T) {
	isolated(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"command\\\":\\\"rm -rf /tmp/test\\\",\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"\\\"explanation\\\":\\\"强制删除临时目录\\\",\\\"risk\\\":\\\"safe\\\"}\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	if err := SaveAI(AISettings{Endpoint: srv.URL + "/v1", Model: "test-model"}); err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	res, err := SuggestStructured(context.Background(), "delete tmp", "Ubuntu 24.04", func(d string) {
		streamed.WriteString(d)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Command != "rm -rf /tmp/test" || res.Risk != "danger" {
		t.Fatalf("expected heuristic to elevate rm -rf risk to danger, got %+v", res)
	}
	if streamed.Len() == 0 {
		t.Fatal("expected SSE delta callbacks")
	}
}
