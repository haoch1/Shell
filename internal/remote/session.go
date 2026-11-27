// Package remote owns the SSH transport shared by a terminal, SFTP file browser, port tunnels, and server telemetry.
package remote

import (
	"bytes"
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
	"unicode/utf8"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// JumpDialFunc acquires an authenticated *ssh.Client for a ProxyJump hop.
// If owned is true, the caller owns the client and closes it when the child session closes.
// If owned is false, the client is borrowed (from a live session or jump pool) and must NOT be closed;
// release (if non-nil) is invoked once when the child session finishes using the hop.
type JumpDialFunc func(ctx context.Context, jumpCfg Config, cb Callbacks) (client *ssh.Client, owned bool, release func(), err error)

type Config struct {
	Host, User, Password, KeyPath, Passphrase, Auth string
	Port, Cols, Rows                                int
	KeepAliveSec                                    int
	ForwardAgent                                    bool
	StartupCommand                                  string
	Proxy                                           ProxyConfig
	Jump                                            *Config
	ProfileID                                       string
	ParentClient                                    *ssh.Client
	DialJump                                        JumpDialFunc
	OnJumpDead                                      func(jumpCfg Config, dead *ssh.Client) bool
}

type Callbacks struct {
	Verify    func(address, algorithm, fingerprint string) bool
	Challenge func(user, instruction string, questions []string, echo []bool) ([]string, error)
	Output    func([]byte)
	End       func(error)
	Latency   func(ms int64)
	Status    func(stage, message string)
	// OnReady fires immediately after SSH authentication (before interactive PTY setup finishes)
	// so callers can run multiplexed Exec/SFTP/Tunnels or reuse the transport without waiting for PTY.
	OnReady func(*Session)
}

func (cb Callbacks) emitStatus(stage, message string) {
	if cb.Status != nil {
		cb.Status(stage, message)
	}
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
	client           *ssh.Client
	jumpClients      []*ssh.Client
	releaseJumps     []func()
	ptyMu            sync.Mutex
	pty              *ssh.Session
	ptyReady         chan struct{}
	ptyReadyOnce     sync.Once
	input            io.WriteCloser
	stops            []func() bool
	stopKeep         context.CancelFunc
	agentCleanup     func()
	closed           atomic.Bool
	write            sync.Mutex
	fileMu           sync.Mutex
	files            *sftp.Client
	tunnelMu         sync.Mutex
	tunnels          map[string]*activeTunnel
	latencyMs        atomic.Int64
	ringMu           sync.Mutex
	ringBuf          []byte
	ringPos          int
	ringFull         bool
	inAltScreen      bool
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
	warmMu           sync.Mutex
	warmStarted      bool
	warmDone         chan struct{}
}

type outputWriter struct {
	mu      sync.Mutex
	output  func([]byte)
	capture func([]byte)
}

func (w *outputWriter) Write(b []byte) (int, error) {
	if len(b) > 0 {
		w.mu.Lock()
		if w.capture != nil {
			w.capture(b)
		}
		if w.output != nil {
			cp := append([]byte(nil), b...)
			w.output(cp)
		}
		w.mu.Unlock()
	}
	return len(b), nil
}

func (s *Session) appendRing(b []byte) {
	if len(b) == 0 {
		return
	}
	s.ringMu.Lock()
	defer s.ringMu.Unlock()
	if s.ringBuf == nil {
		s.ringBuf = make([]byte, ringBufferLimit)
	}
	if len(b) >= ringBufferLimit {
		copy(s.ringBuf, b[len(b)-ringBufferLimit:])
		s.ringPos = 0
		s.ringFull = true
		s.updateAltScreenLocked(len(b))
		return
	}
	n := copy(s.ringBuf[s.ringPos:], b)
	if n < len(b) {
		copy(s.ringBuf, b[n:])
		s.ringPos = len(b) - n
		s.ringFull = true
		s.updateAltScreenLocked(len(b))
		return
	}
	s.ringPos += n
	if s.ringPos == ringBufferLimit {
		s.ringPos = 0
		s.ringFull = true
	}
	s.updateAltScreenLocked(len(b))
}

var (
	ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)
	altScreenRe  = regexp.MustCompile(`\x1b\[\?(?:[0-9;]*;)?(?:1049|1047|47)(?:;[0-9;]*)?([hl])`)
)

