// Package remote owns the SSH transport shared by a terminal, SFTP file browser, port tunnels, and server telemetry.
package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type Config struct {
	Host, User, Password, KeyPath, Passphrase, Auth string
	Port, Cols, Rows                                int
	KeepAliveSec                                    int
	ForwardAgent                                    bool
	StartupCommand                                  string
	Proxy                                           ProxyConfig
	Jump                                            *Config
}

type Callbacks struct {
	Verify    func(address, algorithm, fingerprint string) bool
	Challenge func(user, instruction string, questions []string, echo []bool) ([]string, error)
	Output    func([]byte)
	End       func(error)
	Latency   func(ms int64)
}

// ExecResult contains structured output from a multiplexed SSH exec command.
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
}

const ringBufferLimit = 64 << 10

type Session struct {
	client       *ssh.Client
	jumpClients  []*ssh.Client
	pty          *ssh.Session
	input        io.WriteCloser
	stop         func() bool
	stopKeep     context.CancelFunc
	agentCleanup func()
	closed       atomic.Bool
	write        sync.Mutex
	fileMu       sync.Mutex
	files        *sftp.Client
	tunnelMu         sync.Mutex
	tunnels          map[string]*activeTunnel
	latencyMs        atomic.Int64
	ringMu           sync.Mutex
	ringBuf          []byte
	statsMu          sync.Mutex
	prevCPU          [7]float64
	hasPrevCPU       bool
	staticHost       string
	staticOS         string
	staticKernel     string
	staticCores      int
	execWorkerMu     sync.Mutex
	execWorker       *sshExecWorker
	noPersistentExec atomic.Bool
}

type outputWriter struct {
	mu      sync.Mutex
	output  func([]byte)
	capture func([]byte)
}

func (w *outputWriter) Write(b []byte) (int, error) {
	if len(b) > 0 {
		cp := append([]byte(nil), b...)
		w.mu.Lock()
		if w.capture != nil {
			w.capture(cp)
		}
		if w.output != nil {
			w.output(cp)
		}
		w.mu.Unlock()
	}
	return len(b), nil
}

func (s *Session) appendRing(b []byte) {
	s.ringMu.Lock()
	defer s.ringMu.Unlock()
	s.ringBuf = append(s.ringBuf, b...)
	if len(s.ringBuf) > ringBufferLimit {
		s.ringBuf = append([]byte(nil), s.ringBuf[len(s.ringBuf)-ringBufferLimit:]...)
	}
}

