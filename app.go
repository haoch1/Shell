package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/haoch1/Shell/internal/agentbridge"
	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/remote"
	"github.com/haoch1/Shell/internal/security"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var appVersion = "1.2.0"

type liveSession struct {
	cancel  context.CancelFunc
	session *remote.Session
	profile local.Profile
}

type App struct {
	ctx              context.Context
	mu               sync.Mutex
	live             map[string]*liveSession
	headless         map[string]*liveSession
	headlessAttempts map[*headlessAttempt]struct{}
	visibleWaiters   map[chan visibleResult]local.Profile
	verify           map[string]chan bool
	challenges       map[string]chan challengeReply
	approvals        map[string]chan string
	sessionApprovals map[string]bool
	transfers        map[string]context.CancelFunc
	bridge           *agentbridge.Server
	ws               *termWSServer
	tray             *systemTray
	quitting         atomic.Bool
	aiCancel         context.CancelFunc
	aiSequence       uint64
	agentCfgMu       sync.RWMutex
	agentCfg         local.AgentConfig
	agentCfgLoaded   bool
	updateMu         sync.Mutex
	updating         bool
}

type challengeReply struct {
	answers []string
	ok      bool
}

func newApp() *App {
	local.SetAIProxyFunc(outboundHTTPProxy)
	a := &App{
		live:             make(map[string]*liveSession),
		headless:         make(map[string]*liveSession),
		headlessAttempts: make(map[*headlessAttempt]struct{}),
		visibleWaiters:   make(map[chan visibleResult]local.Profile),
		verify:           make(map[string]chan bool),
		challenges:       make(map[string]chan challengeReply),
		approvals:        make(map[string]chan string),
		sessionApprovals: make(map[string]bool),
		transfers:        make(map[string]context.CancelFunc),
	}
	a.bridge = agentbridge.New(a)
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	runtime.OnFileDrop(ctx, func(x, y int, paths []string) {
		if len(paths) > 0 {
			runtime.EventsEmit(a.ctx, "shell:filedrop", map[string]any{
				"x":     x,
				"y":     y,
				"paths": paths,
			})
		}
	})
	a.initAgentBridge()
	if ws, err := startTermWSServer(a); err == nil {
		a.ws = ws
	}
	a.tray = startSystemTray(a)
}

func (a *App) TermWSURL(sessionID string) string {
	return a.ws.URLForSession(sessionID)
}

func (a *App) saveWindowBounds(ctx context.Context) {
	if ctx == nil || runtime.WindowIsMinimised(ctx) {
		return
	}
	st, err := local.LoadSettings()
	if err != nil {
		return
	}
	maximised := runtime.WindowIsMaximised(ctx)
	changed := st.WindowMaximised != maximised
	st.WindowMaximised = maximised
	if !maximised {
		w, h := runtime.WindowGetSize(ctx)
		if w >= 880 && w <= 3840 && h >= 560 && h <= 2160 && (w != st.WindowWidth || h != st.WindowHeight) {
			st.WindowWidth = w
			st.WindowHeight = h
			changed = true
		}
	}
	if changed {
		_, _ = local.SaveSettings(st)
	}
}

func (a *App) beforeClose(ctx context.Context) bool {
	a.saveWindowBounds(ctx)
	if a.quitting.Load() || os.Getenv("SHELL_EXIT_ON_CLOSE") == "1" {
		return false
	}
	if st, err := local.LoadSettings(); err == nil && st.CloseToTray {
		runtime.WindowHide(ctx)
		return true
	}
	return false
}

func (a *App) ShowMainWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
	focusShellWindow()
}

func (a *App) QuitApp() {
	a.saveWindowBounds(a.ctx)
	a.quitting.Store(true)
	if a.tray != nil {
		a.tray.Stop()
	}
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
}

func (a *App) ActiveSessionCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.live) + len(a.headless)
}

func (a *App) DisconnectAllSessions() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.live))
	for id := range a.live {
		ids = append(ids, id)
	}
	headlessList := make([]*liveSession, 0, len(a.headless))
	for id, e := range a.headless {
		headlessList = append(headlessList, e)
		delete(a.headless, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.Disconnect(id)
	}
	for _, e := range headlessList {
		if e != nil {
			if e.cancel != nil {
				e.cancel()
			}
			if e.session != nil {
				e.session.Close()
			}
		}
	}
	total := len(ids) + len(headlessList)
	if a.ctx != nil && total > 0 {
		runtime.EventsEmit(a.ctx, "shell:notice", fmt.Sprintf("已断开 %d 个 SSH 会话", total))
	}
}

