package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/haoch1/Shell/internal/agentbridge"
	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/localpty"
	"github.com/haoch1/Shell/internal/remote"
	"github.com/haoch1/Shell/internal/security"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/ssh"
)

type liveSession struct {
	cancel     context.CancelFunc
	session    *remote.Session
	localPTY   *localpty.Session
	isLocal    bool
	localShell localpty.ShellProfile
	localCwd   string
	profile    local.Profile
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
	uninstalling     atomic.Bool
	aiCancel         context.CancelFunc
	aiSequence       uint64
	agentCfgMu       sync.RWMutex
	agentCfg         local.AgentConfig
	agentCfgLoaded   bool
	jumpMu           sync.Mutex
	jumpPool         map[string]*pooledJump
	jumpDials        map[string]*jumpDialAttempt
	deadJumpClients  map[*ssh.Client]struct{}
	jumpIdleTimeout  time.Duration
	jumpGen          uint64
	cacheMu          sync.RWMutex
	profCache        profileCacheState
	profGen          uint64
	settingsCache    settingsCacheState
	settingsGen      uint64
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
		jumpPool:         make(map[string]*pooledJump),
		jumpDials:        make(map[string]*jumpDialAttempt),
		deadJumpClients:  make(map[*ssh.Client]struct{}),
		jumpIdleTimeout:  defaultJumpIdleTimeout,
	}
	a.bridge = agentbridge.New(a, appVersion)
	return a
}

func (a *App) baseContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func (a *App) clearSessionApprovalsLocked(id string) {
	prefix := id + "|"
	for k := range a.sessionApprovals {
		if strings.HasPrefix(k, prefix) {
			delete(a.sessionApprovals, k)
		}
	}
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
	go func() {
		if err := local.EnsureDefaultAutoStart(); err == nil {
			a.invalidateSettingsCache()
		}
		_ = local.SyncAutoStartPath()
	}()
}

func (a *App) TermWSURL(sessionID string) string {
	return a.ws.URLForSession(sessionID)
}

func (a *App) saveWindowBounds(ctx context.Context) {
	if a.uninstalling.Load() || ctx == nil || runtime.WindowIsMinimised(ctx) {
		return
	}
	maximised := runtime.WindowIsMaximised(ctx)
	var w, h int
	if !maximised {
		w, h = runtime.WindowGetSize(ctx)
	}
	_, _ = local.UpdateSettings(func(st *local.AppSettings) error {
		st.WindowMaximised = maximised
		if w >= 880 && w <= 3840 && h >= 560 && h <= 2160 {
			st.WindowWidth = w
			st.WindowHeight = h
		}
		return nil
	})
	a.invalidateSettingsCache()
}

func (a *App) beforeClose(ctx context.Context) bool {
	if a.uninstalling.Load() {
		return false
	}
	a.saveWindowBounds(ctx)
	if a.quitting.Load() || os.Getenv("SHELL_EXIT_ON_CLOSE") == "1" {
		return false
	}
	if st, err := a.getCachedSettings(); err == nil && st.CloseToTray {
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
	if !a.uninstalling.Load() {
		a.saveWindowBounds(a.ctx)
	}
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
		a.clearSessionApprovalsLocked(id)
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
			if e.localPTY != nil {
				_ = e.localPTY.Close()
			}
		}
	}
	a.closeAllPooledJumps()
	total := len(ids) + len(headlessList)
	if a.ctx != nil && total > 0 {
		runtime.EventsEmit(a.ctx, "shell:notice", fmt.Sprintf("已关闭 %d 个终端会话", total))
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
	saved, err := local.UpdateSettings(func(st *local.AppSettings) error {
		st.CloseToTray = !st.CloseToTray
		return nil
	})
	if err != nil {
		return
	}
	a.invalidateSettingsCache()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "shell:settings_updated", saved)
	}
}

func (a *App) stopRuntimeServices(clearBridge bool) {
	if a.tray != nil {
		a.tray.Stop()
	}
	if a.ws != nil {
		a.ws.Stop()
	}
	if a.bridge != nil {
		a.bridge.Stop()
	}
	if clearBridge {
		local.ClearBridgeRuntime()
	}
	a.mu.Lock()
	ids := make([]string, 0, len(a.live))
	for id := range a.live {
		ids = append(ids, id)
	}
	headlessList := make([]*liveSession, 0, len(a.headless))
	for id, e := range a.headless {
		headlessList = append(headlessList, e)
		delete(a.headless, id)
		a.clearSessionApprovalsLocked(id)
	}
	for tid, cancel := range a.transfers {
		cancel()
		delete(a.transfers, tid)
	}
	if a.aiCancel != nil {
		a.aiCancel()
		a.aiCancel = nil
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
			if e.localPTY != nil {
				_ = e.localPTY.Close()
			}
		}
	}
	a.closeAllPooledJumps()
}