var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// RecentOutput returns the last `maxLines` lines of cleaned terminal output from the session ring buffer.
func (s *Session) RecentOutput(maxLines int) string {
	if maxLines <= 0 || maxLines > 500 {
		maxLines = 120
	}
	s.ringMu.Lock()
	raw := string(s.ringBuf)
	s.ringMu.Unlock()

	clean := ansiEscapeRe.ReplaceAllString(raw, "")
	clean = strings.ReplaceAll(clean, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "")
	lines := strings.Split(clean, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func hostAddress(host string, port int) (string, error) {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && strings.Contains(host, ":") {
		host = host[1 : len(host)-1]
	}
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return "", errors.New("SSH 端口必须在 1 到 65535 之间")
	}
	nameChar := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		for _, r := range ip.Zone() {
			if !nameChar(r) {
				return "", errors.New("无效的 IPv6 接口名")
			}
		}
		host = ip.String()
	} else {
		if host == "" || len(host) > 253 {
			return "", errors.New("请输入有效的主机名或 IP 地址")
		}
		for _, r := range host {
			if !nameChar(r) {
				return "", errors.New("请输入主机名或 IP 地址，端口应填入独立字段")
			}
		}
		host = strings.ToLower(host)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func authMode(cfg Config) string {
	auth := strings.ToLower(strings.TrimSpace(cfg.Auth))
	if auth == "" {
		auth = "password"
		if cfg.KeyPath != "" {
			auth = "key"
		}
	}
	return auth
}

func authMethod(ctx context.Context, cfg Config) (ssh.AuthMethod, func(), error) {
	switch authMode(cfg) {
	case "password":
		return ssh.Password(cfg.Password), func() {}, nil
	case "key", "privatekey", "private-key":
		signer, err := readKey(cfg.KeyPath, cfg.Passphrase)
		if err != nil {
			return nil, nil, err
		}
		return ssh.PublicKeys(signer), func() {}, nil
	case "agent":
		return agentMethod(ctx)
	default:
		return nil, nil, errors.New("不支持的 SSH 认证方式")
	}
}

func loginMethods(ctx context.Context, cfg Config, ask ssh.KeyboardInteractiveChallenge) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	release := func() {}
	if ask == nil || cfg.Password != "" || authMode(cfg) != "password" {
		method, closeAuth, err := authMethod(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		methods, release = append(methods, method), closeAuth
	}
	if ask != nil {
		var autoUsed atomic.Bool
		methods = append(methods, ssh.KeyboardInteractive(func(name, text string, questions []string, echo []bool) ([]string, error) {
			if len(questions) != len(echo) || len(questions) > 64 {
				return nil, errors.New("服务器返回了无效的交互认证问题")
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Auto-satisfy single non-echo PAM password prompt once when the user already supplied a password.
			if cfg.Password != "" && len(questions) == 1 && !echo[0] && !autoUsed.Load() {
				qLower := strings.ToLower(questions[0])
				if strings.Contains(qLower, "pass") && !strings.Contains(qLower, "code") && !strings.Contains(qLower, "otp") && !strings.Contains(qLower, "token") {
					autoUsed.Store(true)
					return []string{cfg.Password}, nil
				}
			}
			type reply struct {
				answers []string
				err     error
			}
			choice := make(chan reply, 1)
			go func() {
				answers, err := ask(name, text, questions, echo)
				choice <- reply{answers, err}
			}()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case answer := <-choice:
				if answer.err != nil {
					return nil, answer.err
				}
				if len(answer.answers) != len(questions) {
					return nil, errors.New("交互认证答复数量与问题不符")
				}
				return answer.answers, nil
			}
		}))
	}
	return methods, release, nil
}

func readKey(path, passphrase string) (ssh.Signer, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 SSH 私钥失败：%w", err)
	}
	defer file.Close()
	const limit = 4 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	defer clear(data)
	if len(data) > limit {
		return nil, errors.New("SSH 私钥超过 4 MiB")
	}
	signer, err := ssh.ParsePrivateKey(data)
	var locked *ssh.PassphraseMissingError
	if errors.As(err, &locked) {
		if passphrase == "" {
			return nil, errors.New("此私钥需要口令")
		}
		secret := []byte(passphrase)
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, secret)
		clear(secret)
	}
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 私钥失败，请检查格式和口令：%w", err)
	}
	return signer, nil
}

func dialSSHClient(ctx, setup context.Context, cfg Config, cb Callbacks, parent *ssh.Client, depth int) (*ssh.Client, []*ssh.Client, net.Conn, func() bool, error) {
	if depth > 5 {
		return nil, nil, nil, nil, errors.New("跳板机链路过深（最多 5 级）")
	}
	address, err := hostAddress(cfg.Host, cfg.Port)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	user := strings.TrimSpace(cfg.User)
	if user == "" || strings.ContainsFunc(user, unicode.IsControl) {
		return nil, nil, nil, nil, errors.New("请填写有效的 SSH 用户名")
	}
	var jumps []*ssh.Client
	if cfg.Jump != nil {
		jClient, deeper, _, _, err := dialSSHClient(ctx, setup, *cfg.Jump, cb, nil, depth+1)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("连接跳板机 %s 失败：%w", cfg.Jump.Host, err)
		}
		jumps = append(deeper, jClient)
		parent = jClient
	}
	closeJumps := func() {
		for i := len(jumps) - 1; i >= 0; i-- {
			jumps[i].Close()
		}
	}
	methods, release, err := loginMethods(setup, cfg, cb.Challenge)
	if err != nil {
		closeJumps()
		return nil, nil, nil, nil, err
	}
	defer release()
	callback, err := hostVerifier(ctx, cb.Verify)
	if err != nil {
		closeJumps()
		return nil, nil, nil, nil, err
	}

	var raw net.Conn
	if parent != nil {
		type dialRes struct {
			conn net.Conn
			err  error
		}
		ch := make(chan dialRes, 1)
		go func() {
			c, err := parent.Dial("tcp", address)
			ch <- dialRes{c, err}
		}()
		select {
		case <-setup.Done():
			closeJumps()
			return nil, nil, nil, nil, fmt.Errorf("通过跳板机连接 %s 超时：%w", address, setup.Err())
		case r := <-ch:
			raw, err = r.conn, r.err
		}
		if err != nil {
			closeJumps()
			return nil, nil, nil, nil, fmt.Errorf("通过跳板机连接 %s 失败：%w", address, err)
		}
	} else {
		raw, err = dialTCPWithProxy(setup, address, cfg.Proxy)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("连接 %s 失败：%w", address, err)
		}
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	deadline, _ := setup.Deadline()
	_ = raw.SetDeadline(deadline)

	transport, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{
		User:            user,
		Auth:            methods,
		HostKeyCallback: callback,
		Timeout:         15 * time.Second,
	})
	if err != nil {
		stop()
		raw.Close()
		closeJumps()
		return nil, nil, nil, nil, fmt.Errorf("SSH 握手失败：%w", err)
	}
	_ = raw.SetDeadline(time.Time{})
	return ssh.NewClient(transport, channels, requests), jumps, raw, stop, nil
}

