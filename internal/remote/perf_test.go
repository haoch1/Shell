package remote

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type perfTestSSH struct {
	listener   net.Listener
	server     *ssh.ServerConfig
	handshakes atomic.Int32
	execOpens  atomic.Int32
	ptyDelay   time.Duration
	execDelay  time.Duration
	cmdDelay   time.Duration
	workers    sync.WaitGroup
	clients    sync.Map
}

func startPerfSSH(t *testing.T, host ssh.Signer, ptyDelay, execDelay, cmdDelay time.Duration) *perfTestSSH {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sc := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if meta.User() == "test" && string(pass) == "unit-password" {
				return nil, nil
			}
			return nil, fmt.Errorf("bad auth")
		},
	}
	sc.AddHostKey(host)
	s := &perfTestSSH{
		listener:  l,
		server:    sc,
		ptyDelay:  ptyDelay,
		execDelay: execDelay,
		cmdDelay:  cmdDelay,
	}
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
				conn, channels, requests, err := ssh.NewServerConn(sock, s.server)
				if err != nil {
					return
				}
				s.handshakes.Add(1)
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

func (s *perfTestSSH) serveSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	var wg sync.WaitGroup
	defer wg.Wait()
	for req := range requests {
		switch req.Type {
		case "pty-req":
			if s.ptyDelay > 0 {
				time.Sleep(s.ptyDelay)
			}
			req.Reply(true, nil)
		case "shell":
			req.Reply(true, nil)
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = io.Copy(ch, ch)
			}()
		case "exec":
			var p struct{ Command string }
			if ssh.Unmarshal(req.Payload, &p) != nil {
				req.Reply(false, nil)
				continue
			}
			s.execOpens.Add(1)
			if s.execDelay > 0 {
				time.Sleep(s.execDelay)
			}
			req.Reply(true, nil)
			if p.Command == "sh -s" {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s.serveShWorker(ch)
				}()
			} else {
				wg.Add(1)
				go func(cmd string) {
					defer wg.Done()
					if s.cmdDelay > 0 {
						time.Sleep(s.cmdDelay)
					}
					_, _ = io.WriteString(ch, "oneshot:"+cmd)
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
					ch.Close()
				}(p.Command)
			}
		default:
			req.Reply(false, nil)
		}
	}
}

// serveShWorker emulates `sh -s` for our sentinel framing protocol in pure Go (platform-independent on Windows).
func (s *perfTestSSH) serveShWorker(ch ssh.Channel) {
	r := bufio.NewReader(ch)
	var cmdLines []string
	inBlock := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "{":
			inBlock = true
			cmdLines = nil
		case trimmed == "}" && inBlock:
			inBlock = false
		case inBlock:
			cmdLines = append(cmdLines, trimmed)
		case strings.HasPrefix(trimmed, "printf '\\n__SH_MCP_END_") && strings.Contains(trimmed, ">&2"):
			start := strings.Index(trimmed, "__SH_MCP_END_")
			end := strings.Index(trimmed[start:], "\\n'")
			if start >= 0 && end > 0 {
				marker := trimmed[start : start+end]
				_, _ = io.WriteString(ch.Stderr(), "\n"+marker+"\n")
			}
		case strings.HasPrefix(trimmed, "printf '\\n__SH_MCP_END_") && strings.Contains(trimmed, ":%d"):
			// Extract stdout marker: printf '\n__SH_MCP_END_<hex>__:%d\n' "$__sh_ec"
			if s.cmdDelay > 0 {
				time.Sleep(s.cmdDelay)
			}
			cmdText := strings.Join(cmdLines, "\n")
			if strings.HasPrefix(cmdText, "sleep ") {
				if d, pErr := time.ParseDuration(strings.TrimPrefix(cmdText, "sleep ")); pErr == nil {
					time.Sleep(d)
				}
			}
			// Extract token between '\n and :%d
			start := strings.Index(trimmed, "__SH_MCP_END_")
			end := strings.Index(trimmed, ":%d")
			if start >= 0 && end > start {
				marker := trimmed[start:end]
				out := "worker:" + cmdText
				_, _ = io.WriteString(ch, out+"\n"+marker+":0\n")
			}
		}
	}
}