func (s *Session) ringTailLocked(window int) []byte {
	total := s.ringPos
	if s.ringFull {
		total = ringBufferLimit
	}
	if total == 0 || window <= 0 {
		return nil
	}
	if window > total {
		window = total
	}
	out := make([]byte, window)
	start := s.ringPos - window
	if start >= 0 {
		copy(out, s.ringBuf[start:s.ringPos])
	} else {
		n := copy(out, s.ringBuf[ringBufferLimit+start:])
		copy(out[n:], s.ringBuf[:s.ringPos])
	}
	return out
}

func (s *Session) updateAltScreenLocked(added int) {
	tail := s.ringTailLocked(added + 32)
	if len(tail) == 0 || bytes.IndexByte(tail, 0x1b) < 0 {
		return
	}
	matches := altScreenRe.FindAllSubmatch(tail, -1)
	if len(matches) > 0 {
		last := matches[len(matches)-1][1]
		s.inAltScreen = len(last) == 1 && last[0] == 'h'
	}
}

func (s *Session) snapshotRingLocked() []byte {
	if s.ringFull {
		rawBytes := make([]byte, ringBufferLimit)
		n := copy(rawBytes, s.ringBuf[s.ringPos:])
		copy(rawBytes[n:], s.ringBuf[:s.ringPos])
		return rawBytes
	}
	if s.ringPos > 0 {
		rawBytes := make([]byte, s.ringPos)
		copy(rawBytes, s.ringBuf[:s.ringPos])
		return rawBytes
	}
	return nil
}

