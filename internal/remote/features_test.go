package remote

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func startForwardingSSH(t *testing.T, host ssh.Signer, user ssh.PublicKey) *testSSH {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := startSSHWithForwarding(t, l, host, user)
	return s
}

func startSSHWithForwarding(t *testing.T, l net.Listener, host ssh.Signer, user ssh.PublicKey) *testSSH {
	t.Helper()
	s := &testSSH{listener: l, terminal: make(chan [2]uint32, 32)}
	s.hostKey(host, user)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			sock, err := l.Accept()
			if err != nil {
				return
			}
			s.clients.Store(sock, true)
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer s.clients.Delete(sock)
				defer sock.Close()
				conn, channels, requests, err := ssh.NewServerConn(sock, s.server.Load())
				if err != nil {
					return
				}
				defer conn.Close()
				go func() {
					for req := range requests {
						if req.Type == "keepalive@openssh.com" {
							req.Reply(true, nil)
						} else {
							req.Reply(false, nil)
						}
					}
				}()
				for c := range channels {
					switch c.ChannelType() {
					case "direct-tcpip":
						var p struct {
							DestHost   string
							DestPort   uint32
							OriginHost string
							OriginPort uint32
						}
						if err := ssh.Unmarshal(c.ExtraData(), &p); err != nil {
							c.Reject(ssh.Prohibited, "bad direct-tcpip")
							continue
						}
						targetConn, err := net.DialTimeout("tcp", net.JoinHostPort(p.DestHost, itoa(int(p.DestPort))), 5*time.Second)
						if err != nil {
							c.Reject(ssh.ConnectionFailed, err.Error())
							continue
						}
						ch, reqs, err := c.Accept()
						if err != nil {
							targetConn.Close()
							continue
						}
						go ssh.DiscardRequests(reqs)
						go func() {
							defer ch.Close()
							defer targetConn.Close()
							done := make(chan struct{}, 2)
							go func() { io.Copy(targetConn, ch); done <- struct{}{} }()
							go func() { io.Copy(ch, targetConn); done <- struct{}{} }()
							<-done
						}()
					case "session":
						ch, reqs, err := c.Accept()
						if err == nil {
							go s.serveSession(ch, reqs)
						}
					default:
						c.Reject(ssh.UnknownChannelType, "unsupported")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		<-stopped
		s.clients.Range(func(k, _ any) bool { k.(net.Conn).Close(); return true })
		s.workers.Wait()
	})
	return s
}

func itoa(n int) string {
	return net.JoinHostPort("", string([]byte{}))[1:] + func() string {
		if n == 0 {
			return "0"
		}
		var digits []byte
		for n > 0 {
			digits = append([]byte{byte('0' + n%10)}, digits...)
			n /= 10
		}
		return string(digits)
	}()
}