func TestJumpHostReuseAndBorrowedClientLifecycle(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	bastionKey, _ := signer(t)
	targetKey, _ := signer(t)

	bastion := startPerfSSH(t, bastionKey, 0, 0, 0)
	target := startPerfSSH(t, targetKey, 0, 0, 0)
	bPort := bastion.listener.Addr().(*net.TCPAddr).Port
	tPort := target.listener.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1. Dial bastion once and reuse via ParentClient (borrowed, must NOT close with child).
	jumpCfg := Config{Host: "127.0.0.1", Port: bPort, User: "test", Password: "unit-password", Auth: "password"}
	cb := Callbacks{Verify: func(_, _, _ string) bool { return true }}

	bastionClient, closeBastion, err := DialClient(ctx, ctx, jumpCfg, cb)
	if err != nil {
		t.Fatalf("DialClient bastion failed: %v", err)
	}
	defer closeBastion()

	targetCfg := Config{
		Host:         "127.0.0.1",
		Port:         tPort,
		User:         "test",
		Password:     "unit-password",
		Auth:         "password",
		ParentClient: bastionClient,
	}
	s1, err := ConnectHeadless(ctx, targetCfg, cb)
	if err != nil {
		t.Fatalf("first jumped ConnectHeadless failed: %v", err)
	}
	s2, err := ConnectHeadless(ctx, targetCfg, cb)
	if err != nil {
		t.Fatalf("second jumped ConnectHeadless failed: %v", err)
	}
	if got := bastion.handshakes.Load(); got != 1 {
		t.Fatalf("expected 1 bastion SSH handshake via ParentClient reuse, got %d", got)
	}

	// Closing child s1 must NOT close the borrowed bastionClient!
	s1.Close()
	if _, _, err := bastionClient.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatalf("borrowed bastion client was closed when child s1 closed: %v", err)
	}
	res, err := s2.ExecDetailed(ctx, "ping", 4096)
	if err != nil || !strings.Contains(res.Stdout, "ping") {
		t.Fatalf("child s2 failed after s1 closed: res=%+v err=%v", res, err)
	}
	s2.Close()

	// 2. DialJump hook + OnJumpDead stale transport recovery.
	var mu sync.Mutex
	pooled := bastionClient
	var releases atomic.Int32
	var deadCallbacks atomic.Int32
	var extraClosers []func()
	defer func() {
		for _, fn := range extraClosers {
			fn()
		}
	}()

	pooledCfg := Config{
		Host:     "127.0.0.1",
		Port:     tPort,
		User:     "test",
		Password: "unit-password",
		Auth:     "password",
		Jump:     &jumpCfg,
		DialJump: func(dCtx context.Context, jCfg Config, jCB Callbacks) (*ssh.Client, bool, func(), error) {
			mu.Lock()
			c := pooled
			mu.Unlock()
			if c != nil {
				return c, false, func() { releases.Add(1) }, nil
			}
			fresh, closeFn, err := DialClient(ctx, dCtx, jCfg, jCB)
			if err != nil {
				return nil, false, nil, err
			}
			mu.Lock()
			pooled = fresh
			extraClosers = append(extraClosers, closeFn)
			mu.Unlock()
			return fresh, false, func() { releases.Add(1) }, nil
		},
		OnJumpDead: func(_ Config, dead *ssh.Client) bool {
			deadCallbacks.Add(1)
			mu.Lock()
			if pooled == dead {
				pooled = nil
			}
			mu.Unlock()
			return true
		},
	}

	s3, err := ConnectHeadless(ctx, pooledCfg, cb)
	if err != nil {
		t.Fatalf("pooled DialJump ConnectHeadless failed: %v", err)
	}
	if got := bastion.handshakes.Load(); got != 1 {
		t.Fatalf("expected bastion handshake count to remain 1 on pool hit, got %d", got)
	}
	s3.Close()
	if releases.Load() != 1 {
		t.Fatalf("expected 1 jump release callback on s3.Close(), got %d", releases.Load())
	}

	// Simulate NAT dropping the pooled bastion connection; next child must transparently evict & redial once.
	closeBastion()
	s4, err := ConnectHeadless(ctx, pooledCfg, cb)
	if err != nil {
		t.Fatalf("expected transparent retry after dead pooled jump, got %v", err)
	}
	defer s4.Close()
	if deadCallbacks.Load() != 1 || bastion.handshakes.Load() != 2 {
		t.Fatalf("expected 1 OnJumpDead and 2 total bastion handshakes, got dead=%d handshakes=%d",
			deadCallbacks.Load(), bastion.handshakes.Load())
	}
}

