package local

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func sshExportFixtures() []Profile {
	return []Profile{
		{ID: "c", Name: "生产机", Host: "app.example.test", Port: 2222, User: "deploy", Auth: "key",
			KeyPath: `C:\Test Keys\ops #1`, ForwardAgent: true, KeepAliveSec: 45, JumpHostID: "a",
			ProxyType: "http", ProxyUser: "private-proxy-account", Notes: "private-notes",
			StartupCommand: "private-startup-cmd", Tunnels: []TunnelRule{{Name: "private-tunnel"}}},
		{ID: "a", Name: "入口 *", Host: "gw.example.test", Port: 2200, User: "ops", Auth: "agent", JumpHostID: "b"},
		{ID: "b", Name: "Gate #2", Host: "192.0.2.1", Port: 22, User: "admin", Auth: "password"},
		{ID: "d", Name: "Other", Host: "other.example.test", Port: 22, User: "owner"},
	}
}

func TestOpenSSHExportSelectionAndRoundTrip(t *testing.T) {
	all := sshExportFixtures()
	data, count, warnings, err := renderHostExport(all, []string{"c", "c"}, "openssh")
	if err != nil || count != 3 || len(warnings) != 3 {
		t.Fatalf("count=%d warnings=%v error=%v", count, warnings, err)
	}
	config := string(data)
	for _, expected := range []string{"# 生产机\nHost app.example.test\n", `IdentityFile "C:/Test Keys/ops #1"`, "ProxyJump gw.example.test", "ProxyJump gate-2"} {
		if !strings.Contains(config, expected) {
			t.Errorf("missing %q in %s", expected, config)
		}
	}
	for _, forbidden := range []string{"other.example.test", "private-", "ProxyCommand", "RemoteCommand", "LocalCommand"} {
		if strings.Contains(config, forbidden) {
			t.Errorf("unexpected %q in %s", forbidden, config)
		}
	}
	again := slices.Clone(all)
	slices.Reverse(again)
	reordered, _, _, err := renderHostExport(again, []string{"b", "c"}, "openssh")
	if err != nil || string(reordered) != config {
		t.Fatalf("selection order or duplicate jump changed the result: %v", err)
	}
	imported := parseOpenSSHConfigText(config)
	if len(imported) != count {
		t.Fatalf("imported %d hosts, want %d", len(imported), count)
	}
	byHost := make(map[string]ImportedHost)
	for _, h := range imported {
		byHost[h.Host] = h
	}
	app := byHost["app.example.test"]
	if app.User != "deploy" || app.Port != 2222 || app.Auth != "key" || app.KeyPath != filepath.Clean(all[0].KeyPath) ||
		!app.ForwardAgent || app.KeepAliveSec != 45 || app.ProxyJump != "gw.example.test" || byHost["192.0.2.1"].Auth != "password" {
		t.Fatalf("imported settings changed: %+v", imported)
	}
}

func TestOpenSSHExportAliasesAndInvalidFields(t *testing.T) {
	all := []Profile{
		{ID: "a", Name: "WEB"},
		{ID: "b", Name: "Web *"},
		{ID: "c", Name: "web-2"},
		{ID: "d", Name: "none"},
	}
	aliases := sshAliases(all)
	if aliases["a"] != "web" || aliases["b"] != "web-3" || aliases["c"] != "web-2" || aliases["d"] != "host-none" {
		t.Fatalf("ambiguous aliases: %v", aliases)
	}
	base := Profile{ID: "a", Name: "中文\r\nHost * #", Host: "127.0.0.1", Port: 22, User: `DOMAIN\Test User`, Auth: "key",
		KeyPath: "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-key-body"}
	data, _, warnings, err := renderHostExport([]Profile{base}, nil, "openssh")
	if err != nil || len(warnings) != 1 || strings.Contains(string(data), "private-key") || strings.Contains(string(data), "\nHost *") {
		t.Fatalf("unsafe data in export: %q %v %v", data, warnings, err)
	}
	roundTrip := parseOpenSSHConfigText(string(data))
	if len(roundTrip) != 1 || roundTrip[0].User != base.User {
		t.Fatalf("quoted username changed: %+v", roundTrip)
	}
	for _, change := range []func(*Profile){
		func(p *Profile) { p.User = "user\r\nHost *" },
		func(p *Profile) { p.Host = "fe80::1%invalid;zone" },
		func(p *Profile) { p.JumpHostID = p.ID },
		func(p *Profile) { p.JumpHostID = "missing" },
	} {
		p := base
		change(&p)
		if data, _, _, err := renderHostExport([]Profile{p}, nil, "openssh"); err == nil || len(data) != 0 {
			t.Fatalf("invalid profile produced output: %v", err)
		}
	}
	if _, _, _, err := renderHostExport(all, []string{"missing"}, "openssh"); err == nil {
		t.Fatal("missing selection exported unrelated profiles")
	}
}

func TestExportHostFileUsesPreferencesAndPreservesDestination(t *testing.T) {
	isolated(t)
	p, err := SaveProfile(Profile{Name: "Demo", Host: "192.0.2.10", User: "admin", Auth: "password"})
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.ProxyMode = "system"
	settings.DefaultKeepAlive = 71
	if _, err := SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "ssh_config")
	count, warnings, err := ExportHostFile(dest, nil, "openssh")
	if err != nil || count != 1 || len(warnings) != 1 {
		t.Fatalf("count=%d warnings=%v err=%v", count, warnings, err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || !strings.Contains(string(data), "ServerAliveInterval 71") {
		t.Fatalf("missing inherited keepalive: %q %v", data, err)
	}
	if _, _, err := ExportHostFile(dest, []string{p.ID, "missing"}, "openssh"); err == nil {
		t.Fatal("an outdated selection overwrote the destination")
	}
	after, err := os.ReadFile(dest)
	if err != nil || string(after) != string(data) {
		t.Fatalf("existing file changed on failure: %v", err)
	}
}

func TestOpenSSHExportWithNativeClient(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("Windows CI needs the OpenSSH client for the ssh_config compatibility test")
		}
		t.Skip("OpenSSH client unavailable")
	}
	data, _, _, err := renderHostExport(sshExportFixtures(), []string{"c"}, "openssh")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(dest, data, 0600); err != nil {
		t.Fatal(err)
	}
	// ssh -G prints evaluated options and exits before connecting. -F isolates the test from the user's configuration.
	for alias, expected := range map[string][]string{
		"app.example.test": {"hostname app.example.test", "user deploy", "port 2222", "proxyjump gw.example.test",
			"identityfile C:/Test Keys/ops #1", "serveraliveinterval 45", "forwardagent yes"},
		"gw.example.test": {"hostname gw.example.test", "port 2200", "proxyjump gate-2"},
		"gate-2":          {"hostname 192.0.2.1", "port 22", "user admin"},
	} {
		t.Run(alias, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, ssh, "-G", "-F", dest, alias)
			out, err := cmd.Output()
			if err != nil {
				if e, ok := err.(*exec.ExitError); ok {
					t.Logf("OpenSSH: %s", e.Stderr)
				}
				t.Fatal(err)
			}
			lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
			for _, want := range expected {
				if !slices.Contains(lines, want) {
					t.Errorf("ssh -G did not report %q", want)
				}
			}
		})
	}
}
