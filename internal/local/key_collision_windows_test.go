package local

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateKeyPreservesStandalonePublicKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "identity")
	const existing = "previous public key"
	if err := os.WriteFile(name+".pub", []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateKey("identity", "ed25519", "", ""); err == nil {
		t.Fatal("expected existing public key to prevent generation")
	}
	body, err := os.ReadFile(name + ".pub")
	if err != nil || string(body) != existing {
		t.Fatalf("existing public key changed: %q, %v", body, err)
	}
	if _, err = os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated private file was not rolled back: %v", err)
	}
}