func (a *App) shutdown(context.Context) {
	uninstalling := a.uninstalling.Load()
	a.stopRuntimeServices(!uninstalling)
	cleanupRuntimeLeftovers()
}

func (a *App) GetAutoStart() bool {
	enabled, _ := local.IsAutoStartEnabled()
	return enabled
}

func (a *App) SetAutoStart(enabled bool) (bool, error) {
	if err := local.SetAutoStart(enabled); err != nil {
		cur, _ := local.IsAutoStartEnabled()
		return cur, err
	}
	if _, err := local.UpdateSettings(func(st *local.AppSettings) error {
		st.AutoStartInitialized = true
		return nil
	}); err == nil {
		a.invalidateSettingsCache()
	}
	cur, err := local.IsAutoStartEnabled()
	if err != nil {
		return enabled, nil
	}
	return cur, nil
}

func (a *App) UninstallAndQuit(removeData bool) error {
	if err := cleanAutoStartRegistry(); err != nil {
		return err
	}
	a.uninstalling.Store(true)
	a.quitting.Store(true)
	a.stopRuntimeServices(true)

	if removeData {
		if dataDir, err := local.DataDir(); err == nil && dataDir != "" {
			for _, sub := range []string{"config", "security", "logs", "runtime"} {
				_ = os.RemoveAll(filepath.Join(dataDir, sub))
			}
			_ = launchDataCleanup(os.Getpid(), dataDir)
		}
	}
	if a.ctx != nil {
		go func() {
			time.Sleep(80 * time.Millisecond)
			a.QuitApp()
		}()
	} else {
		a.QuitApp()
	}
	return nil
}

type AppInfoView struct {
	Version string `json:"version"`
	DataDir string `json:"dataDir"`
}

func (a *App) AppInfo() AppInfoView {
	dir, _ := local.DataDir()
	return AppInfoView{
		Version: appVersion,
		DataDir: dir,
	}
}

func (a *App) OpenDataDir() error {
	dir, err := local.DataDir()
	if err != nil {
		return err
	}
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
	rows, err := a.getCachedProfiles()
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
	a.invalidateProfileCache()
	a.retirePooledJumpsByProfile(saved.ID)
	return view(saved)
}

func (a *App) GetProfileSecret(id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", nil
	}
	p, ok, err := a.getCachedProfileByID(id)
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
	a.invalidateProfileCache()
	a.retirePooledJumpsByProfile(saved.ID)
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
	a.invalidateProfileCache()
	return view(cloned)
}

func (a *App) DeleteProfile(id string) error {
	err := local.DeleteProfile(id)
	if err == nil {
		a.invalidateProfileCache()
		a.retirePooledJumpsByProfile(id)
	}
	return err
}

func (a *App) DeleteProfiles(ids []string) error {
	err := local.DeleteProfiles(ids)
	if err == nil {
		a.invalidateProfileCache()
		for _, id := range ids {
			a.retirePooledJumpsByProfile(id)
		}
	}
	return err
}

func (a *App) SetProfilesGroup(ids []string, group string) error {
	err := local.SetProfilesGroup(ids, group)
	if err == nil {
		a.invalidateProfileCache()
	}
	return err
}

func (a *App) ClearRecentHosts() error {
	err := local.ClearRecentProfiles()
	if err == nil {
		a.invalidateProfileCache()
	}
	return err
}

func (a *App) ForgetSecret(id string) error {
	err := local.ForgetSecret(id)
	if err == nil {
		a.retirePooledJumpsByProfile(id)
	}
	return err
}

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
	a.invalidateProfileCache()
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
	return a.confirmHostKey(ctx, id, host, algo, fingerprint, "", false)
}

func (a *App) confirmChanged(ctx context.Context, id, host, algo, newFingerprint, oldFingerprint string) bool {
	return a.confirmHostKey(ctx, id, host, algo, newFingerprint, oldFingerprint, true)
}

