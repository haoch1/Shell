package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type testSSH struct {
	listener net.Listener
	server   atomic.Pointer[ssh.ServerConfig]
	handlers sftp.Handlers
	terminal chan [2]uint32
	workers  sync.WaitGroup
	clients  sync.Map
}

func signer(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return s, key
}

func (s *testSSH) hostKey(host ssh.Signer, user ssh.PublicKey) {
	c := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if meta.User() == "test" && string(pass) == "unit-password" {
				return nil, nil
			}
			return nil, errors.New("wrong test password")
		},
		PublicKeyCallback: func(meta ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() == "test" && bytes.Equal(k.Marshal(), user.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unrecognized test key")
		},
	}
	c.AddHostKey(host)
	s.server.Store(c)
}

func startSSH(t *testing.T, host ssh.Signer, user ssh.PublicKey) *testSSH {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testSSH{listener: l, handlers: sftp.InMemHandler(), terminal: make(chan [2]uint32, 32)}
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
				go ssh.DiscardRequests(requests)
				var channelsDone sync.WaitGroup
				for c := range channels {
					if c.ChannelType() != "session" {
						c.Reject(ssh.UnknownChannelType, "unsupported test channel")
						continue
					}
					ch, request, err := c.Accept()
					if err == nil {
						channelsDone.Add(1)
						go func() {
							defer channelsDone.Done()
							s.serveSession(ch, request)
						}()
					}
				}
				channelsDone.Wait()
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		<-stopped
		s.clients.Range(func(key, _ any) bool {
			key.(net.Conn).Close()
			return true
		})
		s.workers.Wait()
	})
	return s
}

func (s *testSSH) serveSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	var shellDone sync.WaitGroup
	defer shellDone.Wait()
	for req := range requests {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term             string
				Cols, Rows, X, Y uint32
				Modes            string
			}
			ok := ssh.Unmarshal(req.Payload, &p) == nil && p.Term == "xterm-256color"
			req.Reply(ok, nil)
			if ok {
				s.terminal <- [2]uint32{p.Cols, p.Rows}
			}
		case "window-change":
			var p struct{ Cols, Rows, X, Y uint32 }
			if ssh.Unmarshal(req.Payload, &p) == nil {
				s.terminal <- [2]uint32{p.Cols, p.Rows}
			}
		case "shell":
			req.Reply(true, nil)
			shellDone.Add(1)
			go func() {
				defer shellDone.Done()
				buf := make([]byte, 4096)
				for {
					n, err := ch.Read(buf)
					if n > 0 {
						if string(buf[:n]) == "quit\n" {
							ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
							ch.Close()
							return
						}
						ch.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}()
		case "subsystem":
			var name struct{ Name string }
			if ssh.Unmarshal(req.Payload, &name) != nil || name.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			fs := sftp.NewRequestServer(ch, s.handlers)
			fs.Serve()
			fs.Close()
			return
		default:
			req.Reply(false, nil)
		}
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for loopback peer")
		var zero T
		return zero
	}
}

