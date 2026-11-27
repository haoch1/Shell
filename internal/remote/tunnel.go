package remote

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// TunnelSpec defines a Local (-L), Remote (-R), or Dynamic SOCKS5 (-D) port forward.
type TunnelSpec struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Mode       string `json:"mode"` // "local", "remote", "dynamic"
	BindHost   string `json:"bindHost"`
	BindPort   int    `json:"bindPort"`
	TargetHost string `json:"targetHost,omitempty"`
	TargetPort int    `json:"targetPort,omitempty"`
}

// TunnelStatus reports live metrics for an active port forward.
type TunnelStatus struct {
	TunnelSpec
	Active      bool   `json:"active"`
	Connections int64  `json:"connections"`
	BytesIn     int64  `json:"bytesIn"`
	BytesOut    int64  `json:"bytesOut"`
	Error       string `json:"error,omitempty"`
}

type activeTunnel struct {
	spec     TunnelSpec
	listener net.Listener
	active   atomic.Bool
	conns    atomic.Int64
	bytesIn  atomic.Int64
	bytesOut atomic.Int64
	lastErr  atomic.Value
	connsMu  sync.Mutex
	openSet  map[net.Conn]struct{}
}

func (t *activeTunnel) status() TunnelStatus {
	errStr, _ := t.lastErr.Load().(string)
	return TunnelStatus{
		TunnelSpec:  t.spec,
		Active:      t.active.Load(),
		Connections: t.conns.Load(),
		BytesIn:     t.bytesIn.Load(),
		BytesOut:    t.bytesOut.Load(),
		Error:       errStr,
	}
}

func (t *activeTunnel) track(c net.Conn) {
	t.connsMu.Lock()
	t.openSet[c] = struct{}{}
	t.connsMu.Unlock()
}

func (t *activeTunnel) untrack(c net.Conn) {
	t.connsMu.Lock()
	delete(t.openSet, c)
	t.connsMu.Unlock()
}

func (t *activeTunnel) close() {
	t.active.Store(false)
	if t.listener != nil {
		_ = t.listener.Close()
	}
	t.connsMu.Lock()
	for c := range t.openSet {
		_ = c.Close()
	}
	clear(t.openSet)
	t.connsMu.Unlock()
}

// StartTunnel starts a Local, Remote, or Dynamic SOCKS5 tunnel over this SSH session.
func (s *Session) StartTunnel(spec TunnelSpec) (TunnelStatus, error) {
	if s.closed.Load() {
		return TunnelStatus{}, net.ErrClosed
	}
	spec.Mode = strings.ToLower(strings.TrimSpace(spec.Mode))
	if spec.BindHost == "" {
		spec.BindHost = "127.0.0.1"
	}
	if spec.ID == "" {
		return TunnelStatus{}, errors.New("转发规则缺少 ID")
	}
	if spec.BindPort < 1 || spec.BindPort > 65535 {
		return TunnelStatus{}, errors.New("监听端口必须在 1 到 65535 之间")
	}
	bindAddr := net.JoinHostPort(spec.BindHost, strconv.Itoa(spec.BindPort))

	var ln net.Listener
	var err error
	switch spec.Mode {
	case "local":
		if spec.TargetHost == "" {
			spec.TargetHost = "127.0.0.1"
		}
		if spec.TargetPort < 1 || spec.TargetPort > 65535 {
			return TunnelStatus{}, errors.New("目标端口必须在 1 到 65535 之间")
		}
		ln, err = net.Listen("tcp", bindAddr)
		if err != nil {
			return TunnelStatus{}, fmt.Errorf("监听本地 %s 失败：%w", bindAddr, err)
		}
	case "remote":
		if spec.TargetHost == "" {
			spec.TargetHost = "127.0.0.1"
		}
		if spec.TargetPort < 1 || spec.TargetPort > 65535 {
			return TunnelStatus{}, errors.New("目标端口必须在 1 到 65535 之间")
		}
		ln, err = s.client.Listen("tcp", bindAddr)
		if err != nil {
			return TunnelStatus{}, fmt.Errorf("远端监听 %s 失败：%w", bindAddr, err)
		}
	case "dynamic":
		ln, err = net.Listen("tcp", bindAddr)
		if err != nil {
			return TunnelStatus{}, fmt.Errorf("启动 SOCKS5 监听 %s 失败：%w", bindAddr, err)
		}
	default:
		return TunnelStatus{}, errors.New("不支持的端口转发模式")
	}

	at := &activeTunnel{
		spec:     spec,
		listener: ln,
		openSet:  make(map[net.Conn]struct{}),
	}
	at.active.Store(true)

	s.tunnelMu.Lock()
	if existing := s.tunnels[spec.ID]; existing != nil {
		existing.close()
	}
	s.tunnels[spec.ID] = at
	s.tunnelMu.Unlock()

	go s.serveTunnel(at)
	return at.status(), nil
}

