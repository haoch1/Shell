//go:build windows

package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/haoch1/Shell/internal/local"
)

func writeMaskedClientWSFrame(conn net.Conn, opcode byte, payload []byte) error {
	mask := [4]byte{0x12, 0x34, 0x56, 0x78}
	var hdr []byte
	hdr = append(hdr, 0x80|(opcode&0x0F))
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n <= 65535:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		hdr = append(hdr, 0x80|127)
		hdr = append(hdr, ext[:]...)
	}
	hdr = append(hdr, mask[:]...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i&3]
	}
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	if n > 0 {
		if _, err := conn.Write(masked); err != nil {
			return err
		}
	}
	return nil
}

func TestAppLocalTerminalLifecycleAndWebSocketIO(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())

	app := newApp()
	srv, err := startTermWSServer(app)
	if err != nil {
		t.Fatalf("startTermWSServer failed: %v", err)
	}
	app.ws = srv
	defer srv.Stop()
	defer app.DisconnectAllSessions()

	shells := app.LocalShells()
	if len(shells) == 0 {
		t.Fatal("App.LocalShells() returned empty list")
	}

	// Verify DefaultLocalShell persists cleanly in settings.
	saved, err := app.UpdatePreferences(map[string]any{"defaultLocalShell": "cmd"})
	if err != nil {
		t.Fatalf("UpdatePreferences(defaultLocalShell) failed: %v", err)
	}
	if saved.DefaultLocalShell != "cmd" {
		t.Fatalf("expected DefaultLocalShell=cmd, got %q", saved.DefaultLocalShell)
	}
	loaded, err := local.LoadSettings()
	if err != nil || loaded.DefaultLocalShell != "cmd" {
		t.Fatalf("expected persisted DefaultLocalShell=cmd, got %q err=%v", loaded.DefaultLocalShell, err)
	}

	// Reject invalid session IDs.
	if _, err := app.StartLocalTerminal("", "cmd", "", 80, 24); err == nil {
		t.Fatal("expected error for empty session id")
	}

	const sid = "local-ws-test-1"

	// Connect WS first so binary output streams straight to the client with zero drops.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(srv.port), 2*time.Second)
	if err != nil {
		t.Fatalf("dial ws failed: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	handshake := "GET /ws/term?id=" + sid + "&token=" + srv.token + " HTTP/1.1\r\n" +
		"Host: 127.0.0.1\r\n" +
		"Origin: http://wails.localhost\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(handshake)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "101 Switching Protocols") {
		t.Fatalf("expected 101 Switching Protocols, got %q err=%v", statusLine, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	time.Sleep(25 * time.Millisecond)

	// Start with empty shellID to verify DefaultLocalShell ("cmd") resolution.
	prof, err := app.StartLocalTerminal(sid, "", t.TempDir(), 90, 28)
	if err != nil {
		t.Fatalf("StartLocalTerminal failed: %v", err)
	}
	if prof.ID != "cmd" {
		t.Fatalf("expected defaultLocalShell cmd to resolve, got %+v", prof)
	}
	if app.ActiveSessionCount() != 1 {
		t.Fatalf("expected ActiveSessionCount=1, got %d", app.ActiveSessionCount())
	}

	// Duplicate session ID must be rejected.
	if _, err := app.StartLocalTerminal(sid, "cmd", "", 80, 24); err == nil {
		t.Fatal("expected duplicate session id to fail")
	}

	// Resize local terminal.
	if err := app.Resize(sid, 110, 34); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}

	// Send input over binary WebSocket and verify output returns over binary WebSocket.
	if err := writeMaskedClientWSFrame(conn, 0x2, []byte("echo WS_LOCAL_PTY_OK_99\r\n")); err != nil {
		t.Fatalf("writeMaskedClientWSFrame failed: %v", err)
	}

	// Also exercise Wails base64 Input() fallback path.
	b64Cmd := base64.StdEncoding.EncodeToString([]byte("echo B64_LOCAL_OK_77\r\n"))
	if err := app.Input(sid, b64Cmd); err != nil {
		t.Fatalf("App.Input failed: %v", err)
	}

	var received bytes.Buffer
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		_, op, payload, err := readWSFrame(br, 1<<20)
		if err != nil {
			t.Fatalf("readWSFrame failed before markers arrived (got %q): %v", received.String(), err)
		}
		if op == 0x2 || op == 0x1 {
			received.Write(payload)
			if bytes.Contains(received.Bytes(), []byte("WS_LOCAL_PTY_OK_99")) &&
				bytes.Contains(received.Bytes(), []byte("B64_LOCAL_OK_77")) {
				break
			}
		}
	}
	if !bytes.Contains(received.Bytes(), []byte("WS_LOCAL_PTY_OK_99")) ||
		!bytes.Contains(received.Bytes(), []byte("B64_LOCAL_OK_77")) {
		t.Fatalf("did not receive expected WS + Input echoes; captured=%q", received.String())
	}

	// Disconnect must cleanly remove session and tear down process tree.
	app.Disconnect(sid)
	if app.ActiveSessionCount() != 0 {
		t.Fatalf("expected ActiveSessionCount=0 after Disconnect, got %d", app.ActiveSessionCount())
	}
}