func (a *App) ToggleMCPFromTray() {
	cfg, err := local.LoadAgentConfig()
	if err != nil {
		return
	}
	cfg.Enabled = !cfg.Enabled
	st, err := a.SaveAgentConfig(cfg)
	if err != nil {
		return
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "agent:status", st)
		if st.Running {
			runtime.EventsEmit(a.ctx, "shell:notice", fmt.Sprintf("MCP 服务已开启 (127.0.0.1:%d)", st.Port))
		} else {
			runtime.EventsEmit(a.ctx, "shell:notice", "MCP 服务已暂停")
		}
	}
}

func (a *App) ToggleCloseToTray() {
	st, err := local.LoadSettings()
	if err != nil {
		return
	}
	st.CloseToTray = !st.CloseToTray
	saved, err := local.SaveSettings(st)
	if err != nil {
		return
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "shell:settings_updated", saved)
	}
}

func (a *App) shutdown(context.Context) {
	if a.tray != nil {
		a.tray.Stop()
	}
	if a.ws != nil {
		a.ws.Stop()
	}
	if a.bridge != nil {
		a.bridge.Stop()
	}
	local.ClearBridgeRuntime()
	a.mu.Lock()
	ids := make([]string, 0, len(a.live))
	for id := range a.live {
		ids = append(ids, id)
	}
	headlessList := make([]*liveSession, 0, len(a.headless))
	for id, e := range a.headless {
		headlessList = append(headlessList, e)
		delete(a.headless, id)
	}
	for tid, cancel := range a.transfers {
		cancel()
		delete(a.transfers, tid)
	}
	if a.aiCancel != nil {
		a.aiCancel()
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.Disconnect(id)
	}
	for _, e := range headlessList {
		if e != nil {
			if e.cancel != nil {
				e.cancel()
			}
			if e.session != nil {
				e.session.Close()
			}
		}
	}
}

type AppInfoView struct {
	Version string `json:"version"`
	DataDir string `json:"dataDir"`
}

func (a *App) AppInfo() AppInfoView {
	root, _ := os.UserCacheDir()
	return AppInfoView{
		Version: appVersion,
		DataDir: filepath.Join(root, "Shell"),
	}
}

func (a *App) OpenDataDir() error {
	root, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "Shell")
	_ = os.MkdirAll(dir, 0700)
	return exec.Command("explorer.exe", dir).Start()
}

// --- Profiles ---

type ProfileView struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Group          string             `json:"group,omitempty"`
	Tags           []string           `json:"tags,omitempty"`
	Color          string             `json:"color,omitempty"`
	Icon           string             `json:"icon,omitempty"`
	Pinned         bool               `json:"pinned,omitempty"`
	Host           string             `json:"host"`
	Port           int                `json:"port"`
	User           string             `json:"user"`
	Auth           string             `json:"auth"`
	KeyPath        string             `json:"keyPath,omitempty"`
	JumpHostID     string             `json:"jumpHostId,omitempty"`
	ProxyType      string             `json:"proxyType,omitempty"`
	ProxyHost      string             `json:"proxyHost,omitempty"`
	ProxyPort      int                `json:"proxyPort,omitempty"`
	ProxyUser      string             `json:"proxyUser,omitempty"`
	ForwardAgent   bool               `json:"forwardAgent,omitempty"`
	KeepAliveSec   int                `json:"keepAliveSec,omitempty"`
	StartupCommand string             `json:"startupCommand,omitempty"`
	Notes          string             `json:"notes,omitempty"`
	AgentAccess    string             `json:"agentAccess,omitempty"`
	Tunnels        []local.TunnelRule `json:"tunnels,omitempty"`
	LastUsedAt     int64              `json:"lastUsedAt,omitempty"`
	Remembered     bool               `json:"remembered"`
}

func view(p local.Profile) (ProfileView, error) {
	saved, err := local.HasSecret(p.ID)
	return ProfileView{
		ID:             p.ID,
		Name:           p.Name,
		Group:          p.Group,
		Tags:           p.Tags,
		Color:          p.Color,
		Icon:           p.Icon,
		Pinned:         p.Pinned,
		Host:           p.Host,
		Port:           p.Port,
		User:           p.User,
		Auth:           p.Auth,
		KeyPath:        p.KeyPath,
		JumpHostID:     p.JumpHostID,
		ProxyType:      p.ProxyType,
		ProxyHost:      p.ProxyHost,
		ProxyPort:      p.ProxyPort,
		ProxyUser:      p.ProxyUser,
		ForwardAgent:   p.ForwardAgent,
		KeepAliveSec:   p.KeepAliveSec,
		StartupCommand: p.StartupCommand,
		Notes:          p.Notes,
		AgentAccess:    p.AgentAccess,
		Tunnels:        p.Tunnels,
		LastUsedAt:     p.LastUsedAt,
		Remembered:     saved,
	}, err
}