// RecentOutput returns the last `maxLines` lines of cleaned terminal output from the session ring buffer.
func (s *Session) RecentOutput(maxLines int) string {
	if maxLines <= 0 || maxLines > 500 {
		maxLines = 120
	}
	s.ringMu.Lock()
	rawBytes := s.snapshotRingLocked()
	s.ringMu.Unlock()

	for len(rawBytes) > 0 && !utf8.RuneStart(rawBytes[0]) {
		rawBytes = rawBytes[1:]
	}
	clean := ansiEscapeRe.ReplaceAllString(string(rawBytes), "")
	clean = strings.ReplaceAll(clean, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "")
	lines := strings.Split(clean, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// IsPromptIdle reports whether the interactive PTY appears to be sitting idle at a shell prompt
// (not closed, has a live PTY stdin, not inside an alternate-screen fullscreen TUI, and the last
// non-empty cleaned line ends like a shell prompt).
func (s *Session) IsPromptIdle() bool {
	if s == nil || s.closed.Load() {
		return false
	}
	s.write.Lock()
	hasInput := s.input != nil
	s.write.Unlock()
	if !hasInput {
		return false
	}

	s.ringMu.Lock()
	inAlt := s.inAltScreen
	rawBytes := s.snapshotRingLocked()
	s.ringMu.Unlock()
	if inAlt || len(rawBytes) == 0 {
		return false
	}

	for len(rawBytes) > 0 && !utf8.RuneStart(rawBytes[0]) {
		rawBytes = rawBytes[1:]
	}
	clean := ansiEscapeRe.ReplaceAllString(string(rawBytes), "")
	clean = strings.ReplaceAll(clean, "\r\n", "\n")

	lines := strings.Split(clean, "\n")
	var lastLine string
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimRight(lines[i], "\r\x00\x07")
		if idx := strings.LastIndexByte(l, '\r'); idx >= 0 {
			l = l[idx+1:]
		}
		if strings.TrimSpace(l) != "" {
			lastLine = l
			break
		}
	}
	if lastLine == "" {
		return false
	}
	trimmed := strings.TrimSpace(lastLine)
	if len(trimmed) == 0 || len(trimmed) >= 160 {
		return false
	}
	for _, suffix := range []string{"# ", "$ ", "% ", "❯ ", "➜ ", "> "} {
		if strings.HasSuffix(lastLine, suffix) {
			return true
		}
	}
	return strings.HasSuffix(trimmed, "#") || strings.HasSuffix(trimmed, "$")
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

// preferredSSHConfig prioritizes fast hardware-accelerated AEAD ciphers and modern KEX
// while retaining full fallback compatibility with older OpenSSH servers.
func preferredSSHConfig() ssh.Config {
	return ssh.Config{
		Ciphers: []string{
			ssh.CipherAES128GCM,
			ssh.CipherChaCha20Poly1305,
			ssh.CipherAES256GCM,
			ssh.CipherAES128CTR,
			ssh.CipherAES192CTR,
			ssh.CipherAES256CTR,
		},
		KeyExchanges: []string{
			ssh.KeyExchangeMLKEM768X25519,
			ssh.KeyExchangeCurve25519,
			"curve25519-sha256@libssh.org",
			ssh.KeyExchangeECDHP256,
			ssh.KeyExchangeECDHP384,
			ssh.KeyExchangeECDHP521,
			ssh.KeyExchangeDH14SHA256,
			ssh.KeyExchangeDH16SHA512,
			ssh.KeyExchangeDHGEXSHA256,
			ssh.InsecureKeyExchangeDH14SHA1,
		},
	}
}

func isOpenChannelError(err error) bool {
	var openErr *ssh.OpenChannelError
	return errors.As(err, &openErr)
}

func dialSSHClient(lifeCtx, setup context.Context, cfg Config, cb Callbacks, parent *ssh.Client, depth int) (client *ssh.Client, ownedJumps []*ssh.Client, releaseJumps []func(), stops []func() bool, err error) {
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

	cleanupAll := func() {
		for _, st := range stops {
			if st != nil {
				st()
			}
		}
		for i := len(ownedJumps) - 1; i >= 0; i-- {
			ownedJumps[i].Close()
		}
		for i := len(releaseJumps) - 1; i >= 0; i-- {
			if releaseJumps[i] != nil {
				releaseJumps[i]()
			}
		}
	}

	var borrowedJump bool
	var borrowedRelease func()

	if parent == nil && cfg.ParentClient != nil {
		parent = cfg.ParentClient
		borrowedJump = true
	} else if parent == nil && cfg.Jump != nil {
		cb.emitStatus("jump", "正在通过跳板机建立隧道...")
		jumpCB := cb
		jumpCB.Status = nil
		if cfg.DialJump != nil {
			jClient, owned, rel, jErr := cfg.DialJump(setup, *cfg.Jump, jumpCB)
			if jErr != nil {
				return nil, nil, nil, nil, fmt.Errorf("连接跳板机 %s 失败：%w", cfg.Jump.Host, jErr)
			}
			parent = jClient
			if owned {
				ownedJumps = append(ownedJumps, jClient)
			} else {
				borrowedJump = true
				borrowedRelease = rel
				if rel != nil {
					releaseJumps = append(releaseJumps, rel)
				}
			}
		} else {
			jClient, deeperOwned, deeperRel, jumpStops, jErr := dialSSHClient(lifeCtx, setup, *cfg.Jump, jumpCB, nil, depth+1)
			if jErr != nil {
				return nil, nil, nil, nil, fmt.Errorf("连接跳板机 %s 失败：%w", cfg.Jump.Host, jErr)
			}
			ownedJumps = append(deeperOwned, jClient)
			releaseJumps = append(releaseJumps, deeperRel...)
			stops = append(stops, jumpStops...)
			parent = jClient
		}
	}

	methods, release, err := loginMethods(setup, cfg, cb.Challenge)
	if err != nil {
		cleanupAll()
		return nil, nil, nil, nil, err
	}
	defer release()
	callback, err := hostVerifier(setup, cb.Verify)
	if err != nil {
		cleanupAll()
		return nil, nil, nil, nil, err
	}

	var raw net.Conn
	if parent != nil {
		cb.emitStatus("dial", "正在经跳板机连接目标主机...")
		raw, err = parent.DialContext(setup, "tcp", address)
		// If a borrowed jump transport died silently (e.g. NAT timeout while idle), evict and retry once.
		if err != nil && borrowedJump && cfg.Jump != nil && setup.Err() == nil && !isOpenChannelError(err) {
			if borrowedRelease != nil {
				borrowedRelease()
				if len(releaseJumps) > 0 {
					releaseJumps = releaseJumps[:len(releaseJumps)-1]
				}
				borrowedRelease = nil
			}
			jumpCB := cb
			jumpCB.Status = nil
			canRetryPool := cfg.OnJumpDead != nil && cfg.OnJumpDead(*cfg.Jump, parent)
			if canRetryPool && cfg.DialJump != nil {
				if jClient, owned, rel, jErr := cfg.DialJump(setup, *cfg.Jump, jumpCB); jErr == nil {
					parent = jClient
					if owned {
						ownedJumps = append(ownedJumps, jClient)
					} else if rel != nil {
						releaseJumps = append(releaseJumps, rel)
					}
					raw, err = parent.DialContext(setup, "tcp", address)
				}
			} else if cfg.ParentClient == nil {
				jumpCopy := *cfg.Jump
				jumpCopy.ParentClient = nil
				if jClient, deeperOwned, deeperRel, jumpStops, jErr := dialSSHClient(lifeCtx, setup, jumpCopy, jumpCB, nil, depth+1); jErr == nil {
					ownedJumps = append(deeperOwned, jClient)
					releaseJumps = append(releaseJumps, deeperRel...)
					stops = append(stops, jumpStops...)
					parent = jClient
					raw, err = parent.DialContext(setup, "tcp", address)
				}
			}
		}
		if err != nil {
			cleanupAll()
			if setup.Err() != nil {
				return nil, nil, nil, nil, fmt.Errorf("通过跳板机连接 %s 超时：%w", address, setup.Err())
			}
			return nil, nil, nil, nil, fmt.Errorf("通过跳板机连接 %s 失败：%w", address, err)
		}
	} else {
		cb.emitStatus("dial", fmt.Sprintf("正在连接 %s...", address))
		raw, err = dialTCPWithProxy(setup, address, cfg.Proxy)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("连接 %s 失败：%w", address, err)
		}
	}

	stopDial := context.AfterFunc(setup, func() { raw.Close() })
	deadline, _ := setup.Deadline()
	_ = raw.SetDeadline(deadline)

	cb.emitStatus("auth", "正在校验主机指纹与身份认证...")
	transport, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{
		Config:          preferredSSHConfig(),
		User:            user,
		Auth:            methods,
		HostKeyCallback: callback,
		Timeout:         20 * time.Second,
	})
	if !stopDial() {
		if err == nil {
			transport.Close()
		} else {
			raw.Close()
		}
		cleanupAll()
		if setup.Err() != nil {
			return nil, nil, nil, nil, fmt.Errorf("SSH 握手失败：%w", setup.Err())
		}
		return nil, nil, nil, nil, fmt.Errorf("SSH 握手失败：%w", err)
	}
	if err != nil {
		raw.Close()
		cleanupAll()
		return nil, nil, nil, nil, fmt.Errorf("SSH 握手失败：%w", err)
	}
	_ = raw.SetDeadline(time.Time{})
	stopLife := context.AfterFunc(lifeCtx, func() { raw.Close() })
	stops = append(stops, stopLife)
	return ssh.NewClient(transport, channels, requests), ownedJumps, releaseJumps, stops, nil
}

