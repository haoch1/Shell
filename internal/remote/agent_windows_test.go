package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net"
	"sync"
	"testing"

	"github.com/Microsoft/go-winio"
	"golang.org/x/crypto/ssh/agent"
)

func startTestAgent(t *testing.T, private ed25519.PrivateKey) {
	t.Helper()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	path := "\\\\.\\pipe\\shell-agent-test-" + hex.EncodeToString(id[:])
	t.Setenv("SSH_AUTH_SOCK", path)
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var peers sync.Map
	var wg sync.WaitGroup
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			peers.Store(conn, true)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer peers.Delete(conn)
				defer conn.Close()
				agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-stopped
		peers.Range(func(conn, _ any) bool {
			conn.(net.Conn).Close()
			return true
		})
		wg.Wait()
	})
}