func TestWarmExecWorkerConcurrentAndFastFirstExec(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	hostKey, _ := signer(t)
	// Deliberate 140ms exec channel-open penalty vs 5ms persistent command latency.
	srv := startPerfSSH(t, hostKey, 0, 140*time.Millisecond, 5*time.Millisecond)
	port := srv.listener.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cfg := Config{Host: "127.0.0.1", Port: port, User: "test", Password: "unit-password", Auth: "password"}
	s, err := ConnectHeadless(ctx, cfg, Callbacks{Verify: func(_, _, _ string) bool { return true }})
	if err != nil {
		t.Fatalf("ConnectHeadless failed: %v", err)
	}
	defer s.Close()

	// Wait for the background WarmExecWorker started by ConnectHeadless to finish.
	s.WarmExecWorker()
	if opens := srv.execOpens.Load(); opens != 1 {
		t.Fatalf("expected exactly 1 pre-warmed exec channel open, got %d", opens)
	}

	// First ExecDetailed must reuse the warmed worker (no 140ms channel open delay, no 2nd channel!).
	t0 := time.Now()
	res, err := s.ExecDetailed(ctx, "hello-world", 4096)
	elapsed := time.Since(t0)
	if err != nil || res.Stdout != "worker:hello-world" || res.ExitCode != 0 {
		t.Fatalf("unexpected warmed ExecDetailed result: %+v, err=%v", res, err)
	}
	if opens := srv.execOpens.Load(); opens != 1 {
		t.Fatalf("first ExecDetailed opened a duplicate exec channel (opens=%d)", opens)
	}
	if elapsed >= 120*time.Millisecond {
		t.Fatalf("first ExecDetailed took %v (expected < 120ms via pre-warmed worker)", elapsed)
	}

	// True concurrent ExecDetailed calls: one uses persistent worker while the second fans out to execOneShot.
	var wg sync.WaitGroup
	wg.Add(2)
	var r1, r2 ExecResult
	var e1, e2 error
	go func() {
		defer wg.Done()
		r1, e1 = s.ExecDetailed(ctx, "sleep 120ms", 4096)
	}()
	time.Sleep(20 * time.Millisecond)
	go func() {
		defer wg.Done()
		r2, e2 = s.ExecDetailed(ctx, "parallel-cmd", 4096)
	}()
	wg.Wait()
	if e1 != nil || e2 != nil {
		t.Fatalf("concurrent exec failed: e1=%v e2=%v", e1, e2)
	}
	if r1.Stdout != "worker:sleep 120ms" || r2.Stdout != "oneshot:parallel-cmd" {
		t.Fatalf("unexpected concurrent exec outputs: r1=%q r2=%q", r1.Stdout, r2.Stdout)
	}
}

