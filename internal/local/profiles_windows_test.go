package local

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	p2, err := SaveProfile(Profile{Name: "Second", Host: "10.0.0.2", User: "ops", Auth: "password"})
	if err != nil {
		t.Fatal(err)
	}

	const secret = "password-for-unit-test"
	const secret2 = "second-host-password"
	if err := RememberSecret(p, secret); err != nil {
		t.Fatal(err)
	}
	if err := RememberSecret(p2, secret2); err != nil {
		t.Fatal(err)
	}

	vaultFile := credentialsVaultPath(dir)
	profilesFile := configPath(dir, "profiles.json")
	for _, file := range []string{vaultFile, profilesFile} {
		data, err := os.ReadFile(file)
		if err != nil || bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(secret2)) {
			t.Fatalf("credential file %s is unreadable or persisted as plaintext: %v", file, err)
		}
	}

	// Ensure NO legacy scattered credential-<hash>.dpapi files exist anywhere.
	for _, checkDir := range []string{dir, filepath.Join(dir, "security")} {
		entries, err := os.ReadDir(checkDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "credential-") {
				t.Fatalf("unexpected scattered legacy credential file: %s", filepath.Join(checkDir, e.Name()))
			}
		}
	}

	// Verify HasSecret and SecretIDs operate cleanly on the single vault.
	if has, err := HasSecret(p.ID); err != nil || !has {
		t.Fatalf("HasSecret(p) = %v, %v", has, err)
	}
	idMap, err := SecretIDs([]string{p.ID, p2.ID, "missing-id"})
	if err != nil || !idMap[p.ID] || !idMap[p2.ID] || idMap["missing-id"] {
		t.Fatalf("unexpected SecretIDs map: %+v, %v", idMap, err)
	}

	// Display rename retains credential.
	p.Name = "Renamed"
	if _, err := SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadSecret(p); err != nil || got != secret {
		t.Fatalf("credential wasn't retained for display rename: %v", err)
	}

	// Identity change clears only p's secret while preserving p2's secret in the vault.
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
	if got2, err := LoadSecret(p2); err != nil || got2 != secret2 {
		t.Fatalf("sibling credential in consolidated vault was disturbed: %q, %v", got2, err)
	}
	if err := RememberSecret(stale, secret); err == nil {
		t.Fatal("saved a secret for a stale connection")
	}
	if err := RememberSecret(p, secret); err != nil {
		t.Fatal(err)
	}

	// Deleting one profile removes only its secret; vault remains for p2.
	if err := DeleteProfile(p.ID); err != nil {
		t.Fatal(err)
	}
	if has, err := HasSecret(p.ID); err != nil || has {
		t.Fatalf("deleted profile secret still present: %v, %v", has, err)
	}
	if got2, err := LoadSecret(p2); err != nil || got2 != secret2 {
		t.Fatalf("remaining profile secret lost after sibling delete: %q, %v", got2, err)
	}

	// Deleting the last profile with a secret removes security/credentials.dpapi cleanly.
	if err := DeleteProfiles([]string{p2.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(vaultFile); !os.IsNotExist(err) {
		t.Fatalf("empty credential vault was not removed: %v", err)
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
	path := configPath(dir, "profiles.json")
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