func (a *App) confirmHostKey(ctx context.Context, id, host, algo, fingerprint, oldFingerprint string, changed bool) bool {
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
	payload := map[string]any{
		"id":          id,
		"host":        host,
		"algorithm":   algo,
		"fingerprint": fingerprint,
		"changed":     changed,
	}
	if oldFingerprint != "" {
		payload["previousFingerprint"] = oldFingerprint
	}
	runtime.EventsEmit(a.ctx, "ssh:verify", payload)
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

func (a *App) resolveJumpChain(jumpID string, settings local.AppSettings, visited map[string]bool, depth int) (*remote.Config, error) {
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
	jp, ok, err := a.getCachedProfileByID(jumpID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("未找到所配置的跳板机主机资料")
	}
	parent, err := a.resolveJumpChain(jp.JumpHostID, settings, visited, depth+1)
	if err != nil {
		return nil, err
	}
	keepAlive := jp.KeepAliveSec
	if keepAlive == 0 && settings.DefaultKeepAlive > 0 {
		keepAlive = settings.DefaultKeepAlive
	}
	rc := &remote.Config{
		ProfileID:    jp.ID,
		Host:         jp.Host,
		Port:         jp.Port,
		User:         jp.User,
		Auth:         jp.Auth,
		KeyPath:      jp.KeyPath,
		KeepAliveSec: keepAlive,
		Proxy:        effectiveProxyConfig(jp, settings),
		Jump:         parent,
		DialJump:     a.dialJumpClient,
		OnJumpDead:   a.evictDeadJumpClient,
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
	settings, _ := a.getCachedSettings()
	keepAlive := p.KeepAliveSec
	if keepAlive == 0 && settings.DefaultKeepAlive > 0 {
		keepAlive = settings.DefaultKeepAlive
	}
	visited := map[string]bool{}
	if p.ID != "" {
		visited[p.ID] = true
	}
	jumpCfg, err := a.resolveJumpChain(p.JumpHostID, settings, visited, 1)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(a.baseContext())
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
		a.clearSessionApprovalsLocked(id)
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
		ProfileID:      p.ID,
		DialJump:       a.dialJumpClient,
		OnJumpDead:     a.evictDeadJumpClient,
	}
	if p.Auth == "key" {
		cfg.Passphrase = secret
	} else if p.Auth == "password" {
		cfg.Password = secret
	}

	var exited atomic.Bool
	s, err := remote.Connect(ctx, cfg, remote.Callbacks{
		Status: func(stage, message string) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "ssh:status", id, stage, message)
			}
		},
		Verify: func(host, algorithm, fingerprint string) bool {
			return a.confirm(ctx, id, host, algorithm, fingerprint)
		},
		VerifyChanged: func(host, algorithm, newFingerprint, oldFingerprint string) bool {
			return a.confirmChanged(ctx, id, host, algorithm, newFingerprint, oldFingerprint)
		},
		Challenge: func(user, instruction string, questions []string, echo []bool) ([]string, error) {
			return a.challenge(ctx, id, p.Host, user, instruction, questions, echo)
		},
		OnReady: func(ready *remote.Session) {
			a.mu.Lock()
			active := a.live[id] == entry
			if active {
				entry.session = ready
			}
			a.mu.Unlock()
			if active {
				a.finishVisibleConnect(p, id, nil)
			}
		},
		Output: func(data []byte) {
			if a.ws != nil && a.ws.SendOutput(id, data) {
				return
			}
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "ssh:data", id, base64.StdEncoding.EncodeToString(data))
			}
		},
		Latency: func(ms int64) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "ssh:latency", id, ms)
			}
		},
		End: func(err error) {
			exited.Store(true)
			message := "连接已关闭"
			if err != nil && ctx.Err() == nil {
				message = err.Error()
			}
			remove()
			a.ws.FinishSession(id)
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "ssh:end", id, message)
			}
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
		if exited.Load() {
			// End is already draining the terminal; reporting a failed connect would close the UI socket early.
			return nil
		}
		s.Close()
		return context.Canceled
	}
	// Run disk persistence & auto-start tunnels asynchronously so Connect returns immediately and typing is unblocked.
	go func(prof local.Profile, sec string, rem bool, sess *remote.Session) {
		if prof.ID != "" {
			if err := local.TouchProfile(prof.ID); err == nil {
				a.invalidateProfileCache()
			}
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

func (a *App) writeTerminalInput(id string, data []byte) error {
	a.mu.Lock()
	e := a.live[id]
	var lp *localpty.Session
	var rs *remote.Session
	if e != nil {
		lp = e.localPTY
		rs = e.session
	}
	a.mu.Unlock()
	if lp != nil {
		return lp.Write(data)
	}
	if rs != nil {
		return rs.Write(data)
	}
	return errors.New("终端会话未连接")
}

// LocalShells returns all detected local Windows shells and WSL distributions.
func (a *App) LocalShells() []localpty.ShellProfile {
	shells := localpty.DetectShells()
	if shells == nil {
		return []localpty.ShellProfile{}
	}
	return shells
}

// StartLocalTerminal spawns a native Windows ConPTY session for the requested shell (or default)
// and wires its I/O through the same zero-latency binary WebSocket / Wails event channel as SSH.
func (a *App) StartLocalTerminal(id, shellID, cwd string, cols, rows int) (localpty.ShellProfile, error) {
	id = strings.TrimSpace(id)
	if len(id) < 1 || len(id) > 80 {
		return localpty.ShellProfile{}, errors.New("无效的会话标识")
	}

	var (
		shell localpty.ShellProfile
		err   error
	)
	shellID = strings.TrimSpace(shellID)
	if shellID == "" {
		settings, _ := a.getCachedSettings()
		shell, err = localpty.ResolveShellOrDefault(settings.DefaultLocalShell)
	} else {
		shell, err = localpty.ResolveShell(shellID)
	}
	if err != nil {
		return localpty.ShellProfile{}, err
	}

	ctx, cancel := context.WithCancel(a.baseContext())
	entry := &liveSession{
		cancel:     cancel,
		isLocal:    true,
		localShell: shell,
		localCwd:   strings.TrimSpace(cwd),
		profile: local.Profile{
			Name: shell.Name,
			Host: "local",
		},
	}
	a.mu.Lock()
	if _, found := a.live[id]; found {
		a.mu.Unlock()
		cancel()
		return localpty.ShellProfile{}, errors.New("此会话已打开")
	}
	a.live[id] = entry
	a.mu.Unlock()

	remove := func() {
		a.mu.Lock()
		if a.live[id] == entry {
			delete(a.live, id)
		}
		a.clearSessionApprovalsLocked(id)
		a.mu.Unlock()
		cancel()
	}

	var exited atomic.Bool
	sess, err := localpty.Start(localpty.Options{
		Command: shell.Command,
		Args:    shell.Args,
		Cwd:     cwd,
		Cols:    cols,
		Rows:    rows,
		OnOutput: func(data []byte) {
			if a.ws != nil && a.ws.SendOutput(id, data) {
				return
			}
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "ssh:data", id, base64.StdEncoding.EncodeToString(data))
			}
		},
		OnExit: func(exitCode int, exitErr error) {
			exited.Store(true)
			message := "本地终端已退出"
			if exitCode != 0 {
				message = fmt.Sprintf("本地终端已退出（代码 %d）", exitCode)
			} else if exitErr != nil && ctx.Err() == nil {
				message = exitErr.Error()
			}
			remove()
			a.ws.FinishSession(id)
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "ssh:end", id, message)
			}
		},
	})
	if err != nil {
		remove()
		return localpty.ShellProfile{}, err
	}

	a.mu.Lock()
	active := a.live[id] == entry
	if active {
		entry.localPTY = sess
	}
	a.mu.Unlock()
	if !active && !exited.Load() {
		_ = sess.Close()
		return localpty.ShellProfile{}, context.Canceled
	}
	return shell, nil
}