func (a *App) Profiles() ([]ProfileView, error) {
	rows, err := local.Profiles()
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, p := range rows {
		ids[i] = p.ID
	}
	savedMap, err := local.SecretIDs(ids)
	if err != nil {
		return nil, err
	}
	result := make([]ProfileView, 0, len(rows))
	for _, p := range rows {
		result = append(result, ProfileView{
			ID:             p.ID,
			Name:           p.Name,
			Group:          p.Group,
			Tags:           p.Tags,
			Color:          p.Color,
			Icon:           p.Icon,
			Pinned:         p.Pinned,
			Host:           p.Host,
			Port:           p.Port,
			User:           p.User,
			Auth:           p.Auth,
			KeyPath:        p.KeyPath,
			JumpHostID:     p.JumpHostID,
			ProxyType:      p.ProxyType,
			ProxyHost:      p.ProxyHost,
			ProxyPort:      p.ProxyPort,
			ProxyUser:      p.ProxyUser,
			ForwardAgent:   p.ForwardAgent,
			KeepAliveSec:   p.KeepAliveSec,
			StartupCommand: p.StartupCommand,
			Notes:          p.Notes,
			AgentAccess:    p.AgentAccess,
			Tunnels:        p.Tunnels,
			LastUsedAt:     p.LastUsedAt,
			Remembered:     savedMap[p.ID],
		})
	}
	return result, nil
}

func (a *App) SaveProfile(p local.Profile) (ProfileView, error) {
	saved, err := local.SaveProfile(p)
	if err != nil {
		return ProfileView{}, err
	}
	return view(saved)
}

func (a *App) GetProfileSecret(id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", nil
	}
	p, ok, err := local.GetProfile(id)
	if err != nil || !ok {
		return "", err
	}
	return local.LoadSecret(p)
}

func (a *App) SaveProfileWithSecret(p local.Profile, secret string, remember bool, keepExisting bool) (ProfileView, error) {
	carriedSecret := secret
	if carriedSecret == "" && keepExisting && remember && p.ID != "" && (p.Auth == "password" || p.Auth == "key") {
		old, ok, err := local.GetProfile(p.ID)
		if err != nil {
			return ProfileView{}, err
		}
		if ok {
			carriedSecret, err = local.LoadSecret(old)
			if err != nil {
				return ProfileView{}, err
			}
		}
	}
	saved, err := local.SaveProfile(p)
	if err != nil {
		return ProfileView{}, err
	}
	if remember && (saved.Auth == "password" || saved.Auth == "key") && carriedSecret != "" {
		err = local.RememberSecret(saved, carriedSecret)
	} else {
		err = local.ForgetProfileSecret(saved)
	}
	if err != nil {
		return ProfileView{}, err
	}
	return view(saved)
}

func (a *App) DuplicateProfile(id string) (ProfileView, error) {
	cloned, err := local.DuplicateProfile(id)
	if err != nil {
		return ProfileView{}, err
	}
	return view(cloned)
}

func (a *App) DeleteProfile(id string) error     { return local.DeleteProfile(id) }
func (a *App) DeleteProfiles(ids []string) error { return local.DeleteProfiles(ids) }
func (a *App) ClearRecentHosts() error           { return local.ClearRecentProfiles() }
func (a *App) ForgetSecret(id string) error      { return local.ForgetSecret(id) }

func (a *App) PickKey() (string, error) {
	home, _ := os.UserHomeDir()
	defaultDir := filepath.Join(home, ".ssh")
	if _, err := os.Stat(defaultDir); err != nil {
		defaultDir = ""
	}
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "选择 SSH 私钥",
		DefaultDirectory: defaultDir,
	})
}

// --- OpenSSH Config Import ---

func (a *App) DiscoverSSHConfig() ([]local.SSHConfigHost, error) {
	return local.DiscoverSSHConfig()
}

