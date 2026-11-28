package local

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBridgeDiscoveryIsReadOnly(t *testing.T) {
	dir := isolated(t)
	if _, _, err := ReadBridgeCredentials(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing bridge: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery created a settings directory: %v", err)
	}
	if err := WriteBridgeRuntime(17822, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtimePath(dir, "bridge.json")); err != nil {
		t.Fatalf("expected runtime/bridge.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bridge-runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected legacy bridge-runtime.json in root: %v", err)
	}
	if _, _, err := ReadBridgeCredentials(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing token: %v", err)
	}
	path := securityPath(dir, "agent-token.dpapi")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery generated a token: %v", err)
	}
	damaged := []byte("unreadable token")
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBridgeCredentials(); err == nil {
		t.Fatal("discovery accepted a damaged token")
	}
	if _, err := EnsureAgentToken(); err == nil {
		t.Fatal("reading status silently replaced a damaged token")
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, damaged) {
		t.Fatalf("discovery replaced a damaged token: %v", err)
	}
	ClearBridgeRuntime()
	if _, err := os.Stat(runtimePath(dir, "bridge.json")); !os.IsNotExist(err) {
		t.Fatalf("ClearBridgeRuntime did not remove runtime/bridge.json: %v", err)
	}
}

func TestBridgeCleanupPreservesAnotherLiveDesktop(t *testing.T) {
	dir := isolated(t)
	originalToken, err := EnsureAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBridgeRuntime(17822, ""); err != nil {
		t.Fatal(err)
	}
	other := BridgeRuntimeInfo{Port: 17824, PID: os.Getppid()}
	data, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	path := runtimePath(dir, "bridge.json")
	if err := writeAtomic(path, data); err != nil {
		t.Fatal(err)
	}
	ClearBridgeRuntime()
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(data, got) {
		t.Fatalf("removed another live desktop's discovery file: %v", err)
	}
	if _, err := RotateAgentToken(); err == nil {
		t.Fatal("a second desktop replaced the active listener's token")
	}
	if _, token, err := ReadBridgeCredentials(); err != nil || token != originalToken {
		t.Fatalf("changed the other desktop's credentials: %v", err)
	}
}

func TestBridgeDiscoveryDuringSettingsWrite(t *testing.T) {
	isolated(t)
	const port = 17822
	if err := WriteBridgeRuntime(port, ""); err != nil {
		t.Fatal(err)
	}
	check := func(wantToken string) {
		t.Helper()
		info, gotToken, err := ReadBridgeCredentials()
		if err != nil || info.Port != port || gotToken != wantToken {
			t.Fatalf("wrong bridge or token: port=%d err=%v", info.Port, err)
		}
	}
	old, err := EnsureAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	check(old)
	fresh, err := RotateAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old {
		t.Fatal("rotation kept the old token")
	}
	// Simulate an unrelated backup or profile write holding the store lock.
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- withStore(func(string) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("unable to acquire settings lock: %v", err)
	case <-entered:
	}
	defer func() {
		close(release)
		<-done
	}()
	type result struct {
		port  int
		token string
		err   error
	}
	read := make(chan result, 1)
	go func() {
		p, token, err := ReadBridgeCredentials()
		read <- result{p.Port, token, err}
	}()
	select {
	case r := <-read:
		if r.err != nil || r.port != port || r.token != fresh {
			t.Fatalf("wrong bridge or token after rotation: port=%d err=%v", r.port, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge discovery blocked on an unrelated settings write")
	}
}
