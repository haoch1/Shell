package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHCoreHostKeyChangedRecovery(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	host1, _ := signer(t)
	host2, _ := signer(t)
	user, _ := signer(t)

	srv := startSSH(t, host1, user.PublicKey())
	host, port := splitHostPort(t, srv.listener.Addr().String())
	cfg := Config{Host: host, Port: port, User: "test", Password: "unit-password", Auth: "password", Cols: 80, Rows: 24}

	// First connect trusts host1.
	s1, err := Connect(context.Background(), cfg, Callbacks{
		Verify: func(_, _, _ string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// Rotate server host key (simulating VPS reinstall).
	srv.hostKey(host2, user.PublicKey())

	// Without VerifyChanged callback (headless/strict), mismatch must still reject.
	if sBad, err := Connect(context.Background(), cfg, Callbacks{Verify: func(_, _, _ string) bool { return true }}); err == nil {
		sBad.Close()
		t.Fatal("expected strict reject when VerifyChanged is nil")
	}

	// Declining VerifyChanged must reject and keep old key pinned.
	var sawOldFP, sawNewFP string
	if sDecline, err := Connect(context.Background(), cfg, Callbacks{
		VerifyChanged: func(_, _, newFP, oldFP string) bool {
			sawOldFP = oldFP
			sawNewFP = newFP
			return false
		},
	}); err == nil {
		sDecline.Close()
		t.Fatal("expected reject when user declines VerifyChanged")
	}
	if sawOldFP != ssh.FingerprintSHA256(host1.PublicKey()) || sawNewFP != ssh.FingerprintSHA256(host2.PublicKey()) {
		t.Fatalf("unexpected fingerprints: old=%q new=%q", sawOldFP, sawNewFP)
	}

	// Accepting VerifyChanged must replace old pin and connect cleanly.
	s2, err := Connect(context.Background(), cfg, Callbacks{
		VerifyChanged: func(_, _, _, _ string) bool { return true },
	})
	if err != nil {
		t.Fatalf("expected reconnect after approving changed host key, got %v", err)
	}
	// Immediate liveness probe must succeed with RTT >= 1ms.
	ms, err := s2.Probe(context.Background())
	if err != nil || ms < 1 {
		t.Fatalf("expected healthy Probe RTT, got ms=%d err=%v", ms, err)
	}
	s2.Close()

	// Subsequent connection trusts host2 with zero prompts.
	s3, err := Connect(context.Background(), cfg, Callbacks{
		Verify:        func(_, _, _ string) bool { t.Fatal("should already trust rotated key"); return false },
		VerifyChanged: func(_, _, _, _ string) bool { t.Fatal("should not report mismatch after update"); return false },
	})
	if err != nil {
		t.Fatalf("expected trusted reconnect after host key update, got %v", err)
	}
	s3.Close()
}

func TestSSHCoreInteractivePasswordAndEncryptedKeyPrompt(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	hostSigner, _ := signer(t)
	userSigner, userPriv := signer(t)

	srv := startSSH(t, hostSigner, userSigner.PublicKey())
	host, port := splitHostPort(t, srv.listener.Addr().String())

	// 1. Password-only server with blank saved password must prompt via Challenge callback.
	prompted := false
	sPass, err := Connect(context.Background(), Config{
		Host: host, Port: port, User: "test", Password: "", Auth: "password", Cols: 80, Rows: 24,
	}, Callbacks{
		Verify: func(_, _, _ string) bool { return true },
		Challenge: func(_, _ string, questions []string, _ []bool) ([]string, error) {
			prompted = true
			if len(questions) != 1 {
				t.Fatalf("expected 1 password prompt, got %v", questions)
			}
			return []string{"unit-password"}, nil
		},
	})
	if err != nil {
		t.Fatalf("password interactive fallback failed: %v", err)
	}
	sPass.Close()
	if !prompted {
		t.Fatal("expected Challenge callback for blank password auth")
	}

	// 2. Encrypted key with blank saved passphrase must prompt interactively via Challenge callback.
	block, err := ssh.MarshalPrivateKeyWithPassphrase(userPriv, "test-key", []byte("enc-pass-42"))
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, append([]byte{0xEF, 0xBB, 0xBF}, pem.EncodeToMemory(block)...), 0o600); err != nil {
		t.Fatal(err)
	}

	keyPrompted := false
	sKey, err := Connect(context.Background(), Config{
		Host: host, Port: port, User: "test", Auth: "key", KeyPath: keyPath, Passphrase: "", Cols: 80, Rows: 24,
	}, Callbacks{
		Verify: func(_, _, _ string) bool { return true },
		Challenge: func(_, _ string, questions []string, _ []bool) ([]string, error) {
			keyPrompted = true
			return []string{"enc-pass-42"}, nil
		},
	})
	if err != nil {
		t.Fatalf("encrypted key interactive passphrase prompt failed: %v", err)
	}
	sKey.Close()
	if !keyPrompted {
		t.Fatal("expected Challenge callback for encrypted key passphrase")
	}
}

func TestSSHCoreCertificateAutoDiscovery(t *testing.T) {
	_, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caSigner, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatal(err)
	}
	userSigner, userPriv := signer(t)

	cert := &ssh.Certificate{
		Key:             userSigner.PublicKey(),
		Serial:          1,
		CertType:        ssh.UserCert,
		KeyId:           "test-cert",
		ValidPrincipals: []string{"test"},
		ValidAfter:      uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore:     uint64(time.Now().Add(time.Hour).Unix()),
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	block, err := ssh.MarshalPrivateKey(userPriv, "test-cert-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath+"-cert.pub", ssh.MarshalAuthorizedKey(cert), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := readKey(keyPath, "")
	if err != nil {
		t.Fatalf("readKey with cert failed: %v", err)
	}
	if !strings.HasSuffix(loaded.PublicKey().Type(), "-cert-v01@openssh.com") {
		t.Fatalf("expected OpenSSH certificate signer, got %q", loaded.PublicKey().Type())
	}
	if !bytes.Equal(loaded.PublicKey().Marshal(), cert.Marshal()) {
		t.Fatal("loaded certificate bytes mismatch")
	}
}

func TestIsFatalTransportErr(t *testing.T) {
	for _, err := range []error{
		io.EOF,
		net.ErrClosed,
		errors.New("read tcp: connection reset by peer"),
		errors.New("write: broken pipe"),
		errors.New("wsarecv: An existing connection was forcibly closed"),
	} {
		if !isFatalTransportErr(err) {
			t.Fatalf("expected fatal transport error for %v", err)
		}
	}
	if isFatalTransportErr(errors.New("temporary request timeout")) {
		t.Fatal("transient error must not be marked fatal")
	}
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	for _, ch := range p {
		port = port*10 + int(ch-'0')
	}
	return h, port
}
