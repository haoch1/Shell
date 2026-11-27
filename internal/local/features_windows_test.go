package local

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/haoch1/Shell/internal/security"
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

func TestImportPreservesJumpHostAfterSkippedInvalidProfile(t *testing.T) {
	isolated(t)
	// First entry has invalid host and will be skipped; third entry jumps via second entry.
	bundle := ExportBundle{
		Version: 2,
		Profiles: []Profile{
			{ID: "bad", Name: "Bad", Host: "bad host with spaces", Port: 22, User: "root", Auth: "password"},
			{ID: "bastion", Name: "Bastion", Host: "203.0.113.10", Port: 22, User: "ops", Auth: "agent"},
			{ID: "app", Name: "App", Host: "10.0.1.50", Port: 22, User: "ops", Auth: "agent", JumpHostID: "bastion"},
		},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := ImportData(raw, "")
	if err != nil || sum.Profiles != 2 {
		t.Fatalf("unexpected backup import summary: %+v, %v", sum, err)
	}
	profs, err := Profiles()
	if err != nil || len(profs) != 2 {
		t.Fatalf("unexpected profiles: %+v, %v", profs, err)
	}
	if profs[1].JumpHostID != profs[0].ID {
		t.Fatalf("expected App JumpHostID=%q (Bastion), got %q", profs[0].ID, profs[1].JumpHostID)
	}

	// Same check for OpenSSH config import.
	isolated(t)
	imported, err := ImportSSHConfigEntries([]SSHConfigHost{
		{Alias: "invalid", HostName: "invalid host!", Port: 22, User: "ops"},
		{Alias: "jump", HostName: "203.0.113.20", Port: 22, User: "ops"},
		{Alias: "target", HostName: "10.0.2.20", Port: 22, User: "ops", ProxyJump: "jump"},
	}, "Imported")
	if err != nil || len(imported) != 2 {
		t.Fatalf("unexpected SSH config import: %+v, %v", imported, err)
	}
	if imported[1].JumpHostID != imported[0].ID {
		t.Fatalf("expected target JumpHostID=%q (jump), got %q", imported[0].ID, imported[1].JumpHostID)
	}
}

func TestCappedBufferAndUTF8BoundaryHelpers(t *testing.T) {
	cb := cappedBuffer{limit: 8}
	n, err := cb.Write([]byte("0123456789abcdef"))
	if err != nil || n != 16 || cb.buf.String() != "01234567" {
		t.Fatalf("unexpected cappedBuffer write: n=%d err=%v got=%q", n, err, cb.buf.String())
	}

	sample := []byte("中文测试")
	ready, carry := splitValidUTF8(sample[:4]) // splits 2nd Chinese character after 1 byte
	if string(ready) != "中" || len(carry) != 1 {
		t.Fatalf("unexpected splitValidUTF8: ready=%q carry=%x", string(ready), carry)
	}
	merged := append(append([]byte(nil), carry...), sample[4:]...)
	ready2, carry2 := splitValidUTF8(merged)
	if string(ready2) != "文测试" || len(carry2) != 0 {
		t.Fatalf("unexpected merged UTF-8: ready=%q carry=%x", string(ready2), carry2)
	}
	if tr := truncateUTF8("中文测试数据", 7); !utf8.ValidString(tr) || tr != "中文" {
		t.Fatalf("truncateUTF8 produced %q", tr)
	}
}

func TestAgentConfigPolicyVersionMigration(t *testing.T) {
	dir := isolated(t)

	// 1. Fresh install defaults to PolicyVersion=2 and ModeTrusted.
	def, err := LoadAgentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if def.PolicyVersion != 2 || def.Policy.Mode != security.ModeTrusted {
		t.Fatalf("unexpected default agent config: version=%d mode=%q", def.PolicyVersion, def.Policy.Mode)
	}

	// 2. Legacy v1.0/v1.1 agent.json (no policyVersion, mode=smart) auto-migrates once to ModeTrusted + PolicyVersion=2.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	legacySmart := `{"enabled":true,"port":17822,"execMode":"visible","policy":{"mode":"smart"}}`
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(legacySmart), 0600); err != nil {
		t.Fatal(err)
	}
	migrated, err := LoadAgentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Policy.Mode != security.ModeTrusted || migrated.PolicyVersion != 2 || !migrated.Enabled || migrated.ExecMode != "visible" {
		t.Fatalf("expected legacy smart config to migrate to trusted v2, got %+v", migrated)
	}

	// 3. Explicit user save of smart/readonly/ask_all (even with PolicyVersion=0 from frontend) stamps v2 and persists forever.
	for _, explicitMode := range []security.PermissionMode{security.ModeSmart, security.ModeReadOnly, security.ModeAskAll} {
		toSave := migrated
		toSave.PolicyVersion = 0
		toSave.Policy.Mode = explicitMode
		saved, err := SaveAgentConfig(toSave)
		if err != nil {
			t.Fatal(err)
		}
		if saved.PolicyVersion != 2 || saved.Policy.Mode != explicitMode {
			t.Fatalf("SaveAgentConfig failed to preserve explicit mode %q: %+v", explicitMode, saved)
		}
		reloaded, err := LoadAgentConfig()
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.PolicyVersion != 2 || reloaded.Policy.Mode != explicitMode {
			t.Fatalf("LoadAgentConfig overwrote explicit mode %q: %+v", explicitMode, reloaded)
		}
	}

	// 4. Legacy agent.json with explicit readonly/ask_all (no policyVersion) must NOT be forced to trusted.
	legacyReadOnly := `{"enabled":false,"port":17822,"policy":{"mode":"readonly"}}`
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(legacyReadOnly), 0600); err != nil {
		t.Fatal(err)
	}
	ro, err := LoadAgentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ro.Policy.Mode != security.ModeReadOnly || ro.PolicyVersion != 2 {
		t.Fatalf("legacy readonly config should remain readonly at v2, got %+v", ro)
	}
}