// DialClient establishes a standalone authenticated *ssh.Client (with ProxyJump, proxy,
// known_hosts verification, fast crypto, and background keepalive) for jump-host transport pooling.
func DialClient(lifeCtx, dialCtx context.Context, cfg Config, cb Callbacks) (*ssh.Client, func(), error) {
	if err := lifeCtx.Err(); err != nil {
		return nil, nil, err
	}
	if err := dialCtx.Err(); err != nil {
		return nil, nil, err
	}
	setup, cancel := context.WithTimeout(dialCtx, 45*time.Second)
	defer cancel()

	client, ownedJumps, releaseJumps, stops, err := dialSSHClient(lifeCtx, setup, cfg, cb, nil, 0)
	if err != nil {
		return nil, nil, err
	}

	keepCtx, stopKeep := context.WithCancel(lifeCtx)
	var closed atomic.Bool
	var closeOnce sync.Once
	closeFn := func() {
		closeOnce.Do(func() {
			closed.Store(true)
			stopKeep()
			for _, st := range stops {
				if st != nil {
					st()
				}
			}
			client.Close()
			for i := len(ownedJumps) - 1; i >= 0; i-- {
				ownedJumps[i].Close()
			}
			for i := len(releaseJumps) - 1; i >= 0; i-- {
				if releaseJumps[i] != nil {
					releaseJumps[i]()
				}
			}
		})
	}

	keepSec := cfg.KeepAliveSec
	if keepSec <= 0 {
		keepSec = 30
	}
	go runClientKeepAlive(keepCtx, client, time.Duration(keepSec)*time.Second, &closed, closeFn, nil)
	return client, closeFn, nil
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
	client, ownedJumps, releaseJumps, stops, err := dialSSHClient(ctx, setup, cfg, cb, nil, 0)
	if err != nil {
		return nil, err
	}
	initialRTT := time.Since(t0).Milliseconds()

	keepCtx, stopKeep := context.WithCancel(ctx)
	s := &Session{
		client:       client,
		jumpClients:  ownedJumps,
		releaseJumps: releaseJumps,
		ptyReady:     make(chan struct{}),
		stops:        stops,
		stopKeep:     stopKeep,
		tunnels:      make(map[string]*activeTunnel),
	}
	s.latencyMs.Store(initialRTT)
	if cb.Latency != nil {
		cb.Latency(initialRTT)
	}

	// Pre-warm the persistent `sh -s` exec worker concurrently with interactive PTY setup.
	s.startWarmExecWorkerAsync()

	// Expose the authenticated session immediately so MCP/Exec/Jump callers don't wait for PTY RTTs.
	if cb.OnReady != nil {
		cb.OnReady(s)
	}

	cb.emitStatus("shell", "正在打开交互式终端...")
	pty, err := client.NewSession()
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("建立终端通道失败：%w", err)
	}

	var agentCleanup func()
	if cfg.ForwardAgent {
		if cleanup, err := setupAgentForwarding(ctx, client, pty); err == nil {
			agentCleanup = cleanup
		}
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
		pty.Close()
		s.Close()
		return nil, err
	}

	if err := pty.RequestPty("xterm-256color", cfg.Rows, cfg.Cols, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 38400,
		ssh.TTY_OP_OSPEED: 38400,
	}); err != nil {
		if agentCleanup != nil {
			agentCleanup()
		}
		pty.Close()
		s.Close()
		return nil, fmt.Errorf("远端拒绝分配交互终端：%w", err)
	}
	_ = pty.Setenv("COLORTERM", "truecolor")
	if err := pty.Shell(); err != nil {
		if agentCleanup != nil {
			agentCleanup()
		}
		pty.Close()
		s.Close()
		return nil, fmt.Errorf("启动远端 shell 失败：%w", err)
	}

	s.ptyMu.Lock()
	if s.closed.Load() {
		s.ptyMu.Unlock()
		if agentCleanup != nil {
			agentCleanup()
		}
		pty.Close()
		s.markPTYReady()
		return nil, context.Canceled
	}
	s.pty = pty
	s.agentCleanup = agentCleanup
	s.write.Lock()
	s.input = input
	s.write.Unlock()
	s.ptyMu.Unlock()
	s.markPTYReady()

	if cmd := strings.TrimSpace(cfg.StartupCommand); cmd != "" {
		go func() {
			timer := time.NewTimer(180 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-keepCtx.Done():
				return
			case <-timer.C:
				if !s.closed.Load() {
					_ = s.Write([]byte(cmd + "\n"))
				}
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
	runClientKeepAlive(ctx, s.client, interval, &s.closed, s.Close, func(ms int64) {
		s.latencyMs.Store(ms)
		if onLatency != nil {
			onLatency(ms)
		}
	})
}

func runClientKeepAlive(ctx context.Context, client *ssh.Client, interval time.Duration, closed *atomic.Bool, onDead func(), onLatency func(int64)) {
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	probeTimer := time.NewTimer(10 * time.Second)
	if !probeTimer.Stop() {
		select {
		case <-probeTimer.C:
		default:
		}
	}
	defer probeTimer.Stop()
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if closed.Load() {
				return
			}
			start := time.Now()
			done := make(chan error, 1)
			go func() {
				_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
				done <- err
			}()
			probeTimer.Reset(10 * time.Second)
			select {
			case <-ctx.Done():
				return
			case err := <-done:
				if !probeTimer.Stop() {
					select {
					case <-probeTimer.C:
					default:
					}
				}
				if err != nil {
					missed++
					if missed >= 3 {
						onDead()
						return
					}
				} else {
					missed = 0
					ms := time.Since(start).Milliseconds()
					if ms < 1 {
						ms = 1
					}
					if onLatency != nil {
						onLatency(ms)
					}
				}
			case <-probeTimer.C:
				missed++
				if missed >= 2 {
					onDead()
					return
				}
			}
		}
	}
}