func (s *Session) serveTunnel(t *activeTunnel) {
	defer t.active.Store(false)
	targetAddr := ""
	if t.spec.Mode == "local" || t.spec.Mode == "remote" {
		targetAddr = net.JoinHostPort(t.spec.TargetHost, strconv.Itoa(t.spec.TargetPort))
	}
	for {
		inConn, err := t.listener.Accept()
		if err != nil {
			if t.active.Load() && !s.closed.Load() {
				t.lastErr.Store(err.Error())
			}
			return
		}
		t.conns.Add(1)
		t.track(inConn)
		go func(c net.Conn) {
			defer func() {
				t.untrack(c)
				_ = c.Close()
				t.conns.Add(-1)
			}()
			switch t.spec.Mode {
			case "local":
				outConn, err := s.client.Dial("tcp", targetAddr)
				if err != nil {
					t.lastErr.Store(err.Error())
					return
				}
				t.pipe(c, outConn)
			case "remote":
				outConn, err := net.Dial("tcp", targetAddr)
				if err != nil {
					t.lastErr.Store(err.Error())
					return
				}
				t.pipe(c, outConn)
			case "dynamic":
				s.handleSOCKS5(t, c)
			}
		}(inConn)
	}
}

func (t *activeTunnel) pipe(a, b net.Conn) {
	t.track(b)
	defer func() {
		t.untrack(b)
		_ = b.Close()
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(b, a)
		t.bytesOut.Add(n)
		if tc, ok := b.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(a, b)
		t.bytesIn.Add(n)
		if tc, ok := a.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}()
	wg.Wait()
}

// handleSOCKS5 implements a minimal RFC 1928 SOCKS5 CONNECT proxy over the SSH connection.
func (s *Session) handleSOCKS5(t *activeTunnel, c net.Conn) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 0x05 {
		return
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	// Accept NO AUTHENTICATION REQUIRED (0x00).
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil || req[0] != 0x05 || req[1] != 0x01 {
		_, _ = c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch req[3] {
	case 0x01: // IPv4
		addr := make([]byte, 4)
		if _, err := io.ReadFull(c, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	case 0x03: // Domain
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return
		}
		dom := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, dom); err != nil {
			return
		}
		host = string(dom)
	case 0x04: // IPv6
		addr := make([]byte, 16)
		if _, err := io.ReadFull(c, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	default:
		_, _ = c.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(c, portBuf[:]); err != nil {
		return
	}
	port := int(binary.BigEndian.Uint16(portBuf[:]))
	dest := net.JoinHostPort(host, strconv.Itoa(port))

	outConn, err := s.client.Dial("tcp", dest)
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		_ = outConn.Close()
		return
	}
	t.pipe(c, outConn)
}

func (s *Session) StopTunnel(id string) {
	s.tunnelMu.Lock()
	t := s.tunnels[id]
	delete(s.tunnels, id)
	s.tunnelMu.Unlock()
	if t != nil {
		t.close()
	}
}

func (s *Session) ListTunnels() []TunnelStatus {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	out := make([]TunnelStatus, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		out = append(out, t.status())
	}
	return out
}

func (s *Session) CloseAllTunnels() {
	s.tunnelMu.Lock()
	all := make([]*activeTunnel, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		all = append(all, t)
	}
	clear(s.tunnels)
	s.tunnelMu.Unlock()
	for _, t := range all {
		t.close()
	}
}
