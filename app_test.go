package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haoch1/Shell/internal/local"
)

func TestExplainTerminalStopsOnInvalidRedactionSettings(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	dir := filepath.Join(root, "Shell")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	app := newApp()
	app.ctx = t.Context()
	text, err := app.ExplainTerminal("sample terminal output", "explain")
	if text != "" || err == nil || !strings.Contains(err.Error(), "脱敏设置") {
		t.Fatalf("expected redaction settings error, got %q, %v", text, err)
	}
}

func TestSaveProfileWithSecretRetainsPasswordOnEdit(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	app := newApp()

	// 1. Save a new host with "仅保存" (SaveProfileWithSecret) and password.
	created, err := app.SaveProfileWithSecret(local.Profile{
		Name: "SG - AWS",
		Host: "56.10.113.238",
		Port: 58222,
		User: "root",
		Auth: "password",
	}, "super-secret-pass", true, false)
	if err != nil {
		t.Fatalf("SaveProfileWithSecret create failed: %v", err)
	}
	if !created.Remembered {
		t.Fatal("expected created profile to have remembered=true")
	}
	gotSec, err := app.GetProfileSecret(created.ID)
	if err != nil || gotSec != "super-secret-pass" {
		t.Fatalf("GetProfileSecret returned %q, err=%v", gotSec, err)
	}

	// 2. Edit host from IPv4 to IPv6 without re-entering password (keepExisting=true).
	updated, err := app.SaveProfileWithSecret(local.Profile{
		ID:   created.ID,
		Name: "SG - AWS",
		Host: "2406:da18:b15:8000:4839:f12a:9c3b:4e21",
		Port: 58222,
		User: "root",
		Auth: "password",
	}, "", true, true)
	if err != nil {
		t.Fatalf("SaveProfileWithSecret IPv6 edit failed: %v", err)
	}
	if !updated.Remembered {
		t.Fatal("expected password to stay remembered after editing IP to IPv6")
	}
	gotSec, err = app.GetProfileSecret(updated.ID)
	if err != nil || gotSec != "super-secret-pass" {
		t.Fatalf("password lost after IP edit: %q, err=%v", gotSec, err)
	}
	saved, ok, err := local.GetProfile(updated.ID)
	if err != nil || !ok {
		t.Fatal("missing saved profile", err)
	}
	cleared, err := app.SaveProfileWithSecret(saved, "", true, false)
	if err != nil || cleared.Remembered {
		t.Fatal("explicitly cleared password was retained", err)
	}
}