func TestConnectOnReadyFiresBeforeSlowPTY(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	hostKey, _ := signer(t)
	// Simulate 250ms PTY allocation RTT while exec is fast.
	srv := startPerfSSH(t, hostKey, 250*time.Millisecond, 0, 0)
	port := srv.listener.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var readyAt time.Time
	readyCh := make(chan *Session, 1)
	var echoed bytesBufferSync

	cfg := Config{Host: "127.0.0.1", Port: port, User: "test", Password: "unit-password", Auth: "password", Cols: 80, Rows: 24}
	doneConnect := make(chan struct {
		s   *Session
		err error
	}, 1)
	go func() {
		s, err := Connect(ctx, cfg, Callbacks{
			Verify: func(_, _, _ string) bool { return true },
			Output: func(b []byte) { echoed.Write(b) },
			OnReady: func(ready *Session) {
				readyAt = time.Now()
				readyCh <- ready
			},
		})
		doneConnect <- struct {
			s   *Session
			err error
		}{s, err}
	}()

	var early *Session
	select {
	case early = <-readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("OnReady did not fire")
	}

	// Execute a command immediately on OnReady BEFORE PTY setup finishes!
	res, err := early.ExecDetailed(ctx, "early-exec", 4096)
	if err != nil || res.Stdout != "worker:early-exec" {
		t.Fatalf("early ExecDetailed failed: res=%+v err=%v", res, err)
	}

	// Write called before PTY finishes must wait cleanly for ptyReady rather than returning net.ErrClosed.
	writeErrCh := make(chan error, 1)
	go func() {
		writeErrCh <- early.Write([]byte("pty-ok"))
	}()

	finished := <-doneConnect
	connectDoneAt := time.Now()
	if finished.err != nil {
		t.Fatalf("Connect failed: %v", finished.err)
	}
	defer finished.s.Close()

	if diff := connectDoneAt.Sub(readyAt); diff < 120*time.Millisecond {
		t.Fatalf("expected OnReady to fire well before slow PTY finished, got diff=%v", diff)
	}
	if err := <-writeErrCh; err != nil {
		t.Fatalf("early Write did not wait cleanly for PTY readiness: %v", err)
	}
}

type bytesBufferSync struct {
	mu  sync.Mutex
	buf []byte
}

func (b *bytesBufferSync) Write(p []byte) {
	b.mu.Lock()
	b.buf = append(b.buf, p...)
	b.mu.Unlock()
}

