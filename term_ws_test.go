package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTerminalOriginIsAnExactLocalOrigin(t *testing.T) {
	for _, origin := range []string{"http://wails.localhost", "http://localhost:5173", "http://[::1]:5173", "wails://wails", ""} {
		if !isAllowedWSOrigin(origin) {
			t.Errorf("rejected local origin: %s", origin)
		}
	}
	for _, origin := range []string{"https://wails.localhost.example", "http://localhost.example", "https://localhost/a", "wails://example.test"} {
		if isAllowedWSOrigin(origin) {
			t.Errorf("accepted nonlocal or malformed origin: %s", origin)
		}
	}
}

func TestTermWSServerAuthOriginAndBinaryFraming(t *testing.T) {
	app := newApp()
	srv, err := startTermWSServer(app)
	if err != nil {
		t.Fatalf("startTermWSServer failed: %v", err)
	}
	defer srv.Stop()

	// 1. Reject bad token
	resp, err := http.Get("http://127.0.0.1:" + itoa(srv.port) + "/ws/term?id=s1&token=wrong")
	if err != nil {
		t.Fatalf("http get failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad token, got %d", resp.StatusCode)
	}

	// 2. Reject evil Origin
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+itoa(srv.port)+"/ws/term?id=s1&token="+srv.token, nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http do failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for foreign origin, got %d", resp2.StatusCode)
	}

	// 3. Valid RFC 6455 upgrade + instant binary output delivery
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(srv.port), 2*time.Second)
	if err != nil {
		t.Fatalf("dial ws failed: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	handshake := "GET /ws/term?id=test-sess&token=" + srv.token + " HTTP/1.1\r\n" +
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

	// Wait briefly for server map registration then emit binary payload (including >126 bytes).
	time.Sleep(25 * time.Millisecond)
	want := bytes.Repeat([]byte("hello-ssh-stream!"), 20)
	if ok := srv.SendOutput("test-sess", want); !ok {
		t.Fatalf("expected SendOutput to deliver to attached WebSocket")
	}

	fin, opcode, got, err := readWSFrame(br, 1<<20)
	if err != nil {
		t.Fatalf("readWSFrame failed: %v", err)
	}
	if !fin || opcode != 0x2 || !bytes.Equal(got, want) {
		t.Fatalf("unexpected ws frame: fin=%v opcode=%d len=%d want=%d", fin, opcode, len(got), len(want))
	}

	// Verify client->server Ping receives Pong cleanly
	var pingFrame [6]byte
	pingFrame[0] = 0x89 // FIN + Ping
	pingFrame[1] = 0x80 // Masked, len 0
	binary.BigEndian.PutUint32(pingFrame[2:6], 0x12345678)
	_, _ = conn.Write(pingFrame[:])
	_, pongOp, _, err := readWSFrame(br, 1024)
	if err != nil || pongOp != 0xA {
		t.Fatalf("expected pong (0xA), got op=%x err=%v", pongOp, err)
	}

	// Natural exit must drain queued output before sending the close frame. Some
	// payloads cross the batching boundary; others share a batch with the close.
	final := [][]byte{
		bytes.Repeat([]byte("w"), 64<<10),
		bytes.Repeat([]byte("v"), 64<<10),
		[]byte("last terminal line\r\n"),
	}
	for _, chunk := range final {
		if !srv.SendOutput("test-sess", chunk) {
			t.Fatal("terminal disconnected before its final output was queued")
		}
	}
	finished := make(chan struct{})
	go func() {
		srv.FinishSession("test-sess")
		close(finished)
	}()
	var received bytes.Buffer
	for {
		_, op, payload, err := readWSFrame(br, 1<<20)
		if err != nil {
			t.Fatalf("read final terminal output: %v", err)
		}
		if op == 0x8 {
			break
		}
		if op != 0x2 {
			t.Fatalf("unexpected opcode %x", op)
		}
		received.Write(payload)
	}
	if !bytes.Equal(received.Bytes(), bytes.Join(final, nil)) {
		t.Fatalf("terminal output before close: got %d bytes", received.Len())
	}
	<-finished
	if ok := srv.SendOutput("test-sess", []byte("after-close")); ok {
		t.Fatal("expected SendOutput after FinishSession to return false")
	}
}