func (a *App) ImportSSHConfig(entries []local.SSHConfigHost, group string) ([]ProfileView, error) {
	saved, err := local.ImportSSHConfigEntries(entries, group)
	if err != nil {
		return nil, err
	}
	out := make([]ProfileView, 0, len(saved))
	for _, p := range saved {
		v, err := view(p)
		if err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// --- SSH Connection & Auth ---

func (a *App) ConfirmHost(id string, accept bool) {
	a.mu.Lock()
	ch := a.verify[id]
	a.mu.Unlock()
	if ch != nil {
		select {
		case ch <- accept:
		default:
		}
	}
}

func (a *App) AnswerChallenge(id string, answers []string, accept bool) {
	a.mu.Lock()
	ch := a.challenges[id]
	a.mu.Unlock()
	if ch != nil {
		select {
		case ch <- challengeReply{answers, accept}:
		default:
		}
	}
}

func (a *App) challenge(ctx context.Context, id, host, user, instruction string, questions []string, echo []bool) ([]string, error) {
	if len(questions) > 32 || len(questions) != len(echo) {
		return nil, errors.New("服务器认证提示无效")
	}
	if len(questions) == 0 {
		return []string{}, nil
	}
	ch := make(chan challengeReply, 1)
	a.mu.Lock()
	a.challenges[id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.challenges[id] == ch {
			delete(a.challenges, id)
		}
		a.mu.Unlock()
	}()
	runtime.EventsEmit(a.ctx, "ssh:challenge", map[string]any{
		"id":          id,
		"host":        host,
		"user":        user,
		"instruction": instruction,
		"questions":   questions,
		"echo":        echo,
	})
	select {
	case result := <-ch:
		if result.ok && len(result.answers) == len(questions) {
			return result.answers, nil
		}
		return nil, errors.New("认证已取消")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *App) confirm(ctx context.Context, id, host, algo, fingerprint string) bool {
	ch := make(chan bool, 1)
	a.mu.Lock()
	a.verify[id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.verify[id] == ch {
			delete(a.verify, id)
		}
		a.mu.Unlock()
	}()
	runtime.EventsEmit(a.ctx, "ssh:verify", map[string]string{
		"id":          id,
		"host":        host,
		"algorithm":   algo,
		"fingerprint": fingerprint,
	})
	select {
	case ok := <-ch:
		return ok
	case <-ctx.Done():
		return false
	}
}

func effectiveProxyConfig(p local.Profile, st local.AppSettings) remote.ProxyConfig {
	mode := strings.ToLower(strings.TrimSpace(p.ProxyType))
	switch mode {
	case "direct":
		return remote.ProxyConfig{Type: "direct"}
	case "system":
		return remote.ProxyConfig{Type: "system"}
	case "socks5", "http":
		return remote.ProxyConfig{
			Type: mode,
			Host: p.ProxyHost,
			Port: p.ProxyPort,
			User: p.ProxyUser,
		}
	default:
		// Inherit global proxy routing preference ("direct", "system", "socks5", or "http")
		gMode := strings.ToLower(strings.TrimSpace(st.ProxyMode))
		if gMode == "" || gMode == "direct" {
			return remote.ProxyConfig{Type: "direct"}
		}
		if gMode == "system" {
			return remote.ProxyConfig{Type: "system"}
		}
		if (gMode == "socks5" || gMode == "http") && st.ProxyHost != "" && st.ProxyPort > 0 {
			return remote.ProxyConfig{
				Type: gMode,
				Host: st.ProxyHost,
				Port: st.ProxyPort,
				User: st.ProxyUser,
			}
		}
		return remote.ProxyConfig{Type: "direct"}
	}
}

func (a *App) DetectSystemProxy() remote.SystemProxyInfo {
	return remote.DetectSystemProxy()
}

func resolveJumpChain(jumpID string, settings local.AppSettings, visited map[string]bool, depth int) (*remote.Config, error) {
	if jumpID == "" {
		return nil, nil
	}
	if depth > 5 {
		return nil, errors.New("跳板机链路过深（最多 5 级）")
	}
	if visited[jumpID] {
		return nil, errors.New("检测到跳板机循环引用，请检查主机配置")
	}
	visited[jumpID] = true
	jp, ok, err := local.GetProfile(jumpID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("未找到所配置的跳板机主机资料")
	}
	var secret string
	if jp.Auth == "password" || jp.Auth == "key" {
		secret, err = local.LoadSecret(jp)
		if err != nil {
			return nil, fmt.Errorf("读取跳板机凭据失败：%w", err)
		}
	}
	parent, err := resolveJumpChain(jp.JumpHostID, settings, visited, depth+1)
	if err != nil {
		return nil, err
	}
	rc := &remote.Config{
		Host:    jp.Host,
		Port:    jp.Port,
		User:    jp.User,
		Auth:    jp.Auth,
		KeyPath: jp.KeyPath,
		Proxy:   effectiveProxyConfig(jp, settings),
		Jump:    parent,
	}
	if jp.Auth == "key" {
		rc.Passphrase = secret
	} else if jp.Auth == "password" {
		rc.Password = secret
	}
	return rc, nil
}

func (a *App) Connect(id string, p local.Profile, secret string, remember bool, cols, rows int) (connectErr error) {
	defer func() { a.finishVisibleConnect(p, id, connectErr) }()
	if len(id) < 1 || len(id) > 80 {
		return errors.New("无效的会话标识")
	}
	if secret == "" && remember && p.ID != "" {
		var err error
		if secret, err = local.LoadSecret(p); err != nil {
			return err
		}
	}
	settings, _ := local.LoadSettings()
	keepAlive := p.KeepAliveSec
	if keepAlive == 0 && settings.DefaultKeepAlive > 0 {
		keepAlive = settings.DefaultKeepAlive
	}
	visited := map[string]bool{}
	if p.ID != "" {
		visited[p.ID] = true
	}
	jumpCfg, err := resolveJumpChain(p.JumpHostID, settings, visited, 1)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(a.ctx)
	entry := &liveSession{cancel: cancel, profile: p}
	a.mu.Lock()
	if _, found := a.live[id]; found {
		a.mu.Unlock()
		cancel()
		return errors.New("此会话已打开")
	}
	a.live[id] = entry
	a.mu.Unlock()
	remove := func() {
		a.mu.Lock()
		if a.live[id] == entry {
			delete(a.live, id)
		}
		for k := range a.sessionApprovals {
			if strings.HasPrefix(k, id+"|") {
				delete(a.sessionApprovals, k)
			}
		}
		a.mu.Unlock()
		cancel()
	}

	cfg := remote.Config{
		Host:           p.Host,
		User:           p.User,
		Port:           p.Port,
		Auth:           p.Auth,
		KeyPath:        p.KeyPath,
		Cols:           cols,
		Rows:           rows,
		KeepAliveSec:   keepAlive,
		ForwardAgent:   p.ForwardAgent,
		StartupCommand: p.StartupCommand,
		Proxy:          effectiveProxyConfig(p, settings),
		Jump:           jumpCfg,
	}
	if p.Auth == "key" {
		cfg.Passphrase = secret
	} else if p.Auth == "password" {
		cfg.Password = secret
	}

	s, err := remote.Connect(ctx, cfg, remote.Callbacks{
		Verify: func(host, algorithm, fingerprint string) bool {
			return a.confirm(ctx, id, host, algorithm, fingerprint)
		},
		Challenge: func(user, instruction string, questions []string, echo []bool) ([]string, error) {
			return a.challenge(ctx, id, p.Host, user, instruction, questions, echo)
		},
		Output: func(data []byte) {
			if a.ws != nil && a.ws.SendOutput(id, data) {
				return
			}
			runtime.EventsEmit(a.ctx, "ssh:data", id, base64.StdEncoding.EncodeToString(data))
		},
		Latency: func(ms int64) {
			runtime.EventsEmit(a.ctx, "ssh:latency", id, ms)
		},
		End: func(err error) {
			message := "连接已关闭"
			if err != nil && ctx.Err() == nil {
				message = err.Error()
			}
			remove()
			if a.ws != nil {
				a.ws.CloseSession(id)
			}
			runtime.EventsEmit(a.ctx, "ssh:end", id, message)
		},
	})
	if err != nil {
		remove()
		return err
	}

	a.mu.Lock()
	active := a.live[id] == entry
	if active {
		entry.session = s
	}
	a.mu.Unlock()
	if !active {
		s.Close()
		return context.Canceled
	}
	// Run disk persistence & auto-start tunnels asynchronously so Connect returns immediately and typing is unblocked.
	go func(prof local.Profile, sec string, rem bool, sess *remote.Session) {
		if prof.ID != "" {
			_ = local.TouchProfile(prof.ID)
		}
		if rem && sec != "" && prof.ID != "" && prof.Auth != "agent" {
			if err := local.RememberSecret(prof, sec); err != nil {
				runtime.EventsEmit(a.ctx, "shell:notice", "连接已建立，但未能记住凭据："+err.Error())
			}
		}
		for _, tr := range prof.Tunnels {
			if !tr.AutoStart {
				continue
			}
			_, tErr := sess.StartTunnel(remote.TunnelSpec{
				ID:         tr.ID,
				Name:       tr.Name,
				Mode:       tr.Mode,
				BindHost:   tr.BindHost,
				BindPort:   tr.BindPort,
				TargetHost: tr.TargetHost,
				TargetPort: tr.TargetPort,
			})
			if tErr != nil {
				runtime.EventsEmit(a.ctx, "shell:notice", fmt.Sprintf("自动启动端口转发「%s」失败：%v", tr.Name, tErr))
			}
		}
	}(p, secret, remember, s)
	return nil
}

func (a *App) get(id string) (*remote.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.live[id]; e != nil && e.session != nil {
		return e.session, nil
	}
	return nil, errors.New("SSH 会话未连接")
}

func (a *App) Disconnect(id string) {
	a.mu.Lock()
	e := a.live[id]
	delete(a.live, id)
	var session *remote.Session
	if e != nil {
		session = e.session
	}
	a.mu.Unlock()
	if e != nil {
		e.cancel()
	}
	if session != nil {
		session.Close()
	}
	if a.ws != nil {
		a.ws.CloseSession(id)
	}
}

func (a *App) Input(id, data string) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("单次输入过大")
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	return s.Write(b)
}

func (a *App) Resize(id string, cols, rows int) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.Resize(cols, rows)
}

func (a *App) FetchStats(id string) (remote.ServerStats, error) {
	s, err := a.get(id)
	if err != nil {
		return remote.ServerStats{}, err
	}
	return s.FetchStats(a.ctx)
}

func (a *App) InstallPublicKey(id, pubKey string) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.InstallPublicKey(a.ctx, pubKey)
}