func TestPreferredSSHConfigAndKnownHostsFastPath(t *testing.T) {
	cfg := preferredSSHConfig()
	if len(cfg.Ciphers) < 6 || cfg.Ciphers[0] != ssh.CipherAES128GCM || cfg.Ciphers[1] != ssh.CipherChaCha20Poly1305 {
		t.Fatalf("unexpected cipher preference order: %v", cfg.Ciphers)
	}
	if len(cfg.KeyExchanges) < 7 || cfg.KeyExchanges[0] != ssh.KeyExchangeMLKEM768X25519 || cfg.KeyExchanges[1] != ssh.KeyExchangeCurve25519 {
		t.Fatalf("unexpected KEX preference order: %v", cfg.KeyExchanges)
	}

	t.Setenv("LOCALAPPDATA", t.TempDir())
	host1, _ := signer(t)
	host2, _ := signer(t)
	srv := startPerfSSH(t, host1, 0, 0, 0)
	port := srv.listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var prompts atomic.Int32
	cb := Callbacks{Verify: func(_, _, _ string) bool {
		prompts.Add(1)
		return true
	}}
	connCfg := Config{Host: "127.0.0.1", Port: port, User: "test", Password: "unit-password", Auth: "password"}

	// First connect pins key (1 prompt), subsequent connects hit cached fast path (0 prompts).
	for i := 0; i < 3; i++ {
		s, err := ConnectHeadless(ctx, connCfg, cb)
		if err != nil {
			t.Fatalf("connect #%d failed: %v", i+1, err)
		}
		s.Close()
	}
	if prompts.Load() != 1 {
		t.Fatalf("expected exactly 1 host-key prompt across 3 connects, got %d", prompts.Load())
	}

	// Replace server host key -> must still reject even with warm known_hosts cache!
	nextSC := &ssh.ServerConfig{PasswordCallback: srv.server.PasswordCallback}
	nextSC.AddHostKey(host2)
	srv.server = nextSC
	if _, err := ConnectHeadless(ctx, connCfg, cb); err == nil || !strings.Contains(err.Error(), "指纹已变更") {
		t.Fatalf("expected changed host key rejection after cache warm, got %v", err)
	}
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

func TestSessionIsPromptIdle(t *testing.T) {
	// Nil, closed, and headless (no PTY stdin) sessions must never report prompt idle.
	var nilSess *Session
	if nilSess.IsPromptIdle() {
		t.Fatal("nil session reported IsPromptIdle=true")
	}
	headless := &Session{}
	headless.appendRing([]byte("root@host:~# "))
	if headless.IsPromptIdle() {
		t.Fatal("headless session (input==nil) reported IsPromptIdle=true")
	}

	makePTYSession := func(chunks ...string) *Session {
		s := &Session{input: nopWriteCloser{}}
		for _, c := range chunks {
			s.appendRing([]byte(c))
		}
		return s
	}

	// Empty ring buffer -> not idle yet.
	if makePTYSession().IsPromptIdle() {
		t.Fatal("empty ring buffer reported IsPromptIdle=true")
	}

	// Standard idle shell prompts (including ANSI colors, OSC titles, and \r line overwrites).
	idleCases := []struct {
		name   string
		chunks []string
	}{
		{"root bash ANSI", []string{"Last login: Fri Nov 27\r\n\x1b]0;root@ip-172-26-1-186: ~\x07\x1b[01;32mroot@ip-172-26-1-186\x1b[00m:\x1b[01;34m~\x1b[00m# \x1b[?2004h"}},
		{"user bash dollar", []string{"ubuntu@web:~$ "}},
		{"zsh percent", []string{"macbook% "}},
		{"starship chevron", []string{"~/workspace main\r\n\x1b[32m❯\x1b[0m "}},
		{"arrow prompt", []string{"~ ➜ "}},
		{"angle prompt", []string{"PS C:\\Users\\ops> "}},
		{"trimmed hash", []string{"root@srv:~#"}},
		{"trimmed dollar", []string{"deploy@srv:~$"}},
		{"cr overwrite prompt", []string{"downloading 100%\rdeploy@srv:~$ "}},
	}
	for _, tc := range idleCases {
		if !makePTYSession(tc.chunks...).IsPromptIdle() {
			t.Fatalf("case %q expected IsPromptIdle=true", tc.name)
		}
	}

	// Busy / non-prompt / half-typed / password / long lines -> must be false.
	busyCases := []struct {
		name   string
		chunks []string
	}{
		{"half typed command", []string{"root@srv:~# ls -la"}},
		{"sudo password prompt", []string{"[sudo] password for ubuntu: "}},
		{"running log output", []string{"2026-11-27 12:00:00 INFO server started\r\n"}},
		{"overly long line", []string{strings.Repeat("x", 165) + "$ "}},
		{"inside alt screen vim", []string{"root@srv:~# vim\r\n\x1b[?1049h~ \r\n# comment\r\n$ "}},
		{"split alt screen sequence", []string{"root@srv:~# htop\r\n\x1b[?1049", "h$ "}},
		{"alt screen rolled past 64KiB", []string{"\x1b[?1049h", strings.Repeat("a", ringBufferLimit+1024), "\r\n$ "}},
	}
	for _, tc := range busyCases {
		if makePTYSession(tc.chunks...).IsPromptIdle() {
			t.Fatalf("case %q expected IsPromptIdle=false", tc.name)
		}
	}

	// Exiting alt screen back to shell prompt restores IsPromptIdle=true.
	exited := makePTYSession("\x1b[?1049hvim buffer\x1b[?1049l\r\nroot@srv:~# ")
	if !exited.IsPromptIdle() {
		t.Fatal("expected IsPromptIdle=true after exiting alt screen back to prompt")
	}

	// Closed session returns false even if ring buffer ended with a prompt.
	exited.closed.Store(true)
	if exited.IsPromptIdle() {
		t.Fatal("closed session reported IsPromptIdle=true")
	}
}

func TestConnectStatusCallbacks(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	bastionKey, _ := signer(t)
	targetKey, _ := signer(t)

	bastion := startPerfSSH(t, bastionKey, 0, 0, 0)
	target := startPerfSSH(t, targetKey, 0, 0, 0)
	bPort := bastion.listener.Addr().(*net.TCPAddr).Port
	tPort := target.listener.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	type statusEvent struct {
		stage   string
		message string
	}

	// 1. Direct Connect emits dial -> auth -> shell and never leaks password.
	var directEvents []statusEvent
	var mu sync.Mutex
	s1, err := Connect(ctx, Config{
		Host:     "127.0.0.1",
		Port:     tPort,
		User:     "test",
		Password: "unit-password",
		Auth:     "password",
		Cols:     80,
		Rows:     24,
	}, Callbacks{
		Verify: func(_, _, _ string) bool { return true },
		Status: func(stage, message string) {
			mu.Lock()
			directEvents = append(directEvents, statusEvent{stage: stage, message: message})
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("direct Connect failed: %v", err)
	}
	s1.Close()

	wantDirect := []string{"dial", "auth", "shell"}
	if len(directEvents) != len(wantDirect) {
		t.Fatalf("expected %d direct status events, got %d: %+v", len(wantDirect), len(directEvents), directEvents)
	}
	for i, want := range wantDirect {
		if directEvents[i].stage != want {
			t.Fatalf("direct event[%d] stage = %q, want %q (%+v)", i, directEvents[i].stage, want, directEvents)
		}
		if strings.Contains(directEvents[i].message, "unit-password") {
			t.Fatalf("status event leaked secret: %q", directEvents[i].message)
		}
	}
	if !strings.Contains(directEvents[0].message, fmt.Sprintf("127.0.0.1:%d", tPort)) {
		t.Fatalf("expected direct dial message to contain target address, got %q", directEvents[0].message)
	}

	// 2. ProxyJump Connect emits jump -> dial -> auth -> shell cleanly without jump hop duplicates.
	var jumpEvents []statusEvent
	jumpCfg := Config{Host: "127.0.0.1", Port: bPort, User: "test", Password: "unit-password", Auth: "password"}
	s2, err := Connect(ctx, Config{
		Host:     "127.0.0.1",
		Port:     tPort,
		User:     "test",
		Password: "unit-password",
		Auth:     "password",
		Cols:     80,
		Rows:     24,
		Jump:     &jumpCfg,
	}, Callbacks{
		Verify: func(_, _, _ string) bool { return true },
		Status: func(stage, message string) {
			mu.Lock()
			jumpEvents = append(jumpEvents, statusEvent{stage: stage, message: message})
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("jumped Connect failed: %v", err)
	}
	s2.Close()

	wantJump := []string{"jump", "dial", "auth", "shell"}
	if len(jumpEvents) != len(wantJump) {
		t.Fatalf("expected %d jump status events, got %d: %+v", len(wantJump), len(jumpEvents), jumpEvents)
	}
	for i, want := range wantJump {
		if jumpEvents[i].stage != want {
			t.Fatalf("jump event[%d] stage = %q, want %q (%+v)", i, jumpEvents[i].stage, want, jumpEvents)
		}
		if strings.Contains(jumpEvents[i].message, "unit-password") {
			t.Fatalf("jump status event leaked secret: %q", jumpEvents[i].message)
		}
	}
	if jumpEvents[1].message != "正在经跳板机连接目标主机..." {
		t.Fatalf("unexpected jump dial message: %q", jumpEvents[1].message)
	}

	// 3. Auth failure emits dial -> auth and stops before shell.
	var failEvents []statusEvent
	_, err = Connect(ctx, Config{
		Host:     "127.0.0.1",
		Port:     tPort,
		User:     "test",
		Password: "wrong-secret-password",
		Auth:     "password",
		Cols:     80,
		Rows:     24,
	}, Callbacks{
		Verify: func(_, _, _ string) bool { return true },
		Status: func(stage, message string) {
			mu.Lock()
			failEvents = append(failEvents, statusEvent{stage: stage, message: message})
			mu.Unlock()
		},
	})
	if err == nil {
		t.Fatal("expected auth failure with wrong password")
	}
	if len(failEvents) != 2 || failEvents[0].stage != "dial" || failEvents[1].stage != "auth" {
		t.Fatalf("expected [dial auth] before auth failure, got %+v", failEvents)
	}
	for _, ev := range failEvents {
		if strings.Contains(ev.message, "wrong-secret-password") {
			t.Fatalf("status event leaked wrong password: %q", ev.message)
		}
	}
}