func TestSettingsGroupOrderAndSyntaxHighlight(t *testing.T) {
	dir := isolated(t)

	// 1. Default settings enable SyntaxHighlight=true and empty GroupOrder.
	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.SyntaxHighlight == nil || !*s.SyntaxHighlight {
		t.Fatalf("expected default SyntaxHighlight=true, got %+v", s.SyntaxHighlight)
	}
	if len(s.GroupOrder) != 0 {
		t.Fatalf("expected empty default GroupOrder, got %v", s.GroupOrder)
	}

	// 2. Legacy settings.json without syntaxHighlight still defaults to true.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"dark","fontSize":15}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Theme != "dark" || loaded.SyntaxHighlight == nil || !*loaded.SyntaxHighlight {
		t.Fatalf("legacy settings should default SyntaxHighlight=true, got %+v", loaded)
	}

	// 3. UpdateSettings mutating *SyntaxHighlight in-place (just like json.Unmarshal in UpdatePreferences) MUST persist false!
	updated, err := UpdateSettings(func(cur *AppSettings) error {
		return json.Unmarshal([]byte(`{"syntaxHighlight":false,"groupOrder":[" Prod ","","Staging","Prod","Dev  "]}`), cur)
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SyntaxHighlight == nil || *updated.SyntaxHighlight {
		t.Fatalf("expected SyntaxHighlight=false after update, got %+v", updated.SyntaxHighlight)
	}
	wantOrder := []string{"Prod", "Staging", "Dev"}
	if !reflect.DeepEqual(updated.GroupOrder, wantOrder) {
		t.Fatalf("expected sanitized GroupOrder %v, got %v", wantOrder, updated.GroupOrder)
	}
	reloaded, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SyntaxHighlight == nil || *reloaded.SyntaxHighlight {
		t.Fatalf("SyntaxHighlight=false did not persist on disk: %+v", reloaded.SyntaxHighlight)
	}
	if !reflect.DeepEqual(reloaded.GroupOrder, wantOrder) {
		t.Fatalf("GroupOrder did not persist on disk: got %v want %v", reloaded.GroupOrder, wantOrder)
	}

	// 4. Clearing groupOrder to empty slice removes it cleanly.
	cleared, err := UpdateSettings(func(cur *AppSettings) error {
		return json.Unmarshal([]byte(`{"groupOrder":[]}`), cur)
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.GroupOrder != nil {
		t.Fatalf("expected nil GroupOrder after clearing, got %v", cleared.GroupOrder)
	}
	reloaded2, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded2.GroupOrder != nil || reloaded2.SyntaxHighlight == nil || *reloaded2.SyntaxHighlight {
		t.Fatalf("unexpected settings after clearing GroupOrder: %+v", reloaded2)
	}
}
