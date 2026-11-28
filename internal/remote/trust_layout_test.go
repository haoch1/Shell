package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestHostVerifierStoresKnownHostsAndLockInSubdirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	cb, err := hostVerifier(t.Context(), func(string, string, string) bool { return true }, nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}
	if err := cb("127.0.0.1:2222", peer, sshPub); err != nil {
		t.Fatalf("hostVerifier accept failed: %v", err)
	}

	shellDir := filepath.Join(root, "Shell")
	if _, err := os.Stat(filepath.Join(shellDir, "security", "known_hosts")); err != nil {
		t.Fatalf("expected security/known_hosts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(shellDir, "runtime", "known_hosts.lock")); err != nil {
		t.Fatalf("expected runtime/known_hosts.lock: %v", err)
	}
	entries, err := os.ReadDir(shellDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Fatalf("loose file in Shell/ root after host verification: %s", e.Name())
		}
	}
}