func (a *App) SaveTranscript(defaultName, content string) (string, error) {
	if defaultName == "" {
		defaultName = "shell-session.log"
	}
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "导出终端日志",
		DefaultFilename: defaultName,
		Filters: []runtime.FileFilter{
			{DisplayName: "日志文件 (*.log;*.txt)", Pattern: "*.log;*.txt"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil || dest == "" {
		return "", err
	}
	if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
		return "", err
	}
	return dest, nil
}

// --- SFTP File Operations ---

func (a *App) RealPath(id, dir string) (string, error) {
	s, err := a.get(id)
	if err != nil {
		return "", err
	}
	return s.RealPath(dir)
}

func (a *App) ListFiles(id, dir string) ([]remote.Entry, error) {
	s, err := a.get(id)
	if err != nil {
		return nil, err
	}
	return s.List(dir)
}

func newTransferID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (a *App) registerTransfer() (string, context.Context, func()) {
	tid := newTransferID()
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.transfers[tid] = cancel
	a.mu.Unlock()
	cleanup := func() {
		cancel()
		a.mu.Lock()
		delete(a.transfers, tid)
		a.mu.Unlock()
	}
	return tid, ctx, cleanup
}

func (a *App) CancelTransfer(transferID string) {
	a.mu.Lock()
	cancel := a.transfers[transferID]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) emitTransfer(tid, sessionID, name, direction string, transferred, total int64, status, errMsg string) {
	runtime.EventsEmit(a.ctx, "sftp:progress", map[string]any{
		"transferId":  tid,
		"sessionId":   sessionID,
		"name":        name,
		"direction":   direction,
		"transferred": transferred,
		"total":       total,
		"status":      status,
		"error":       errMsg,
	})
}

func (a *App) PickUploadFiles() ([]string, error) {
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择要上传的文件"})
}

func (a *App) UploadLocalPaths(id, dir string, paths []string, overwrite bool) ([]string, error) {
	s, err := a.get(id)
	if err != nil {
		return nil, err
	}
	uploaded := make([]string, 0, len(paths))
	var firstErr error
	for _, localPath := range paths {
		if strings.TrimSpace(localPath) == "" {
			continue
		}
		info, statErr := os.Stat(localPath)
		if statErr != nil || !info.Mode().IsRegular() {
			if firstErr == nil {
				firstErr = fmt.Errorf("跳过非普通文件：%s", filepath.Base(localPath))
			}
			continue
		}
		leaf := filepath.Base(localPath)
		target := path.Join(dir, leaf)
		tid, ctx, cleanup := a.registerTransfer()
		a.emitTransfer(tid, id, leaf, "upload", 0, info.Size(), "running", "")
		upErr := s.UploadFile(ctx, localPath, target, overwrite, func(done, total int64) {
			a.emitTransfer(tid, id, leaf, "upload", done, total, "running", "")
		})
		cleanup()
		if upErr != nil {
			status := "error"
			if errors.Is(upErr, context.Canceled) {
				status = "canceled"
			}
			a.emitTransfer(tid, id, leaf, "upload", 0, info.Size(), status, upErr.Error())
			if firstErr == nil {
				firstErr = upErr
			}
			continue
		}
		a.emitTransfer(tid, id, leaf, "upload", info.Size(), info.Size(), "done", "")
		uploaded = append(uploaded, target)
	}
	return uploaded, firstErr
}

func (a *App) UploadFile(id, dir string) (string, error) {
	files, err := a.PickUploadFiles()
	if err != nil || len(files) == 0 {
		return "", err
	}
	done, err := a.UploadLocalPaths(id, dir, files, true)
	if err != nil {
		return "", err
	}
	if len(done) == 0 {
		return "", nil
	}
	return done[len(done)-1], nil
}

func (a *App) DownloadFile(id, remotePath string, overwrite bool) (string, error) {
	s, err := a.get(id)
	if err != nil {
		return "", err
	}
	leaf := filepath.Base(path.Base(remotePath))
	if leaf == "." || leaf == ".." || strings.ContainsAny(leaf, `<>:"/\|?*`) {
		return "", errors.New("文件名不适用于 Windows")
	}
	name, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "下载到",
		DefaultFilename: leaf,
	})
	if err != nil || name == "" {
		return "", err
	}
	tid, ctx, cleanup := a.registerTransfer()
	defer cleanup()
	a.emitTransfer(tid, id, leaf, "download", 0, 0, "running", "")
	// SaveFileDialog already asks user for confirmation when overwriting an existing file on Windows.
	err = s.DownloadFile(ctx, remotePath, name, true, func(done, total int64) {
		a.emitTransfer(tid, id, leaf, "download", done, total, "running", "")
	})
	if err != nil {
		status := "error"
		if errors.Is(err, context.Canceled) {
			status = "canceled"
		}
		a.emitTransfer(tid, id, leaf, "download", 0, 0, status, err.Error())
		return "", err
	}
	a.emitTransfer(tid, id, leaf, "download", 1, 1, "done", "")
	return name, nil
}

