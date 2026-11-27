package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// termWSServer provides a loopback-only RFC 6455 binary WebSocket stream for low-latency
// full-duplex terminal I/O, bypassing Wails JSON/Base64 ExecuteScript IPC overhead.
type termWSServer struct {
	app      *App
	token    string
	listener net.Listener
	server   *http.Server
	port     int

	mu    sync.Mutex
	conns map[string]*termWSConn
}

type termWSConn struct {
	sessionID string
	raw       net.Conn
	bw        *bufio.Writer
	outCh     chan []byte
	done      chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex
}

func startTermWSServer(app *App) (*termWSServer, error) {
	var rawTok [24]byte
	if _, err := rand.Read(rawTok[:]); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &termWSServer{
		app:      app,
		token:    hex.EncodeToString(rawTok[:]),
		listener: ln,
		port:     ln.Addr().(*net.TCPAddr).Port,
		conns:    make(map[string]*termWSConn),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/term", s.handleUpgrade)
	s.server = &http.Server{Handler: mux}
	go func() {
		_ = s.server.Serve(ln)
	}()
	return s, nil
}

func (s *termWSServer) Stop() {
	if s == nil {
		return
	}
	_ = s.server.Close()
	s.mu.Lock()
	for id, c := range s.conns {
		c.Close()
		delete(s.conns, id)
	}
	s.mu.Unlock()
}

func (s *termWSServer) URLForSession(sessionID string) string {
	if s == nil || s.port == 0 {
		return ""
	}
	return "ws://127.0.0.1:" + itoa(s.port) + "/ws/term?id=" + sessionID + "&token=" + s.token
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// SendOutput delivers SSH output bytes over the active binary WebSocket if attached.
// Returns true if queued to WebSocket, false if caller should fall back to Wails EventsEmit.
func (s *termWSServer) SendOutput(sessionID string, data []byte) bool {
	if s == nil || len(data) == 0 {
		return false
	}
	s.mu.Lock()
	c := s.conns[sessionID]
	s.mu.Unlock()
	if c == nil {
		return false
	}
	return c.Enqueue(data)
}

func (s *termWSServer) CloseSession(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	c := s.conns[sessionID]
	delete(s.conns, sessionID)
	s.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

func isAllowedWSOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(origin))
	return strings.HasPrefix(lower, "http://wails.localhost") ||
		strings.HasPrefix(lower, "https://wails.localhost") ||
		strings.HasPrefix(lower, "http://127.0.0.1") ||
		strings.HasPrefix(lower, "http://localhost") ||
		strings.HasPrefix(lower, "wails://")
}

func (s *termWSServer) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if !isAllowedWSOrigin(r.Header.Get("Origin")) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	tok := q.Get("token")
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sessionID := strings.TrimSpace(q.Get("id"))
	if len(sessionID) < 1 || len(sessionID) > 80 {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}
	wsKey := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if wsKey == "" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	raw, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	if tcp, ok := raw.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}

	sum := sha1.Sum([]byte(wsKey + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		raw.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		raw.Close()
		return
	}

	c := &termWSConn{
		sessionID: sessionID,
		raw:       raw,
		bw:        bufio.NewWriterSize(raw, 64<<10),
		outCh:     make(chan []byte, 512),
		done:      make(chan struct{}),
	}

	s.mu.Lock()
	if old := s.conns[sessionID]; old != nil {
		old.Close()
	}
	s.conns[sessionID] = c
	s.mu.Unlock()

	go c.writeLoop()
	c.readLoop(rw.Reader, s)

	s.mu.Lock()
	if s.conns[sessionID] == c {
		delete(s.conns, sessionID)
	}
	s.mu.Unlock()
	c.Close()
}

func (c *termWSConn) Enqueue(data []byte) bool {
	cp := append([]byte(nil), data...)
	select {
	case <-c.done:
		return false
	case c.outCh <- cp:
		return true
	}
}

func (c *termWSConn) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.raw.Close()
	})
}

// writeLoop uses adaptive coalescing: the first chunk when idle is flushed in 0 ms
// (instant keystroke echo), while burst floods batch up to 64 KiB within 3 ms.
func (c *termWSConn) writeLoop() {
	const maxBatch = 64 << 10
	buf := make([]byte, 0, maxBatch)
	for {
		select {
		case <-c.done:
			return
		case first := <-c.outCh:
			buf = append(buf[:0], first...)
			// Drain any already-queued chunks immediately without waiting.
			for len(buf) < maxBatch {
				select {
				case more := <-c.outCh:
					buf = append(buf, more...)
				default:
					goto flush
				}
			}
		flush:
			if err := c.writeFrame(0x2, buf); err != nil {
				c.Close()
				return
			}
			// If traffic is still arriving right after a large burst, coalesce briefly (3ms)
			// so WebView2 xterm.js parses larger smooth frames instead of hundreds of tiny ones.
			if len(buf) >= 2048 && len(c.outCh) > 0 {
				time.Sleep(3 * time.Millisecond)
			}
		}
	}
}

func (c *termWSConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var hdr [10]byte
	hdr[0] = 0x80 | (opcode & 0x0F)
	n := len(payload)
	var hdrLen int
	switch {
	case n < 126:
		hdr[1] = byte(n)
		hdrLen = 2
	case n <= 65535:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hdrLen = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		hdrLen = 10
	}
	if _, err := c.bw.Write(hdr[:hdrLen]); err != nil {
		return err
	}
	if n > 0 {
		if _, err := c.bw.Write(payload); err != nil {
			return err
		}
	}
	return c.bw.Flush()
}

func (c *termWSConn) readLoop(br *bufio.Reader, srv *termWSServer) {
	var msgBuf []byte
	for {
		fin, opcode, payload, err := readWSFrame(br, 2<<20)
		if err != nil {
			return
		}
		switch opcode {
		case 0x8: // Close
			_ = c.writeFrame(0x8, nil)
			return
		case 0x9: // Ping -> Pong
			_ = c.writeFrame(0xA, payload)
		case 0xA: // Pong
			continue
		case 0x1, 0x2: // Text / Binary frame
			if fin && len(msgBuf) == 0 {
				if len(payload) > 0 {
					if sess, err := srv.app.get(c.sessionID); err == nil {
						_ = sess.Write(payload)
					}
				}
			} else {
				msgBuf = append(msgBuf[:0], payload...)
				if fin {
					if len(msgBuf) > 0 {
						if sess, err := srv.app.get(c.sessionID); err == nil {
							_ = sess.Write(msgBuf)
						}
					}
					msgBuf = msgBuf[:0]
				}
			}
		case 0x0: // Continuation
			if len(msgBuf)+len(payload) <= 2<<20 {
				msgBuf = append(msgBuf, payload...)
			}
			if fin {
				if len(msgBuf) > 0 {
					if sess, err := srv.app.get(c.sessionID); err == nil {
						_ = sess.Write(msgBuf)
					}
				}
				msgBuf = msgBuf[:0]
			}
		}
	}
}

func readWSFrame(r io.Reader, maxLen int) (fin bool, opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin = (h[0] & 0x80) != 0
	opcode = h[0] & 0x0F
	masked := (h[1] & 0x80) != 0
	length := uint64(h[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > uint64(maxLen) {
		return false, 0, nil, errors.New("frame too large")
	}
	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(r, maskKey[:]); err != nil {
			return false, 0, nil, err
		}
	}
	if length == 0 {
		return fin, opcode, nil, nil
	}
	payload = make([]byte, int(length))
	if _, err = io.ReadFull(r, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i&3]
		}
	}
	return fin, opcode, payload, nil
}
