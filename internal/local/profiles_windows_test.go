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

func TestSetProfilesGroup(t *testing.T) {
	isolated(t)
	p1, err := SaveProfile(Profile{Name: "web-1", Host: "10.0.0.1", User: "root", Auth: "agent", Group: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := SaveProfile(Profile{Name: "web-2", Host: "10.0.0.2", User: "root", Auth: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	p3, err := SaveProfile(Profile{Name: "db-1", Host: "10.0.0.3", User: "root", Auth: "agent", Group: "DB"})
	if err != nil {
		t.Fatal(err)
	}

	if err := SetProfilesGroup([]string{p1.ID, p2.ID}, "  Prod  "); err != nil {
		t.Fatalf("SetProfilesGroup assign failed: %v", err)
	}
	for _, id := range []string{p1.ID, p2.ID} {
		got, ok, err := GetProfile(id)
		if err != nil || !ok || got.Group != "Prod" {
			t.Fatalf("profile %s group = %q, ok=%v, err=%v", id, got.Group, ok, err)
		}
	}
	if got, ok, err := GetProfile(p3.ID); err != nil || !ok || got.Group != "DB" {
		t.Fatalf("unselected profile group changed: %q, ok=%v, err=%v", got.Group, ok, err)
	}

	// Ungroup selected hosts
	if err := SetProfilesGroup([]string{p1.ID}, ""); err != nil {
		t.Fatalf("SetProfilesGroup ungroup failed: %v", err)
	}
	if got, ok, err := GetProfile(p1.ID); err != nil || !ok || got.Group != "" {
		t.Fatalf("profile p1 was not ungrouped: %q, ok=%v, err=%v", got.Group, ok, err)
	}

	// Reject control characters
	if err := SetProfilesGroup([]string{p2.ID}, "bad\ngroup"); err == nil {
		t.Fatal("expected error for control character in group name")
	}
}