func (a *App) MakeDir(id, remoteDir string) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.Mkdir(remoteDir)
}

func (a *App) CreateRemoteFile(id, remotePath string) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.CreateEmptyFile(remotePath)
}

func (a *App) RenameRemote(id, oldPath, newPath string) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.Rename(oldPath, newPath)
}

func (a *App) DeleteRemote(id, targetPath string) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.Delete(targetPath)
}

func (a *App) ChmodRemote(id, remotePath string, mode uint32) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.Chmod(remotePath, mode)
}

func (a *App) ReadRemoteText(id, remotePath string) (string, error) {
	s, err := a.get(id)
	if err != nil {
		return "", err
	}
	return s.ReadText(remotePath)
}

func (a *App) WriteRemoteText(id, remotePath, content string) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.WriteText(remotePath, content)
}

// --- Port Forwarding Tunnels ---

func (a *App) StartTunnel(id string, spec remote.TunnelSpec) (remote.TunnelStatus, error) {
	s, err := a.get(id)
	if err != nil {
		return remote.TunnelStatus{}, err
	}
	return s.StartTunnel(spec)
}

func (a *App) StopTunnel(id, tunnelID string) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	s.StopTunnel(tunnelID)
	return nil
}

func (a *App) ListTunnels(id string) ([]remote.TunnelStatus, error) {
	s, err := a.get(id)
	if err != nil {
		return []remote.TunnelStatus{}, nil
	}
	return s.ListTunnels(), nil
}

