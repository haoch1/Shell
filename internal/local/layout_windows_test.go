package local

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/haoch1/Shell/internal/security"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestCleanV16DirectoryLayoutZeroLooseRootFiles(t *testing.T) {
	dir := isolated(t)

	// 1. Verify exported path helpers match the exact 5-folder layout.
	checks := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"DataDir", DataDir, dir},
		{"ConfigDir", ConfigDir, filepath.Join(dir, "config")},
		{"SecurityDir", SecurityDir, filepath.Join(dir, "security")},
		{"LogsDir", LogsDir, filepath.Join(dir, "logs")},
		{"RuntimeDir", RuntimeDir, filepath.Join(dir, "runtime")},
		{"UpdatesDir", UpdatesDir, filepath.Join(dir, "runtime", "updates")},
		{"WebViewCacheDir", WebViewCacheDir, filepath.Join(dir, "cache", "WebView2")},
		{"ConfigFile", func() (string, error) { return ConfigFile("settings.json") }, filepath.Join(dir, "config", "settings.json")},
	}
	for _, tc := range checks {
		got, err := tc.fn()
		if err != nil || got != tc.want {
			t.Fatalf("%s() = (%q, %v), want %q", tc.name, got, err, tc.want)
		}
	}

	// 2. Exercise every local storage surface: settings, profiles, consolidated credentials,
	// snippets, agent config, agent token, bridge runtime, known_hosts, and audit log.
	if _, err := SaveSettings(DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	p1, err := SaveProfile(Profile{Name: "web-1", Host: "10.0.0.1", User: "root", Auth: "password"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := SaveProfile(Profile{Name: "web-2", Host: "10.0.0.2", User: "root", Auth: "password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RememberSecret(p1, "secret-one"); err != nil {
		t.Fatal(err)
	}
	if err := RememberSecret(p2, "secret-two"); err != nil {
		t.Fatal(err)
	}
	if _, err := Snippets(); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveAgentConfig(DefaultAgentConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAgentToken(); err != nil {
		t.Fatal(err)
	}
	if err := WriteBridgeRuntime(17822, "pipe-layout"); err != nil {
		t.Fatal(err)
	}

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	khPath := securityPath(dir, "known_hosts")
	if err := os.WriteFile(khPath, []byte(knownhosts.Line([]string{"10.0.0.1:22"}, sshPub)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if kh, err := ListKnownHosts(); err != nil || len(kh) != 1 {
		t.Fatalf("ListKnownHosts() = (%+v, %v)", kh, err)
	}
	if err := security.AppendAudit(security.AuditEntry{
		Source:   "mcp",
		Host:     "web-1",
		Tool:     "shell_exec",
		Summary:  "uname -a",
		Risk:     "safe",
		Decision: "auto-allow",
		OK:       true,
	}); err != nil {
		t.Fatal(err)
	}

	// 3. Assert ZERO loose files in Shell/ root — ONLY clean subdirectories!
	rootEntries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rootDirs []string
	for _, entry := range rootEntries {
		if !entry.IsDir() {
			t.Fatalf("loose file found in Shell/ root: %s", entry.Name())
		}
		rootDirs = append(rootDirs, entry.Name())
	}
	slices.Sort(rootDirs)
	wantDirs := []string{"config", "logs", "runtime", "security"}
	if !slices.Equal(rootDirs, wantDirs) {
		t.Fatalf("Shell/ root dirs = %v, want %v", rootDirs, wantDirs)
	}

	// 4. Assert exact file placement in each subdirectory.
	expectedSubfiles := map[string][]string{
		"config":   {"agent.json", "profiles.json", "settings.json", "snippets.json"},
		"security": {"agent-token.dpapi", "credentials.dpapi", "known_hosts"},
		"logs":     {"audit.jsonl"},
		"runtime":  {"bridge.json", "store.lock"},
	}
	for sub, want := range expectedSubfiles {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			t.Fatalf("ReadDir(%s) failed: %v", sub, err)
		}
		var got []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "credential-") {
				t.Fatalf("legacy scattered credential file found in %s/: %s", sub, e.Name())
			}
			got = append(got, e.Name())
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("%s/ contents = %v, want %v", sub, got, want)
		}
	}
}