func (a *App) Disconnect(id string) {
	a.mu.Lock()
	e := a.live[id]
	delete(a.live, id)
	var session *remote.Session
	var lp *localpty.Session
	if e != nil {
		session = e.session
		lp = e.localPTY
	}
	a.mu.Unlock()
	if e != nil && e.cancel != nil {
		e.cancel()
	}
	if session != nil {
		session.Close()
	}
	if lp != nil {
		_ = lp.Close()
	}
	if a.ws != nil {
		a.ws.CloseSession(id)
	}
}

func (a *App) Input(id, data string) error {
	if len(data) > 2<<20 {
		return errors.New("单次输入过大")
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	return a.writeTerminalInput(id, b)
}

func (a *App) Resize(id string, cols, rows int) error {
	a.mu.Lock()
	e := a.live[id]
	var lp *localpty.Session
	var rs *remote.Session
	if e != nil {
		lp = e.localPTY
		rs = e.session
	}
	a.mu.Unlock()
	if lp != nil {
		return lp.Resize(cols, rows)
	}
	if rs != nil {
		return rs.Resize(cols, rows)
	}
	return errors.New("终端会话未连接")
}

func (a *App) ProbeSession(id string) (int64, error) {
	s, err := a.get(id)
	if err != nil {
		return 0, err
	}
	return s.Probe(a.baseContext())
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

func (a *App) BrowseDirectory(id, dir string) (remote.DirectoryListing, error) {
	s, err := a.get(id)
	if err != nil {
		return remote.DirectoryListing{}, err
	}
	return s.BrowseDirectory(a.ctx, dir)
}

func (a *App) ResolveFileLink(id, filePath string) (remote.Entry, error) {
	s, err := a.get(id)
	if err != nil {
		return remote.Entry{}, err
	}
	return s.ResolveFileLink(filePath)
}

func newTransferID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (a *App) registerTransfer() (string, context.Context, func()) {
	tid := newTransferID()
	ctx, cancel := context.WithCancel(a.baseContext())
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
		if statErr != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
			if firstErr == nil {
				firstErr = fmt.Errorf("跳过非普通文件：%s", filepath.Base(localPath))
			}
			continue
		}
		leaf := filepath.Base(filepath.Clean(localPath))
		target := path.Join(dir, leaf)
		tid, ctx, cleanup := a.registerTransfer()
		speed := newTransferSpeedTracker()

		if info.IsDir() {
			a.emitTransferRich(tid, id, leaf, "upload", 0, 0, 0, "running", "", localPath, target, "")
			var lastDone, lastTotal int64
			var lastItem string
			lastEmit := time.Time{}
			_, upErr := s.UploadDir(ctx, localPath, target, overwrite, func(done, total int64, currentItem string) {
				lastDone = done
				if total > 0 {
					lastTotal = total
				}
				now := time.Now()
				if currentItem == lastItem && !lastEmit.IsZero() && now.Sub(lastEmit) < 80*time.Millisecond && done < total {
					return
				}
				lastItem = currentItem
				lastEmit = now
				a.emitTransferRich(tid, id, leaf, "upload", done, total, speed.sample(done), "running", "", localPath, target, currentItem)
			})
			cleanup()
			if upErr != nil {
				status := "error"
				if errors.Is(upErr, context.Canceled) {
					status = "canceled"
				}
				a.emitTransferRich(tid, id, leaf, "upload", lastDone, lastTotal, 0, status, upErr.Error(), localPath, target, lastItem)
				if firstErr == nil {
					firstErr = upErr
				}
				continue
			}
			finalTotal := lastTotal
			if finalTotal <= 0 {
				finalTotal = lastDone
			}
			a.emitTransferRich(tid, id, leaf, "upload", finalTotal, finalTotal, speed.final(finalTotal), "done", "", localPath, target, "")
			uploaded = append(uploaded, target)
			continue
		}

		a.emitTransferRich(tid, id, leaf, "upload", 0, info.Size(), 0, "running", "", localPath, target, "")
		var lastDone int64
		upErr := s.UploadFile(ctx, localPath, target, overwrite, func(done, total int64) {
			lastDone = done
			a.emitTransferRich(tid, id, leaf, "upload", done, total, speed.sample(done), "running", "", localPath, target, "")
		})
		cleanup()
		if upErr != nil {
			status := "error"
			if errors.Is(upErr, context.Canceled) {
				status = "canceled"
			}
			a.emitTransferRich(tid, id, leaf, "upload", lastDone, info.Size(), 0, status, upErr.Error(), localPath, target, "")
			if firstErr == nil {
				firstErr = upErr
			}
			continue
		}
		a.emitTransferRich(tid, id, leaf, "upload", info.Size(), info.Size(), speed.final(info.Size()), "done", "", localPath, target, "")
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
	// SaveFileDialog already asks user for confirmation when overwriting an existing file on Windows.
	if err := a.downloadRemoteFileInto(s, id, remotePath, name, leaf); err != nil {
		return "", err
	}
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
	return s.EditText(remotePath, content)
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

func (a *App) GetSettings() (local.AppSettings, error) { return a.getCachedSettings() }
func (a *App) SaveSettings(s local.AppSettings) (local.AppSettings, error) {
	saved, err := local.SaveSettings(s)
	if err == nil {
		a.invalidateSettingsCache()
		a.retirePooledJumpsByProfile("")
	}
	return saved, err
}

func (a *App) UpdatePreferences(changes map[string]any) (local.AppSettings, error) {
	data, err := json.Marshal(changes)
	if err != nil {
		return local.AppSettings{}, err
	}
	saved, err := local.UpdateSettings(func(current *local.AppSettings) error {
		s := *current
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		s.WindowWidth = current.WindowWidth
		s.WindowHeight = current.WindowHeight
		s.WindowMaximised = current.WindowMaximised
		s.AutoStartInitialized = current.AutoStartInitialized
		*current = s
		return nil
	})
	if err == nil {
		a.invalidateSettingsCache()
		if _, p1 := changes["proxyMode"]; p1 {
			a.retirePooledJumpsByProfile("")
		} else if _, p2 := changes["proxyHost"]; p2 {
			a.retirePooledJumpsByProfile("")
		} else if _, p3 := changes["proxyPort"]; p3 {
			a.retirePooledJumpsByProfile("")
		} else if _, p4 := changes["proxyUser"]; p4 {
			a.retirePooledJumpsByProfile("")
		}
	}
	return saved, err
}

func (a *App) KnownHosts() ([]local.KnownHostEntry, error) { return local.ListKnownHosts() }
func (a *App) RemoveKnownHost(line int) error              { return local.RemoveKnownHost(line) }
func (a *App) RemoveKnownHosts(lines []int) error          { return local.RemoveKnownHosts(lines) }
func (a *App) ClearKnownHosts() error                      { return local.ClearKnownHosts() }

func (a *App) LocalKeys() ([]local.LocalKeyInfo, error) { return local.ListLocalKeys() }
func (a *App) GenerateSSHKey(name, algo, passphrase, comment string) (local.LocalKeyInfo, error) {
	return local.GenerateKey(name, algo, passphrase, comment)
}

type HostExportResult struct {
	Path     string   `json:"path"`
	Count    int      `json:"count"`
	Warnings []string `json:"warnings"`
}

func (a *App) ExportHosts(ids []string, format string) (HostExportResult, error) {
	options := runtime.SaveDialogOptions{Title: "导出服务器"}
	switch format {
	case "csv", "csv-sanitized":
		options.DefaultFilename = fmt.Sprintf("Shell-hosts-%s.csv", time.Now().Format("2006-01-02"))
		options.Filters = []runtime.FileFilter{{DisplayName: "CSV 主机列表 (*.csv)", Pattern: "*.csv"}}
	case "openssh":
		options.DefaultFilename = "ssh_config"
		options.Filters = []runtime.FileFilter{{DisplayName: "OpenSSH 配置", Pattern: "*"}}
	case "putty":
		options.DefaultFilename = "Shell-hosts.reg"
		options.Filters = []runtime.FileFilter{{DisplayName: "PuTTY 会话 (*.reg)", Pattern: "*.reg"}}
	default:
		return HostExportResult{}, errors.New("未知的服务器导出格式")
	}
	dest, err := runtime.SaveFileDialog(a.ctx, options)
	if err != nil || dest == "" {
		return HostExportResult{Warnings: []string{}}, err
	}
	count, warnings, err := local.ExportHostFile(dest, ids, format)
	if err != nil {
		return HostExportResult{}, err
	}
	if warnings == nil {
		warnings = []string{}
	}
	return HostExportResult{Path: dest, Count: count, Warnings: warnings}, nil
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
	summary, err := local.ImportFromFile(src, passphrase)
	if err == nil {
		a.invalidateProfileCache()
		a.invalidateSettingsCache()
		a.retirePooledJumpsByProfile("")
	}
	return summary, err
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
		if strings.TrimRight(strings.TrimSpace(in.Endpoint), "/") == strings.TrimRight(strings.TrimSpace(old.Endpoint), "/") {
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
	ctx, cancel := context.WithCancel(a.baseContext())
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
	cfg, err := local.LoadAgentConfig()
	if err != nil {
		return "", fmt.Errorf("无法读取终端内容脱敏设置：%w", err)
	}
	red := security.Redact(selectedText, cfg.Redaction)
	selectedText = red.Text
	if red.Count > 0 {
		runtime.EventsEmit(a.ctx, "shell:notice", fmt.Sprintf("发送至 AI 前已自动脱敏 %d 处敏感信息", red.Count))
	}
	return local.ExplainStream(ctx, selectedText, question, func(delta string) {
		runtime.EventsEmit(a.ctx, "ai:delta", delta)
	})
}