// --- Snippets, Settings, Known Hosts, SSH Keys, Backup ---

func (a *App) Snippets() ([]local.Snippet, error)                 { return local.Snippets() }
func (a *App) SaveSnippet(s local.Snippet) (local.Snippet, error) { return local.SaveSnippet(s) }
func (a *App) DeleteSnippet(id string) error                      { return local.DeleteSnippet(id) }

func (a *App) GetSettings() (local.AppSettings, error) { return local.LoadSettings() }
func (a *App) SaveSettings(s local.AppSettings) (local.AppSettings, error) {
	return local.SaveSettings(s)
}

func (a *App) KnownHosts() ([]local.KnownHostEntry, error) { return local.ListKnownHosts() }
func (a *App) RemoveKnownHost(line int) error              { return local.RemoveKnownHost(line) }
func (a *App) RemoveKnownHosts(lines []int) error          { return local.RemoveKnownHosts(lines) }
func (a *App) ClearKnownHosts() error                      { return local.ClearKnownHosts() }

func (a *App) LocalKeys() ([]local.LocalKeyInfo, error) { return local.ListLocalKeys() }
func (a *App) GenerateSSHKey(name, algo, passphrase, comment string) (local.LocalKeyInfo, error) {
	return local.GenerateKey(name, algo, passphrase, comment)
}

func (a *App) ExportBackup(includeSecrets bool, passphrase string) (string, error) {
	defaultName := "shell-backup.json"
	if passphrase != "" {
		defaultName = "shell-vault.json"
	}
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "导出配置备份",
		DefaultFilename: defaultName,
		Filters:         []runtime.FileFilter{{DisplayName: "JSON 备份 (*.json)", Pattern: "*.json"}},
	})
	if err != nil || dest == "" {
		return "", err
	}
	if err := local.ExportToFile(dest, includeSecrets, passphrase); err != nil {
		return "", err
	}
	return dest, nil
}

func (a *App) ImportBackup(passphrase string) (local.ImportSummary, error) {
	src, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "选择配置备份文件",
		Filters: []runtime.FileFilter{{DisplayName: "JSON 备份 (*.json)", Pattern: "*.json"}},
	})
	if err != nil || src == "" {
		return local.ImportSummary{}, err
	}
	return local.ImportFromFile(src, passphrase)
}

// --- AI Assistant ---

type AIView struct {
	Mode       string `json:"mode"` // "api" or "cli"
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	HasKey     bool   `json:"hasKey"`
	CLICommand string `json:"cliCommand"`
	CLIArgs    string `json:"cliArgs"`
	CLIModel   string `json:"cliModel"`
	Ready      bool   `json:"ready"`
}