// Client returns the underlying live *ssh.Client for jump-host transport reuse, or nil if closed.
func (s *Session) Client() *ssh.Client {
	if s == nil || s.closed.Load() {
		return nil
	}
	return s.client
}

// IsClosed reports whether the session has been closed.
func (s *Session) IsClosed() bool {
	return s == nil || s.closed.Load()
}

func (s *Session) markPTYReady() {
	if s.ptyReady != nil {
		s.ptyReadyOnce.Do(func() { close(s.ptyReady) })
	}
}

func (s *Session) waitPTY() error {
	if s.ptyReady != nil {
		<-s.ptyReady
	}
	if s.closed.Load() {
		return net.ErrClosed
	}
	return nil
}

func (s *Session) startWarmExecWorkerAsync() {
	if s == nil || s.client == nil || s.closed.Load() || s.noPersistentExec.Load() {
		return
	}
	s.warmMu.Lock()
	if s.warmStarted {
		s.warmMu.Unlock()
		return
	}
	s.warmStarted = true
	done := make(chan struct{})
	s.warmDone = done
	s.warmMu.Unlock()
	go s.runWarmExecWorker(done)
}

// WarmExecWorker pre-opens the persistent multiplexed `sh -s` exec channel so the first
// Exec / ExecDetailed call completes in 1 RTT. If warming is already running, it waits for it.
func (s *Session) WarmExecWorker() {
	if s == nil || s.client == nil || s.closed.Load() || s.noPersistentExec.Load() {
		return
	}
	s.warmMu.Lock()
	if s.warmStarted {
		done := s.warmDone
		s.warmMu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	s.warmStarted = true
	done := make(chan struct{})
	s.warmDone = done
	s.warmMu.Unlock()
	s.runWarmExecWorker(done)
}

func (s *Session) runWarmExecWorker(done chan struct{}) {
	defer close(done)
	if s.closed.Load() || s.noPersistentExec.Load() {
		return
	}
	if !s.execWorkerMu.TryLock() {
		return
	}
	defer s.execWorkerMu.Unlock()
	if s.closed.Load() || s.noPersistentExec.Load() || (s.execWorker != nil && !s.execWorker.isClosed()) {
		return
	}
	w, err := startExecWorker(s.client)
	if err != nil {
		if !s.closed.Load() {
			s.noPersistentExec.Store(true)
		}
		return
	}
	if s.closed.Load() {
		w.close()
		return
	}
	s.execWorker = w
}

func (s *Session) waitWarmInFlight(ctx context.Context) {
	s.warmMu.Lock()
	done := s.warmDone
	s.warmMu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
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
// coordinates cleanly with any in-flight WarmExecWorker(), and falls back seamlessly to a
// fresh SSH channel for concurrent calls or non-POSIX remotes.
func (s *Session) ExecDetailed(ctx context.Context, command string, maxBytes int64) (ExecResult, error) {
	if s.closed.Load() {
		return ExecResult{}, net.ErrClosed
	}
	if !s.noPersistentExec.Load() {
		s.waitWarmInFlight(ctx)
		if err := ctx.Err(); err != nil {
			return ExecResult{}, err
		}
		if s.closed.Load() {
			return ExecResult{}, net.ErrClosed
		}
	}
	if !s.noPersistentExec.Load() && s.execWorkerMu.TryLock() {
		defer s.execWorkerMu.Unlock()
		if s.execWorker == nil || s.execWorker.isClosed() {
			w, err := startExecWorker(s.client)
			if err != nil {
				if !s.closed.Load() {
					s.noPersistentExec.Store(true)
				}
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
			s.execWorker.close()
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
	if err := s.waitPTY(); err != nil {
		return err
	}
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
	if err := s.waitPTY(); err != nil {
		return err
	}
	s.ptyMu.Lock()
	pty := s.pty
	s.ptyMu.Unlock()
	if s.closed.Load() || pty == nil {
		return net.ErrClosed
	}
	return pty.WindowChange(rows, cols)
}

func (s *Session) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.markPTYReady()
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
	for _, st := range s.stops {
		if st != nil {
			st()
		}
	}
	s.ptyMu.Lock()
	agentCleanup := s.agentCleanup
	s.agentCleanup = nil
	pty := s.pty
	s.pty = nil
	s.ptyMu.Unlock()
	if agentCleanup != nil {
		agentCleanup()
	}
	if pty != nil {
		pty.Close()
	}
	if s.client != nil {
		s.client.Close()
	}
	for i := len(s.jumpClients) - 1; i >= 0; i-- {
		s.jumpClients[i].Close()
	}
	for i := len(s.releaseJumps) - 1; i >= 0; i-- {
		if s.releaseJumps[i] != nil {
			s.releaseJumps[i]()
		}
	}
	s.fileMu.Lock()
	if s.files != nil {
		s.files.Close()
	}
	s.fileMu.Unlock()
}