func TestTerminalSFTPAndPins(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	host, _ := signer(t)
	user, private := signer(t)
	server := startSSH(t, host, user.PublicKey())
	addr := server.listener.Addr().(*net.TCPAddr)
	cfg := Config{Host: "127.0.0.1", Port: addr.Port, User: "test", Password: "unit-password", Auth: "password", Cols: 93, Rows: 30}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output := make(chan []byte, 8)
	end := make(chan error, 4)
	var prompts atomic.Int32
	callbacks := Callbacks{
		Verify: func(address, algorithm, fingerprint string) bool {
			prompts.Add(1)
			return address == addr.String() && algorithm == host.PublicKey().Type() &&
				fingerprint == ssh.FingerprintSHA256(host.PublicKey())
		},
		Output: func(p []byte) { output <- p },
		End:    func(err error) { end <- err },
	}
	reject := Callbacks{Verify: func(_, _, _ string) bool { return false }}
	if _, err := Connect(ctx, cfg, reject); err == nil {
		t.Fatal("declined host key was accepted")
	}
	s, err := Connect(ctx, cfg, callbacks)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if size := receive(t, server.terminal); size != [2]uint32{93, 30} {
		t.Fatalf("initial PTY dimensions = %v", size)
	}
	if err = s.Resize(116, 42); err != nil {
		t.Fatal(err)
	}
	if size := receive(t, server.terminal); size != [2]uint32{116, 42} {
		t.Fatalf("resized PTY = %v", size)
	}
	input := []byte("\x1b[32m测试\x1b[0m\r\n")
	if err = s.Write(input); err != nil {
		t.Fatal(err)
	}
	var result []byte
	for len(result) < len(input) {
		result = append(result, receive(t, output)...)
	}
	if !bytes.Equal(result, input) {
		t.Fatal("terminal transformed UTF-8 or VT bytes")
	}

	local := t.TempDir()
	src, dst := filepath.Join(local, "source"), filepath.Join(local, "result")
	payload := bytes.Repeat([]byte{0, 0xff, 13, 10, 7}, 24000)
	if err := os.WriteFile(src, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Upload(src, "/报告.bin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Upload(src, "/报告.bin"); err == nil {
		t.Fatal("overwrote existing remote destination")
	}
	list, err := s.List("/")
	if err != nil || len(list) != 1 || list[0].Name != "报告.bin" || list[0].Size != int64(len(payload)) {
		t.Fatalf("file list = %+v, err = %v", list, err)
	}
	if err = s.Download("/报告.bin", dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(payload, b) {
		t.Fatalf("download contents differ: %v", err)
	}
	if err := s.Download("/报告.bin", dst); err == nil {
		t.Fatal("overwrote existing local destination")
	}
	if err = s.Write([]byte("quit\n")); err != nil || receive(t, end) != nil {
		t.Fatal("remote shell did not exit successfully", err)
	}

	lockedKey, err := ssh.MarshalPrivateKeyWithPassphrase(private, "loopback", []byte("unit-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.KeyPath = filepath.Join(local, "unit key")
	if err = os.WriteFile(cfg.KeyPath, pem.EncodeToMemory(lockedKey), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Auth, cfg.Passphrase, cfg.Password = "key", "unit-passphrase", ""
	s, err = Connect(ctx, cfg, callbacks)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := receive(t, end); err != nil {
		t.Fatal(err)
	}
	startTestAgent(t, private)
	cfg.Auth, cfg.KeyPath, cfg.Passphrase = "agent", "", ""
	s, err = Connect(ctx, cfg, callbacks)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := receive(t, end); err != nil {
		t.Fatal(err)
	}
	replacement, _ := signer(t)
	server.hostKey(replacement, user.PublicKey())
	if _, err = Connect(ctx, cfg, callbacks); err == nil || !strings.Contains(err.Error(), "指纹已变更") {
		t.Fatalf("expected changed-key rejection: %v", err)
	}
	if n := prompts.Load(); n != 1 {
		t.Fatalf("expected one approval prompt, got %d", n)
	}
}

func TestKeyboardInteractiveAndSecondFactor(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	host, _ := signer(t)
	user, _ := signer(t)
	server := startSSH(t, host, user.PublicKey())
	cfg := Config{Host: "127.0.0.1", Port: server.listener.Addr().(*net.TCPAddr).Port, User: "test", Auth: "password"}
	challenge := func(_ ssh.ConnMetadata, ask ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		answers, err := ask("Server login", "Test challenge", []string{"Account alias", "One-time code"}, []bool{true, false})
		if err != nil {
			return nil, err
		}
		if len(answers) != 2 || answers[0] != "example" || answers[1] != "unit-code" {
			return nil, errors.New("incorrect test challenge responses")
		}
		return nil, nil
	}
	var direct atomic.Int32
	callbacks := Callbacks{
		Verify: func(_, _, _ string) bool { return true },
		Challenge: func(name, details string, prompts []string, echo []bool) ([]string, error) {
			if name != "Server login" || details != "Test challenge" || len(prompts) != 2 ||
				prompts[0] != "Account alias" || prompts[1] != "One-time code" || len(echo) != 2 || !echo[0] || echo[1] {
				return nil, errors.New("challenge content or echo modes changed")
			}
			return []string{"example", "unit-code"}, nil
		},
	}
	for _, mode := range []string{"keyboard-only", "empty-password", "two-factor"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			config := &ssh.ServerConfig{}
			cfg.Password = ""
			direct.Store(0)
			switch mode {
			case "keyboard-only":
				// The initial form's password must not be reused as an answer to arbitrary prompts.
				cfg.Password = "initial-password"
				config.KeyboardInteractiveCallback = challenge
			case "empty-password":
				config.KeyboardInteractiveCallback = challenge
				config.PasswordCallback = func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
					direct.Add(1)
					return nil, errors.New("empty password was attempted")
				}
			case "two-factor":
				cfg.Password = "unit-password"
				config.PasswordCallback = func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
					if string(password) != "unit-password" {
						return nil, errors.New("wrong first factor")
					}
					direct.Add(1)
					return nil, &ssh.PartialSuccessError{Next: ssh.ServerAuthCallbacks{KeyboardInteractiveCallback: challenge}}
				}
			}
			config.AddHostKey(host)
			server.server.Store(config)
			s, err := Connect(ctx, cfg, callbacks)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			expected := int32(0)
			if mode == "two-factor" {
				expected = 1
			}
			if direct.Load() != expected {
				t.Fatalf("password attempts = %d, want %d", direct.Load(), expected)
			}
		})
	}
}

func TestDialCancelAndHostInput(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"[2001:db8::1]", "[2001:db8::1]:22"}, {"fe80::1%7", "[fe80::1%7]:22"}, {"Host.Example", "host.example:22"},
	} {
		if got, err := hostAddress(tc.input, 0); err != nil || got != tc.want {
			t.Fatalf("hostAddress(%q) = %q, %v", tc.input, got, err)
		}
	}
	for _, bad := range []string{"", "*", "a,b", "[::1%bad*]", "a\nb", "x:22"} {
		if _, err := hostAddress(bad, 22); err == nil {
			t.Fatalf("invalid host %q accepted", bad)
		}
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfg := Config{Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, User: "test"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := make(chan error, 1)
	go func() {
		_, err := Connect(ctx, cfg, Callbacks{})
		failed <- err
	}()
	l.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
	sock, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	cancel()
	if err := receive(t, failed); err == nil {
		t.Fatal("cancel did not stop SSH negotiation")
	}
}