type AIConfigInput struct {
	Mode       string `json:"mode"`
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	APIKey     string `json:"apiKey"`
	KeepKey    bool   `json:"keepKey"`
	CLICommand string `json:"cliCommand"`
	CLIArgs    string `json:"cliArgs"`
	CLIModel   string `json:"cliModel"`
}

func (a *App) AIConfig() (AIView, error) {
	s, err := local.LoadAI()
	mode := "api"
	if s.IsCLI() {
		mode = "cli"
	}
	return AIView{
		Mode:       mode,
		Endpoint:   s.Endpoint,
		Model:      s.Model,
		HasKey:     s.APIKey != "",
		CLICommand: s.CLICommand,
		CLIArgs:    s.CLIArgs,
		CLIModel:   s.CLIModel,
		Ready:      s.Ready(),
	}, err
}

func (a *App) DetectAICLIs() []local.DetectedAICLI {
	return local.DetectAICLIs()
}

func resolveAISettingsInput(in AIConfigInput) (local.AISettings, error) {
	old, loadErr := local.LoadAI()
	if strings.EqualFold(strings.TrimSpace(in.Mode), "cli") {
		// Preserve existing API config fields so switching back to API never loses them.
		out := old
		out.Mode = "cli"
		out.CLICommand = strings.TrimSpace(in.CLICommand)
		out.CLIArgs = strings.TrimSpace(in.CLIArgs)
		out.CLIModel = strings.TrimSpace(in.CLIModel)
		return out, nil
	}
	key := in.APIKey
	if in.KeepKey && key == "" {
		if loadErr != nil {
			return local.AISettings{}, loadErr
		}
		if strings.TrimSpace(in.Endpoint) == old.Endpoint {
			key = old.APIKey
		}
	}
	return local.AISettings{
		Mode:       "api",
		Endpoint:   in.Endpoint,
		Model:      in.Model,
		APIKey:     key,
		CLICommand: old.CLICommand,
		CLIArgs:    old.CLIArgs,
		CLIModel:   old.CLIModel,
	}, nil
}

func (a *App) SaveAIConfig(in AIConfigInput) error {
	s, err := resolveAISettingsInput(in)
	if err != nil {
		return err
	}
	return local.SaveAI(s)
}

func (a *App) TestAIConfig(in AIConfigInput) error {
	s, err := resolveAISettingsInput(in)
	if err != nil {
		return err
	}
	return local.TestAI(a.ctx, s)
}

func (a *App) ConfigureAI(endpoint, model, key string, keep bool) error {
	return a.SaveAIConfig(AIConfigInput{
		Mode:     "api",
		Endpoint: endpoint,
		Model:    model,
		APIKey:   key,
		KeepKey:  keep,
	})
}

func (a *App) TestAI(endpoint, model, key string, keep bool) error {
	return a.TestAIConfig(AIConfigInput{
		Mode:     "api",
		Endpoint: endpoint,
		Model:    model,
		APIKey:   key,
		KeepKey:  keep,
	})
}

func (a *App) CancelAI() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.aiCancel != nil {
		a.aiCancel()
	}
}

func (a *App) beginAI() (context.Context, func()) {
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	if a.aiCancel != nil {
		a.aiCancel()
	}
	a.aiSequence++
	seq := a.aiSequence
	a.aiCancel = cancel
	a.mu.Unlock()
	return ctx, func() {
		cancel()
		a.mu.Lock()
		if seq == a.aiSequence {
			a.aiCancel = nil
		}
		a.mu.Unlock()
	}
}

func (a *App) Suggest(request string) (string, error) {
	request = strings.TrimSpace(request)
	if request == "" {
		return "", fmt.Errorf("请输入任务")
	}
	ctx, done := a.beginAI()
	defer done()
	return local.Suggest(ctx, request)
}

func (a *App) SuggestCommand(request, osHint string) (local.AIResult, error) {
	request = strings.TrimSpace(request)
	if request == "" {
		return local.AIResult{}, fmt.Errorf("请输入任务")
	}
	ctx, done := a.beginAI()
	defer done()
	return local.SuggestStructured(ctx, request, osHint, func(delta string) {
		runtime.EventsEmit(a.ctx, "ai:delta", delta)
	})
}

func (a *App) ExplainTerminal(selectedText, question string) (string, error) {
	ctx, done := a.beginAI()
	defer done()
	if cfg, err := local.LoadAgentConfig(); err == nil {
		red := security.Redact(selectedText, cfg.Redaction)
		selectedText = red.Text
		if red.Count > 0 {
			runtime.EventsEmit(a.ctx, "shell:notice", fmt.Sprintf("发送至 AI 前已自动脱敏 %d 处敏感信息", red.Count))
		}
	}
	return local.ExplainStream(ctx, selectedText, question, func(delta string) {
		runtime.EventsEmit(a.ctx, "ai:delta", delta)
	})
}