// Connect establishes an SSH session (optionally via ProxyJump or SOCKS5/HTTP proxy) and keeps ctx associated until close.
func Connect(ctx context.Context, cfg Config, cb Callbacks) (*Session, error) {
	if cfg.Cols == 0 {
		cfg.Cols = 80
	}
	if cfg.Rows == 0 {
		cfg.Rows = 24
	}
	if !validSize(cfg.Cols, cfg.Rows) {
		return nil, errors.New("终端尺寸无效")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	setup, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	t0 := time.Now()
	client, jumps, _, stop, err := dialSSHClient(ctx, setup, cfg, cb, nil, 0)
	if err != nil {
		return nil, err
	}
	initialRTT := time.Since(t0).Milliseconds()

	cleanupClients := func() {
		stop()
		client.Close()
		for i := len(jumps) - 1; i >= 0; i-- {
			jumps[i].Close()
		}
	}

	pty, err := client.NewSession()
	if err != nil {
		cleanupClients()
		return nil, fmt.Errorf("建立终端通道失败：%w", err)
	}

	var agentCleanup func()
	if cfg.ForwardAgent {
		if cleanup, err := setupAgentForwarding(ctx, client, pty); err == nil {
			agentCleanup = cleanup
		}
	}

	keepCtx, stopKeep := context.WithCancel(ctx)
	s := &Session{
		client:       client,
		jumpClients:  jumps,
		pty:          pty,
		stop:         stop,
		stopKeep:     stopKeep,
		agentCleanup: agentCleanup,
		tunnels:      make(map[string]*activeTunnel),
	}

	out := &outputWriter{
		output:  cb.Output,
		capture: s.appendRing,
	}
	pty.Stdout, pty.Stderr = out, out
	input, err := pty.StdinPipe()
	if err != nil {
		if agentCleanup != nil {
			agentCleanup()
		}
		stopKeep()
		pty.Close()
		cleanupClients()
		return nil, err
	}
	s.input = input

	if err := pty.RequestPty("xterm-256color", cfg.Rows, cfg.Cols, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 38400,
		ssh.TTY_OP_OSPEED: 38400,
	}); err != nil {
		if agentCleanup != nil {
			agentCleanup()
		}
		stopKeep()
		pty.Close()
		cleanupClients()
		return nil, fmt.Errorf("远端拒绝分配交互终端：%w", err)
	}
	if err := pty.Shell(); err != nil {
		if agentCleanup != nil {
			agentCleanup()
		}
		stopKeep()
		pty.Close()
		cleanupClients()
		return nil, fmt.Errorf("启动远端 shell 失败：%w", err)
	}

	s.latencyMs.Store(initialRTT)
	if cb.Latency != nil {
		cb.Latency(initialRTT)
	}

	if cmd := strings.TrimSpace(cfg.StartupCommand); cmd != "" {
		go func() {
			time.Sleep(180 * time.Millisecond)
			if !s.closed.Load() {
				_ = s.Write([]byte(cmd + "\n"))
			}
		}()
	}

	if cfg.KeepAliveSec > 0 {
		go s.keepAliveLoop(keepCtx, time.Duration(cfg.KeepAliveSec)*time.Second, cb.Latency)
	}

	go func() {
		err := pty.Wait()
		explicit := s.closed.Load()
		s.Close()
		if cb.End != nil {
			if explicit {
				err = nil
			} else if ctx.Err() != nil {
				err = ctx.Err()
			}
			cb.End(err)
		}
	}()
	return s, nil
}

