package local

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func isolated(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	return filepath.Join(root, "Shell")
}

func TestProfileAndCredentialLifecycle(t *testing.T) {
	dir := isolated(t)
	p, err := SaveProfile(Profile{Host: "[::1]", User: "demo"})
	if err != nil || p.Host != "::1" || p.Name != "::1" || p.Port != 22 || p.Auth != "password" || p.ID == "" {
		t.Fatalf("unexpected defaults: %#v, %v", p, err)
	}
	const secret = "password-for-unit-test"
	if err := RememberSecret(p, secret); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{secretPath(dir, p.ID), filepath.Join(dir, "profiles.json")} {
		data, err := os.ReadFile(file)
		if err != nil || bytes.Contains(data, []byte(secret)) {
			t.Fatal("credential is unreadable or persisted as plaintext")
		}
	}
	p.Name = "Renamed"
	if _, err := SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadSecret(p); err != nil || got != secret {
		t.Fatalf("credential wasn't retained for display rename: %v", err)
	}
	stale := p
	p.Host = "other.example"
	if _, err := SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	for _, lookup := range []Profile{p, stale} {
		if got, err := LoadSecret(lookup); err != nil || got != "" {
			t.Fatalf("stale credential available: %v", err)
		}
	}
	if err := RememberSecret(stale, secret); err == nil {
		t.Fatal("saved a secret for a stale connection")
	}
	if err := RememberSecret(p, secret); err != nil {
		t.Fatal(err)
	}
	if err := DeleteProfile(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secretPath(dir, p.ID)); !os.IsNotExist(err) {
		t.Fatalf("credential still present: %v", err)
	}
	if list, err := Profiles(); err != nil || len(list) != 0 || list == nil {
		t.Fatalf("unexpected empty profile list: %#v, %v", list, err)
	}
}

func TestConcurrentSavesAndDamagedFile(t *testing.T) {
	dir := isolated(t)
	const count = 8
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		go func(i int) {
			_, err := SaveProfile(Profile{Host: "server-" + strconv.Itoa(i), User: "demo", Auth: "agent"})
			errs <- err
		}(i)
	}
	for i := 0; i < count; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if list, err := Profiles(); err != nil || len(list) != count {
		t.Fatalf("concurrent saves lost: %d, %v", len(list), err)
	}
	path := filepath.Join(dir, "profiles.json")
	bad := []byte("{broken")
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveProfile(Profile{Host: "server", User: "demo"}); err == nil {
		t.Fatal("overwrote corrupt profiles")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, bad) {
		t.Fatalf("damaged file modified: %v", err)
	}
}