func TestProxyJumpAndTunnels(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	bastionHost, _ := signer(t)
	targetHost, _ := signer(t)
	userKey, _ := signer(t)

	bastion := startForwardingSSH(t, bastionHost, userKey.PublicKey())
	target := startForwardingSSH(t, targetHost, userKey.PublicKey())

	// Start a local echo TCP backend to test -L and -D SOCKS5 tunnels.
	echoLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				io.Copy(conn, conn)
			}(c)
		}
	}()
	echoPort := echoLn.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	bAddr := bastion.listener.Addr().(*net.TCPAddr)
	tAddr := target.listener.Addr().(*net.TCPAddr)

	cfg := Config{
		Host:     "127.0.0.1",
		Port:     tAddr.Port,
		User:     "test",
		Password: "unit-password",
		Auth:     "password",
		Jump: &Config{
			Host:     "127.0.0.1",
			Port:     bAddr.Port,
			User:     "test",
			Password: "unit-password",
			Auth:     "password",
		},
	}

	s, err := Connect(ctx, cfg, Callbacks{Verify: func(_, _, _ string) bool { return true }})
	if err != nil {
		t.Fatalf("ProxyJump Connect failed: %v", err)
	}
	defer s.Close()

	// 1. Test Local Port Forwarding (-L)
	freeLn, _ := net.Listen("tcp4", "127.0.0.1:0")
	localPort := freeLn.Addr().(*net.TCPAddr).Port
	freeLn.Close()

	st, err := s.StartTunnel(TunnelSpec{
		ID:         "t-local",
		Name:       "Echo Local",
		Mode:       "local",
		BindHost:   "127.0.0.1",
		BindPort:   localPort,
		TargetHost: "127.0.0.1",
		TargetPort: echoPort,
	})
	if err != nil || !st.Active {
		t.Fatalf("StartTunnel local failed: %+v, %v", st, err)
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(localPort)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("hello-tunnel"))
	buf := make([]byte, 12)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello-tunnel" {
		t.Fatalf("local tunnel echo mismatch: %q, %v", string(buf), err)
	}
	c.Close()

	// 2. Test Dynamic SOCKS5 Forwarding (-D)
	freeLn2, _ := net.Listen("tcp4", "127.0.0.1:0")
	socksPort := freeLn2.Addr().(*net.TCPAddr).Port
	freeLn2.Close()

	st2, err := s.StartTunnel(TunnelSpec{
		ID:       "t-socks",
		Name:     "SOCKS5",
		Mode:     "dynamic",
		BindHost: "127.0.0.1",
		BindPort: socksPort,
	})
	if err != nil || !st2.Active {
		t.Fatalf("StartTunnel dynamic failed: %+v, %v", st2, err)
	}
	sc, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(socksPort)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	// SOCKS5 handshake: VER=5, NMETHODS=1, METHOD=0
	_, _ = sc.Write([]byte{0x05, 0x01, 0x00})
	hs := make([]byte, 2)
	if _, err := io.ReadFull(sc, hs); err != nil || hs[0] != 0x05 || hs[1] != 0x00 {
		t.Fatalf("SOCKS5 handshake failed: %x, %v", hs, err)
	}
	// CONNECT 127.0.0.1:echoPort
	req := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(req[8:10], uint16(echoPort))
	_, _ = sc.Write(req)
	rep := make([]byte, 10)
	if _, err := io.ReadFull(sc, rep); err != nil || rep[1] != 0x00 {
		t.Fatalf("SOCKS5 CONNECT failed: %x, %v", rep, err)
	}
	_, _ = sc.Write([]byte("socks5-ok"))
	sbuf := make([]byte, 9)
	if _, err := io.ReadFull(sc, sbuf); err != nil || string(sbuf) != "socks5-ok" {
		t.Fatalf("SOCKS5 payload mismatch: %q, %v", string(sbuf), err)
	}
	if len(s.ListTunnels()) != 2 {
		t.Fatalf("expected 2 active tunnels, got %d", len(s.ListTunnels()))
	}
	s.StopTunnel("t-local")
	if len(s.ListTunnels()) != 1 {
		t.Fatalf("expected 1 active tunnel after stop, got %d", len(s.ListTunnels()))
	}
}

func TestSFTPRichOperations(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	host, _ := signer(t)
	user, _ := signer(t)
	server := startSSH(t, host, user.PublicKey())
	addr := server.listener.Addr().(*net.TCPAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := Connect(ctx, Config{Host: "127.0.0.1", Port: addr.Port, User: "test", Password: "unit-password", Auth: "password"}, Callbacks{
		Verify: func(_, _, _ string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Mkdir("/app/config"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/app/config/nginx.conf", "worker_processes auto;\n"); err != nil {
		t.Fatal(err)
	}
	txt, err := s.ReadText("/app/config/nginx.conf")
	if err != nil || !strings.Contains(txt, "worker_processes") {
		t.Fatalf("ReadText mismatch: %q, %v", txt, err)
	}
	if err := s.Rename("/app/config/nginx.conf", "/app/config/app.conf"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("/"); err == nil {
		t.Fatal("root delete must be blocked")
	}
	if err := s.Delete("/app"); err != nil {
		t.Fatalf("recursive delete failed: %v", err)
	}
}

func TestTelemetryParser(t *testing.T) {
	sample := strings.Join([]string{
		"H:prod-tokyo-01",
		"K:Linux 6.8.0-45-generic",
		"O:Ubuntu 24.04.1 LTS",
		"U:86420",
		"L:0.42 0.35 0.28",
		"C:4",
		"P:100 0 50 850 0 0 0 120 0 60 920 0 0 0",
		"M:8192 2048",
		"D:52428800 20971520 40%",
		"N:1048576 524288",
	}, "\n")
	st := parseTelemetry(sample)
	if st.Hostname != "prod-tokyo-01" || st.CPUCores != 4 || st.MemTotalMB != 8192 || st.MemUsedMB != 2048 || st.DiskPercent != 40 {
		t.Fatalf("unexpected parsed telemetry: %+v", st)
	}
	if st.CPUPercent < 29 || st.CPUPercent > 31 {
		t.Fatalf("expected ~30%% CPU, got %v", st.CPUPercent)
	}
	if !bytes.Equal([]byte(st.Load1), []byte("0.42")) || st.NetRxBytes != 1048576 {
		t.Fatalf("unexpected load/net: %+v", st)
	}
}