func (s *Session) keepAliveLoop(ctx context.Context, interval time.Duration, onLatency func(int64)) {
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.closed.Load() {
				return
			}
			start := time.Now()
			done := make(chan error, 1)
			go func() {
				_, _, err := s.client.SendRequest("keepalive@openssh.com", true, nil)
				done <- err
			}()
			select {
			case <-ctx.Done():
				return
			case err := <-done:
				if err != nil {
					missed++
					if missed >= 3 {
						s.Close()
						return
					}
				} else {
					missed = 0
					ms := time.Since(start).Milliseconds()
					if ms < 1 {
						ms = 1
					}
					s.latencyMs.Store(ms)
					if onLatency != nil {
						onLatency(ms)
					}
				}
			case <-time.After(10 * time.Second):
				missed++
				if missed >= 2 {
					s.Close()
					return
				}
			}
		}
	}
}

func (s *Session) LatencyMs() int64 {
	return s.latencyMs.Load()
}

// Exec runs a short non-interactive command over a separate multiplexed channel on the same SSH connection.
func (s *Session) Exec(ctx context.Context, command string, maxBytes int64) (string, error) {
	res, err := s.ExecDetailed(ctx, command, maxBytes)
	return res.Stdout, err
}

// ExecDetailed runs a non-interactive command and returns separated stdout, stderr, exit code, and timing.
// It uses a persistent multiplexed `sh -s` worker channel for 1-RTT execution when idle,
// and falls back seamlessly to a fresh SSH channel for concurrent calls or non-POSIX remotes.
func (s *Session) ExecDetailed(ctx context.Context, command string, maxBytes int64) (ExecResult, error) {
	if s.closed.Load() {
		return ExecResult{}, net.ErrClosed
	}
	if !s.noPersistentExec.Load() && s.execWorkerMu.TryLock() {
		defer s.execWorkerMu.Unlock()
		if s.execWorker == nil || s.execWorker.closed {
			w, err := startExecWorker(s.client)
			if err != nil {
				s.noPersistentExec.Store(true)
				return s.execOneShot(ctx, command, maxBytes)
			}
			s.execWorker = w
		}
		res, err := s.execWorker.run(ctx, command, maxBytes)
		if err == nil {
			return res, nil
		}
		// If the worker died unexpectedly (e.g. restricted/non-POSIX shell) and context wasn't canceled,
		// mark persistent mode unavailable and retry once over standard exec channel.
		if ctx.Err() == nil && !s.closed.Load() {
			s.execWorker = nil
			s.noPersistentExec.Store(true)
			return s.execOneShot(ctx, command, maxBytes)
		}
		return res, err
	}
	return s.execOneShot(ctx, command, maxBytes)
}

// InstallPublicKey appends an OpenSSH public key line to ~/.ssh/authorized_keys safely without duplicates.
func (s *Session) InstallPublicKey(ctx context.Context, pubKey string) error {
	pubKey = strings.TrimSpace(pubKey)
	if pubKey == "" || strings.ContainsAny(pubKey, "\r\n'") {
		return errors.New("公钥格式无效")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubKey)); err != nil {
		return fmt.Errorf("公钥解析失败：%w", err)
	}
	cmd := fmt.Sprintf(
		`mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && (grep -qxF '%s' ~/.ssh/authorized_keys || printf '%%s\n' '%s' >> ~/.ssh/authorized_keys)`,
		pubKey, pubKey,
	)
	execCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := s.Exec(execCtx, cmd, 8192)
	return err
}

func validSize(cols, rows int) bool {
	return cols > 0 && rows > 0 && cols <= 10000 && rows <= 10000
}

func (s *Session) Write(b []byte) error {
	s.write.Lock()
	defer s.write.Unlock()
	if s.closed.Load() || s.input == nil {
		return net.ErrClosed
	}
	n, err := s.input.Write(b)
	if err == nil && n != len(b) {
		return io.ErrShortWrite
	}
	return err
}

func (s *Session) Resize(cols, rows int) error {
	if !validSize(cols, rows) {
		return errors.New("终端尺寸无效")
	}
	if s.closed.Load() || s.pty == nil {
		return net.ErrClosed
	}
	return s.pty.WindowChange(rows, cols)
}

func (s *Session) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	if s.stopKeep != nil {
		s.stopKeep()
	}
	if s.execWorkerMu.TryLock() {
		if s.execWorker != nil {
			s.execWorker.close()
			s.execWorker = nil
		}
		s.execWorkerMu.Unlock()
	}
	s.CloseAllTunnels()
	if s.stop != nil {
		s.stop()
	}
	if s.agentCleanup != nil {
		s.agentCleanup()
	}
	s.client.Close()
	for i := len(s.jumpClients) - 1; i >= 0; i-- {
		s.jumpClients[i].Close()
	}
	s.fileMu.Lock()
	if s.files != nil {
		s.files.Close()
	}
	s.fileMu.Unlock()
}
