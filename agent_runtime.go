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
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/haoch1/Shell/internal/agentbridge"
	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/remote"
	"github.com/haoch1/Shell/internal/security"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// AgentStatusView describes the running state and config of Shell's Agent/MCP bridge.
type AgentStatusView struct {
	Enabled   bool                     `json:"enabled"`
	Running   bool                     `json:"running"`
	Port      int                      `json:"port"`
	ExecMode  string                   `json:"execMode"` // "silent" or "visible"
	Token     string                   `json:"token"`
	ExePath   string                   `json:"exePath"`
	Policy    security.PolicyConfig    `json:"policy"`
	Redaction security.RedactionConfig `json:"redaction"`
}

func (a *App) getCachedAgentConfig() local.AgentConfig {
	a.agentCfgMu.RLock()
	if a.agentCfgLoaded {
		cfg := a.agentCfg
		a.agentCfgMu.RUnlock()
		return cfg
	}
	a.agentCfgMu.RUnlock()

	cfg, err := local.LoadAgentConfig()
	if err != nil {
		cfg = local.DefaultAgentConfig()
	}
	a.agentCfgMu.Lock()
	a.agentCfg = cfg
	a.agentCfgLoaded = true
	a.agentCfgMu.Unlock()
	return cfg
}

func (a *App) setCachedAgentConfig(cfg local.AgentConfig) {
	a.agentCfgMu.Lock()
	a.agentCfg = cfg
	a.agentCfgLoaded = true
	a.agentCfgMu.Unlock()
}

func (a *App) initAgentBridge() {
	cfg, err := local.LoadAgentConfig()
	if err != nil {
		return
	}
	a.setCachedAgentConfig(cfg)
	if !cfg.Enabled {
		local.ClearBridgeRuntime()
		return
	}
	token, err := local.EnsureAgentToken()
	if err != nil {
		return
	}
	port, pipeID, err := a.bridge.Start(cfg.Port, token)
	if err == nil {
		_ = local.WriteBridgeRuntime(port, pipeID)
	}
}

func (a *App) GetAgentStatus() (AgentStatusView, error) {
	cfg, err := local.LoadAgentConfig()
	if err != nil {
		return AgentStatusView{}, err
	}
	a.setCachedAgentConfig(cfg)
	token, err := local.EnsureAgentToken()
	if err != nil {
		return AgentStatusView{}, err
	}
	running, port := a.bridge.Running()
	if port == 0 {
		port = cfg.Port
	}
	exePath, _ := os.Executable()
	return AgentStatusView{
		Enabled:   cfg.Enabled,
		Running:   running,
		Port:      port,
		ExecMode:  cfg.ExecMode,
		Token:     token,
		ExePath:   exePath,
		Policy:    cfg.Policy,
		Redaction: cfg.Redaction,
	}, nil
}

func (a *App) SaveAgentConfig(cfg local.AgentConfig) (AgentStatusView, error) {
	saved, err := local.SaveAgentConfig(cfg)
	if err != nil {
		return AgentStatusView{}, err
	}
	a.setCachedAgentConfig(saved)
	token, err := local.EnsureAgentToken()
	if err != nil {
		return AgentStatusView{}, err
	}
	if saved.Enabled {
		port, pipeID, err := a.bridge.Start(saved.Port, token)
		if err != nil {
			return AgentStatusView{}, err
		}
		_ = local.WriteBridgeRuntime(port, pipeID)
	} else {
		a.bridge.Stop()
		local.ClearBridgeRuntime()
	}
	running, port := a.bridge.Running()
	if port == 0 {
		port = saved.Port
	}
	exePath, _ := os.Executable()
	return AgentStatusView{
		Enabled:   saved.Enabled,
		Running:   running,
		Port:      port,
		ExecMode:  saved.ExecMode,
		Token:     token,
		ExePath:   exePath,
		Policy:    saved.Policy,
		Redaction: saved.Redaction,
	}, nil
}

func (a *App) RotateAgentToken() (string, error) {
	token, err := local.RotateAgentToken()
	if err != nil {
		return "", err
	}
	a.bridge.SetToken(token)
	return token, nil
}

func (a *App) TestRedaction(sample string, cfg security.RedactionConfig) security.RedactResult {
	return security.Redact(sample, cfg)
}

func (a *App) TestCommandPolicy(command string, cfg security.PolicyConfig, hostAccess string) security.Evaluation {
	return security.EvaluateCommand(command, cfg, hostAccess)
}

func (a *App) AuditLogs(limit int) ([]security.AuditEntry, error) {
	return security.ListAudit(limit)
}

func (a *App) ClearAuditLogs() error {
	return security.ClearAudit()
}

// --- Universal SSH Config Importer Wails Bindings ---

func (a *App) PickAndParseImportFiles() (local.ImportPreviewResult, error) {
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择 SSH 配置文件",
		Filters: []runtime.FileFilter{
			{
				DisplayName: "所有支持的 SSH 配置 (*.xsh;*.xts;*.zip;*.mxtsessions;*.json;*.sessions;*.csv;*.xml;*.ini;*.reg;*.yaml;*.yml;config)",
				Pattern:     "*.xsh;*.xts;*.zip;*.mxtsessions;*.json;*.sessions;*.csv;*.xml;*.ini;*.reg;*.yaml;*.yml;*.conf;*.txt",
			},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil || len(paths) == 0 {
		return local.ImportPreviewResult{}, err
	}
	return local.ParseImportFiles(paths)
}

func (a *App) ParseImportFilePaths(paths []string) (local.ImportPreviewResult, error) {
	return local.ParseImportFiles(paths)
}

func (a *App) DiscoverLocalSSHConfigImport() (local.ImportPreviewResult, error) {
	entries, err := local.DiscoverSSHConfig()
	if err != nil {
		return local.ImportPreviewResult{}, err
	}
	hosts := make([]local.ImportedHost, 0, len(entries))
	for _, e := range entries {
		auth := "agent"
		if e.IdentityFile != "" {
			auth = "key"
		}
		hosts = append(hosts, local.ImportedHost{
			Name:         e.Alias,
			Host:         e.HostName,
			Port:         e.Port,
			User:         e.User,
			Auth:         auth,
			KeyPath:      e.IdentityFile,
			ProxyJump:    e.ProxyJump,
			ForwardAgent: e.ForwardAgent,
			KeepAliveSec: e.KeepAliveSec,
			SourceFormat: "OpenSSH",
			AlreadySaved: e.AlreadySaved,
		})
	}
	return local.ImportPreviewResult{
		DetectedFormat: "本机 OpenSSH (~/.ssh/config)",
		Hosts:          hosts,
	}, nil
}

func (a *App) CommitImportedHosts(entries []local.ImportedHost, overrideGroup string) ([]ProfileView, error) {
	saved, err := local.ImportParsedHosts(entries, overrideGroup)
	if err != nil {
		return nil, err
	}
	out := make([]ProfileView, 0, len(saved))
	for _, p := range saved {
		if v, err := view(p); err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// --- Interactive Approval Gate ---

func (a *App) ResolveAgentApproval(id, decision string) {
	a.mu.Lock()
	ch := a.approvals[id]
	a.mu.Unlock()
	if ch != nil {
		select {
		case ch <- decision:
		default:
		}
	}
}

func (a *App) requestApproval(ctx context.Context, source, clientName, host, sessionID, tool, summary string, eval security.Evaluation, cacheKey string) (string, bool) {
	if eval.Risk != "danger" && cacheKey != "" {
		a.mu.Lock()
		cached := a.sessionApprovals[cacheKey]
		a.mu.Unlock()
		if cached {
			return "session-allow", true
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	reqID := hex.EncodeToString(b[:])

	ch := make(chan string, 1)
	a.mu.Lock()
	a.approvals[reqID] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.approvals[reqID] == ch {
			delete(a.approvals, reqID)
		}
		a.mu.Unlock()
	}()

	// Bring window forward if user approval is required so the prompt isn't missed in tray.
	a.ShowMainWindow()
	runtime.EventsEmit(a.ctx, "agent:approval", map[string]any{
		"id":                 reqID,
		"source":             source,
		"clientName":         clientName,
		"host":               host,
		"sessionId":          sessionID,
		"tool":               tool,
		"summary":            summary,
		"risk":               eval.Risk,
		"reason":             eval.Reason,
		"segments":           eval.Segments,
		"canRememberSession": eval.Risk != "danger" && cacheKey != "",
	})

	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()
	select {
	case dec := <-ch:
		switch dec {
		case "allow_session":
			if eval.Risk != "danger" && cacheKey != "" {
				a.mu.Lock()
				a.sessionApprovals[cacheKey] = true
				a.mu.Unlock()
			}
			return "session-allow", true
		case "allow_once":
			return "user-allow", true
		default:
			return "user-deny", false
		}
	case <-timer.C:
		return "user-deny", false
	case <-ctx.Done():
		return "user-deny", false
	}
}

// --- MCP & Built-in AI Unified Tool Registry ---

func (a *App) ListTools() []agentbridge.ToolSpec {
	return []agentbridge.ToolSpec{
		{
			Name:        "shell_list_hosts",
			Description: "List saved SSH host profiles (including activeSessionId if already connected; never returns passwords or keys).",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "shell_list_sessions",
			Description: "List active SSH sessions (both GUI tabs and background MCP sessions) with sessionId, host, mode, and latency.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "shell_connect",
			Description: "Connect to a saved host by ID, display name, or IP and wait until ready, returning sessionId directly (headless in silent mode or GUI tab in visible mode).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"host": map[string]any{"type": "string", "description": "Saved profile ID, display name, or hostname/IP"},
				},
				"required": []string{"host"},
			},
		},
		{
			Name:        "shell_exec",
			Description: "Execute a shell command over a fast persistent SSH exec channel. Accepts either sessionId or host (auto-connects if needed). Output is automatically redacted.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId":  map[string]any{"type": "string", "description": "Active sessionId (optional if host is provided or only one session is open)"},
					"host":       map[string]any{"type": "string", "description": "Optional saved host name, IP, or profile ID (reuses or auto-connects)"},
					"command":    map[string]any{"type": "string", "description": "POSIX shell command or multi-line script to run"},
					"timeoutSec": map[string]any{"type": "integer", "description": "Optional timeout in seconds (default 30, max 120)"},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "shell_read_terminal",
			Description: "Read the most recent cleaned lines from an active interactive terminal buffer (redacted).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string", "description": "Active sessionId (optional if host is provided or only one session is open)"},
					"host":      map[string]any{"type": "string", "description": "Optional host name, IP, or profile ID"},
					"lines":     map[string]any{"type": "integer", "description": "Number of recent lines to read (default 80, max 400)"},
				},
			},
		},
		{
			Name:        "shell_send_terminal",
			Description: "Type and execute a command visibly inside the live interactive PTY terminal.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string", "description": "Active sessionId"},
					"host":      map[string]any{"type": "string", "description": "Optional host name, IP, or profile ID"},
					"command":   map[string]any{"type": "string", "description": "Command to send to the terminal"},
					"enter":     map[string]any{"type": "boolean", "description": "Whether to press Enter after the command (default true)"},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "shell_sftp_list",
			Description: "List files and directories at a remote path over SFTP (accepts sessionId or host).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string"},
					"host":      map[string]any{"type": "string", "description": "Optional host name, IP, or profile ID"},
					"path":      map[string]any{"type": "string", "description": "Remote directory path (default '.')"},
				},
			},
		},
		{
			Name:        "shell_sftp_read",
			Description: "Read a UTF-8 remote text file over SFTP (accepts sessionId or host; sensitive paths blocked & secrets redacted).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string"},
					"host":      map[string]any{"type": "string", "description": "Optional host name, IP, or profile ID"},
					"path":      map[string]any{"type": "string", "description": "Remote file path"},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "shell_sftp_write",
			Description: "Write or update a UTF-8 remote text file atomically over SFTP (accepts sessionId or host).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string"},
					"host":      map[string]any{"type": "string", "description": "Optional host name, IP, or profile ID"},
					"path":      map[string]any{"type": "string", "description": "Remote file path"},
					"content":   map[string]any{"type": "string", "description": "Full text content to save"},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "shell_server_stats",
			Description: "Fetch live CPU, memory, root disk, load average, uptime, and network metrics (accepts sessionId or host).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string"},
					"host":      map[string]any{"type": "string", "description": "Optional host name, IP, or profile ID"},
				},
			},
		},
		{
			Name:        "shell_tunnels",
			Description: "List active port forwarding tunnels (-L/-R/-D), or start/stop a tunnel on an active session.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId":  map[string]any{"type": "string"},
					"host":       map[string]any{"type": "string", "description": "Optional host name, IP, or profile ID"},
					"action":     map[string]any{"type": "string", "enum": []string{"list", "start", "stop"}},
					"tunnelId":   map[string]any{"type": "string"},
					"mode":       map[string]any{"type": "string", "enum": []string{"local", "remote", "dynamic"}},
					"bindHost":   map[string]any{"type": "string"},
					"bindPort":   map[string]any{"type": "integer"},
					"targetHost": map[string]any{"type": "string"},
					"targetPort": map[string]any{"type": "integer"},
					"name":       map[string]any{"type": "string"},
				},
			},
		},
	}
}

func (a *App) mirrorToTerminal(sessionID, text string) {
	if sessionID == "" || text == "" {
		return
	}
	data := []byte(text)
	if a.ws != nil && a.ws.SendOutput(sessionID, data) {
		return
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "ssh:data", sessionID, base64.StdEncoding.EncodeToString(data))
	}
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func formatMirrorLines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "\r\n")
	return s
}

func findSavedProfile(target string) (*local.Profile, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("未指定目标主机")
	}
	profs, err := local.Profiles()
	if err != nil {
		return nil, err
	}
	return matchSavedProfile(profs, target)
}

func matchSavedProfile(profs []local.Profile, target string) (*local.Profile, error) {
	var match *local.Profile
	ambiguous := false
	for i := range profs {
		p := &profs[i]
		if p.ID == target {
			return p, nil
		}
		if strings.EqualFold(p.Name, target) || strings.EqualFold(p.Host, target) {
			ambiguous = match != nil
			match = p
		}
	}
	if ambiguous {
		return nil, errors.New("多个连接资料匹配这个目标，请使用已保存的主机 ID")
	}
	if match != nil {
		return match, nil
	}
	return nil, fmt.Errorf("未找到匹配的已保存主机：%s", target)
}

func sameSessionTarget(p, q local.Profile) bool {
	return p.ID != "" && p.ID == q.ID && strings.EqualFold(p.Host, q.Host) && p.Port == q.Port &&
		p.User == q.User && p.Auth == q.Auth && p.KeyPath == q.KeyPath && p.JumpHostID == q.JumpHostID &&
		p.ProxyType == q.ProxyType && p.ProxyHost == q.ProxyHost && p.ProxyPort == q.ProxyPort &&
		p.ProxyUser == q.ProxyUser && p.ForwardAgent == q.ForwardAgent && p.AgentAccess == q.AgentAccess &&
		p.StartupCommand == q.StartupCommand
}

func (a *App) findExistingSessionForProfile(p local.Profile) (sid string, entry *liveSession, isGUI bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Prefer live GUI tab first
	for id, e := range a.live {
		if e != nil && e.session != nil && sameSessionTarget(p, e.profile) {
			return id, e, true
		}
	}
	for id, e := range a.headless {
		if e != nil && e.session != nil && sameSessionTarget(p, e.profile) {
			return id, e, false
		}
	}
	return "", nil, false
}

// connectHeadlessProfile dials a pooled background SSH transport without touching the GUI (works even in tray).
func (a *App) connectHeadlessProfile(ctx context.Context, p local.Profile) (string, *liveSession, error) {
	return a.connectHeadlessOnce(ctx, p, a.dialHeadlessProfile)
}

func (a *App) dialHeadlessProfile(ctx context.Context, p local.Profile) (string, *liveSession, error) {
	var secret string
	if p.ID != "" && (p.Auth == "password" || p.Auth == "key") {
		var err error
		secret, err = local.LoadSecret(p)
		if err != nil {
			return "", nil, fmt.Errorf("读取已保存的凭据失败：%w", err)
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
		return "", nil, err
	}

	var b [6]byte
	_, _ = rand.Read(b[:])
	sid := "mcp-" + hex.EncodeToString(b[:])

	baseCtx := a.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	sessCtx, cancel := context.WithCancel(baseCtx)
	if err := ctx.Err(); err != nil {
		cancel()
		return "", nil, err
	}
	stopRequestCancel := context.AfterFunc(ctx, cancel)
	defer stopRequestCancel()
	cfg := remote.Config{
		Host:         p.Host,
		User:         p.User,
		Port:         p.Port,
		Auth:         p.Auth,
		KeyPath:      p.KeyPath,
		KeepAliveSec: keepAlive,
		ForwardAgent: p.ForwardAgent,
		Proxy:        effectiveProxyConfig(p, settings),
		Jump:         jumpCfg,
	}
	if p.Auth == "key" {
		cfg.Passphrase = secret
	} else if p.Auth == "password" {
		cfg.Password = secret
	}

	s, err := remote.ConnectHeadless(sessCtx, cfg, remote.Callbacks{
		Verify: func(host, algorithm, fingerprint string) bool {
			if a.ctx == nil {
				return false
			}
			a.ShowMainWindow()
			return a.confirm(sessCtx, sid, host, algorithm, fingerprint)
		},
		Challenge: func(user, instruction string, questions []string, echo []bool) ([]string, error) {
			if a.ctx == nil {
				return nil, errors.New("后台静默模式无法完成交互式 2FA 认证")
			}
			a.ShowMainWindow()
			return a.challenge(sessCtx, sid, p.Host, user, instruction, questions, echo)
		},
		End: func(error) {
			cancel()
			a.mu.Lock()
			delete(a.headless, sid)
			a.clearSessionApprovalsLocked(sid)
			a.mu.Unlock()
		},
	})
	if err != nil {
		cancel()
		return "", nil, err
	}
	entry := &liveSession{cancel: cancel, session: s, profile: p}
	a.mu.Lock()
	if !stopRequestCancel() || ctx.Err() != nil || sessCtx.Err() != nil {
		a.mu.Unlock()
		cancel()
		s.Close()
		return "", nil, context.Canceled
	}
	a.headless[sid] = entry
	a.mu.Unlock()
	return sid, entry, nil
}

// connectVisibleProfile brings the GUI window up, opens/focuses the host tab, and waits until connected.
func (a *App) connectVisibleProfile(ctx context.Context, p local.Profile) (string, *liveSession, error) {
	a.ShowMainWindow()
	if a.ctx == nil {
		return "", nil, errors.New("前台窗口尚未就绪")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	sid, entry, err := a.waitVisibleConnect(waitCtx, p, func() { runtime.EventsEmit(a.ctx, "agent:connect_profile", p.ID, true) })
	if errors.Is(err, context.DeadlineExceeded) {
		return "", nil, fmt.Errorf("等待前台打开「%s」超时：%w", p.Name, err)
	}
	if err == nil {
		runtime.EventsEmit(a.ctx, "agent:focus_session", sid)
	}
	return sid, entry, err
}

func (a *App) resolveLiveSession(requestedID string) (string, *liveSession, error) {
	sid, entry, _, err := a.resolveSessionByIDOrSingle(requestedID)
	return sid, entry, err
}

func (a *App) resolveSessionByIDOrSingle(requestedID string) (string, *liveSession, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	requestedID = strings.TrimSpace(requestedID)
	if requestedID != "" {
		if e := a.live[requestedID]; e != nil && e.session != nil {
			return requestedID, e, true, nil
		}
		if e := a.headless[requestedID]; e != nil && e.session != nil {
			return requestedID, e, false, nil
		}
		return "", nil, false, fmt.Errorf("未找到活动的 SSH 会话：%s", requestedID)
	}
	var chosenID string
	var chosen *liveSession
	chosenGUI := false
	count := 0
	for id, e := range a.live {
		if e != nil && e.session != nil {
			chosenID = id
			chosen = e
			chosenGUI = true
			count++
		}
	}
	for id, e := range a.headless {
		if e != nil && e.session != nil {
			chosenID = id
			chosen = e
			chosenGUI = false
			count++
		}
	}
	if count == 0 {
		return "", nil, false, errors.New("当前没有已连接的 SSH 会话，请传入 host 参数（将自动连接）或先调用 shell_connect")
	}
	if count > 1 {
		return "", nil, false, errors.New("当前存在多个活动会话，请传入 sessionId 或 host 参数指定目标主机")
	}
	return chosenID, chosen, chosenGUI, nil
}

// resolveOrConnectSession resolves by sessionId, by host (auto-reusing or auto-connecting according to ExecMode),
// or falls back to the sole open session.
func (a *App) resolveOrConnectSession(ctx context.Context, reqSessionID, reqHost string, cfg local.AgentConfig, requirePTY bool) (string, *liveSession, bool, error) {
	reqSessionID = strings.TrimSpace(reqSessionID)
	reqHost = strings.TrimSpace(reqHost)
	visible := cfg.ExecMode == "visible" || requirePTY

	if reqSessionID != "" {
		sid, entry, isGUI, err := a.resolveSessionByIDOrSingle(reqSessionID)
		if err == nil {
			return a.useLiveAgentSession(ctx, sid, entry, isGUI, visible)
		}
		// Allow caller passing a host name into sessionId gracefully
		if reqHost == "" {
			reqHost = reqSessionID
		} else {
			return "", nil, false, err
		}
	}

	if reqHost != "" {
		prof, err := findSavedProfile(reqHost)
		if err != nil {
			return "", nil, false, err
		}
		if prof.AgentAccess == "deny" {
			return "", nil, false, errors.New("该主机已禁止 Agent 访问")
		}
		if sid, entry, isGUI := a.findExistingSessionForProfile(*prof); entry != nil {
			if visible && isGUI && a.ctx != nil {
				a.ShowMainWindow()
				runtime.EventsEmit(a.ctx, "agent:focus_session", sid)
				return sid, entry, true, nil
			}
			if !visible {
				return sid, entry, isGUI, nil
			}
		}
		if visible {
			sid, entry, err := a.connectVisibleProfile(ctx, *prof)
			return sid, entry, true, err
		}
		sid, entry, err := a.connectHeadlessProfile(ctx, *prof)
		return sid, entry, false, err
	}

	sid, entry, isGUI, err := a.resolveSessionByIDOrSingle("")
	if err != nil {
		return "", nil, false, err
	}
	return a.useLiveAgentSession(ctx, sid, entry, isGUI, visible)
}

func (a *App) useLiveAgentSession(ctx context.Context, sid string, entry *liveSession, isGUI, visible bool) (string, *liveSession, bool, error) {
	p := entry.profile
	if p.AgentAccess == "deny" {
		return "", nil, false, errors.New("该主机已禁止 Agent 访问")
	}
	if p.ID != "" {
		current, ok, err := local.GetProfile(p.ID)
		if err != nil {
			return "", nil, false, err
		}
		if !ok || !sameSessionTarget(p, current) {
			return "", nil, false, errors.New("主机资料或 Agent 权限已更改，请使用最新资料重新连接")
		}
	}
	if visible && !isGUI {
		if p.ID == "" {
			return "", nil, false, errors.New("后台会话缺少前台连接所需的主机资料")
		}
		guiID, gui, err := a.connectVisibleProfile(ctx, p)
		return guiID, gui, true, err
	}
	if visible && isGUI && a.ctx != nil {
		a.ShowMainWindow()
		runtime.EventsEmit(a.ctx, "agent:focus_session", sid)
	}
	return sid, entry, isGUI, nil
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, key string, def int) int {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func boolArg(args map[string]any, key string, def bool) bool {
	if args == nil {
		return def
	}
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func mustJSONText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func elapsedMs(start time.Time) *int64 {
	ms := time.Since(start).Milliseconds()
	return &ms
}

func (a *App) CallTool(ctx context.Context, source, clientName, toolName string, args map[string]any) agentbridge.ToolCallOutcome {
	t0 := time.Now()
	cfg := a.getCachedAgentConfig()

	audit := security.AuditEntry{
		Source:     source,
		ClientName: clientName,
		Tool:       toolName,
		Risk:       "safe",
		Decision:   "auto-allow",
	}
	finish := func(out agentbridge.ToolCallOutcome) agentbridge.ToolCallOutcome {
		audit.DurationMs = time.Since(t0).Milliseconds()
		audit.OK = !out.IsError
		if out.IsError {
			audit.Error = out.Text
		}
		if out.Risk != "" {
			audit.Risk = out.Risk
		}
		if out.Decision != "" {
			audit.Decision = out.Decision
		}
		audit.RedactedCount = out.RedactedCount
		go func(entry security.AuditEntry) {
			_ = security.AppendAudit(entry)
		}(audit)
		return out
	}

	switch toolName {
	case "shell_list_hosts":
		audit.Summary = "list saved hosts"
		profs, err := local.Profiles()
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		activeByProfile := make(map[string]string)
		a.mu.Lock()
		for id, e := range a.headless {
			if e != nil && e.session != nil && e.profile.ID != "" {
				activeByProfile[e.profile.ID] = id
			}
		}
		for id, e := range a.live {
			if e != nil && e.session != nil && e.profile.ID != "" {
				activeByProfile[e.profile.ID] = id
			}
		}
		a.mu.Unlock()

		type safeHost struct {
			ID              string   `json:"id"`
			Name            string   `json:"name"`
			Group           string   `json:"group,omitempty"`
			Host            string   `json:"host"`
			Port            int      `json:"port"`
			User            string   `json:"user"`
			Tags            []string `json:"tags,omitempty"`
			AgentAccess     string   `json:"agentAccess"`
			ActiveSessionID string   `json:"activeSessionId,omitempty"`
		}
		var list []safeHost
		for _, p := range profs {
			if p.AgentAccess == "deny" {
				continue
			}
			access := p.AgentAccess
			if access == "" {
				access = "inherit"
			}
			list = append(list, safeHost{
				ID: p.ID, Name: p.Name, Group: p.Group, Host: p.Host,
				Port: p.Port, User: p.User, Tags: p.Tags, AgentAccess: access,
				ActiveSessionID: activeByProfile[p.ID],
			})
		}
		red := security.Redact(mustJSONText(list), cfg.Redaction)
		return finish(agentbridge.ToolCallOutcome{Text: red.Text, RedactedCount: red.Count})

	case "shell_list_sessions":
		audit.Summary = "list active sessions"
		a.mu.Lock()
		type sessInfo struct {
			SessionID   string `json:"sessionId"`
			Name        string `json:"name"`
			Host        string `json:"host"`
			Port        int    `json:"port"`
			User        string `json:"user"`
			Mode        string `json:"mode"`
			LatencyMs   int64  `json:"latencyMs"`
			AgentAccess string `json:"agentAccess"`
		}
		list := make([]sessInfo, 0, len(a.live)+len(a.headless))
		appendSessions := func(m map[string]*liveSession, mode string) {
			for id, e := range m {
				if e == nil || e.session == nil || e.profile.AgentAccess == "deny" {
					continue
				}
				access := e.profile.AgentAccess
				if access == "" {
					access = "inherit"
				}
				list = append(list, sessInfo{
					SessionID: id, Name: e.profile.Name, Host: e.profile.Host,
					Port: e.profile.Port, User: e.profile.User, Mode: mode,
					LatencyMs: e.session.LatencyMs(), AgentAccess: access,
				})
			}
		}
		appendSessions(a.live, "visible")
		appendSessions(a.headless, "silent")
		a.mu.Unlock()
		return finish(agentbridge.ToolCallOutcome{Text: mustJSONText(list)})

	case "shell_connect":
		audit.Phases = &security.AuditPhases{}
		target := strings.TrimSpace(strArg(args, "host"))
		audit.Summary = "connect " + target
		matched, err := findSavedProfile(target)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		audit.Host = matched.Name
		eval := security.EvaluateMutatingTool("打开主机 "+matched.Name+" 的 SSH 会话", cfg.Policy, matched.AgentAccess)
		if eval.Decision == security.DecisionDeny {
			return finish(agentbridge.ToolCallOutcome{Text: eval.Reason, IsError: true, Risk: eval.Risk, Decision: "policy-deny"})
		}
		decLabel := "auto-allow"
		if eval.Decision == security.DecisionAsk {
			approvalStart := time.Now()
			d, ok := a.requestApproval(ctx, source, clientName, matched.Name, "", toolName, "连接主机 "+matched.User+"@"+matched.Host, eval, "")
			audit.Phases.ApprovalMs = elapsedMs(approvalStart)
			decLabel = d
			if !ok {
				return finish(agentbridge.ToolCallOutcome{Text: "用户已拒绝连接请求", IsError: true, Risk: eval.Risk, Decision: decLabel})
			}
		}
		sessionStart := time.Now()
		existingID, _, _ := a.findExistingSessionForProfile(*matched)
		var sid string
		var connErr error
		mode := cfg.ExecMode
		if mode == "" {
			mode = "silent"
		}
		if mode == "visible" {
			sid, _, connErr = a.connectVisibleProfile(ctx, *matched)
		} else {
			sid, _, connErr = a.connectHeadlessProfile(ctx, *matched)
		}
		audit.Phases.SessionMs = elapsedMs(sessionStart)
		if connErr != nil {
			return finish(agentbridge.ToolCallOutcome{Text: connErr.Error(), IsError: true, Risk: eval.Risk, Decision: decLabel})
		}
		audit.SessionID = sid
		return finish(agentbridge.ToolCallOutcome{
			Text: mustJSONText(map[string]any{
				"sessionId": sid,
				"name":      matched.Name,
				"host":      matched.Host,
				"port":      matched.Port,
				"user":      matched.User,
				"mode":      mode,
				"reused":    existingID == sid,
			}),
			Risk:     eval.Risk,
			Decision: decLabel,
		})

	case "shell_exec":
		audit.Phases = &security.AuditPhases{}
		cmd := strings.TrimSpace(strArg(args, "command"))
		audit.Summary = cmd
		if cmd == "" {
			return finish(agentbridge.ToolCallOutcome{Text: "策略已拦截：命令为空", IsError: true, Risk: "safe", Decision: "policy-deny"})
		}
		sessionStart := time.Now()
		sid, entry, isGUI, err := a.resolveOrConnectSession(ctx, strArg(args, "sessionId"), strArg(args, "host"), cfg, false)
		audit.Phases.SessionMs = elapsedMs(sessionStart)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		audit.SessionID = sid
		audit.Host = entry.profile.Name
		eval := security.EvaluateCommand(cmd, cfg.Policy, entry.profile.AgentAccess)
		if eval.Decision == security.DecisionDeny {
			return finish(agentbridge.ToolCallOutcome{Text: "策略已拦截：" + eval.Reason, IsError: true, Risk: eval.Risk, Decision: "policy-deny"})
		}
		decLabel := "auto-allow"
		if eval.Decision == security.DecisionAsk {
			sig := ""
			if len(eval.Segments) > 0 {
				sig = sid + "|exec|" + eval.Segments[0].Program
			}
			approvalStart := time.Now()
			d, ok := a.requestApproval(ctx, source, clientName, entry.profile.Name, sid, toolName, cmd, eval, sig)
			audit.Phases.ApprovalMs = elapsedMs(approvalStart)
			decLabel = d
			if !ok {
				return finish(agentbridge.ToolCallOutcome{Text: "用户已拒绝执行命令：" + cmd, IsError: true, Risk: eval.Risk, Decision: decLabel})
			}
		}
		timeoutSec := intArg(args, "timeoutSec", 30)
		if timeoutSec < 3 || timeoutSec > 120 {
			timeoutSec = 30
		}
		mirrorVisible := cfg.ExecMode == "visible" && isGUI
		if mirrorVisible {
			a.mirrorToTerminal(sid, fmt.Sprintf("\r\n\x1b[38;5;75m[MCP · %s]\x1b[0m \x1b[1m$ %s\x1b[0m\r\n", clientName, cmd))
		}
		execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
		remoteStart := time.Now()
		res, execErr := entry.session.ExecDetailed(execCtx, cmd, 256<<10)
		audit.Phases.RemoteMs = elapsedMs(remoteStart)
		rOut := security.Redact(res.Stdout, cfg.Redaction)
		rErr := security.Redact(res.Stderr, cfg.Redaction)
		if mirrorVisible {
			var mirrorBuf strings.Builder
			if outPreview := strings.TrimRight(res.Stdout, "\r\n"); outPreview != "" {
				if len(outPreview) > 8192 {
					outPreview = truncateUTF8(outPreview, 8192) + "\n…(已截断显示)"
				}
				mirrorBuf.WriteString(formatMirrorLines(outPreview))
				mirrorBuf.WriteString("\r\n")
			}
			if errPreview := strings.TrimRight(res.Stderr, "\r\n"); errPreview != "" {
				if len(errPreview) > 4096 {
					errPreview = truncateUTF8(errPreview, 4096) + "\n…(已截断显示)"
				}
				mirrorBuf.WriteString("\x1b[31m")
				mirrorBuf.WriteString(formatMirrorLines(errPreview))
				mirrorBuf.WriteString("\x1b[0m\r\n")
			}
			mirrorBuf.WriteString(fmt.Sprintf("\x1b[90m[exit %d · %dms]\x1b[0m\r\n", res.ExitCode, res.DurationMs))
			a.mirrorToTerminal(sid, mirrorBuf.String())
		}
		totalRedacted := rOut.Count + rErr.Count
		payload := map[string]any{
			"sessionId":     sid,
			"exitCode":      res.ExitCode,
			"durationMs":    res.DurationMs,
			"stdout":        rOut.Text,
			"stderr":        rErr.Text,
			"redactedCount": totalRedacted,
		}
		if execErr != nil && res.ExitCode < 0 {
			return finish(agentbridge.ToolCallOutcome{Text: execErr.Error(), IsError: true, Risk: eval.Risk, Decision: decLabel, RedactedCount: totalRedacted})
		}
		return finish(agentbridge.ToolCallOutcome{Text: mustJSONText(payload), Risk: eval.Risk, Decision: decLabel, RedactedCount: totalRedacted})

	case "shell_read_terminal":
		lines := intArg(args, "lines", 80)
		audit.Summary = fmt.Sprintf("read last %d terminal lines", lines)
		sid, entry, _, err := a.resolveOrConnectSession(ctx, strArg(args, "sessionId"), strArg(args, "host"), cfg, false)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		audit.SessionID = sid
		audit.Host = entry.profile.Name
		if entry.profile.AgentAccess == "deny" {
			return finish(agentbridge.ToolCallOutcome{Text: "该主机已禁止 Agent 访问", IsError: true, Risk: "danger", Decision: "policy-deny"})
		}
		raw := entry.session.RecentOutput(lines)
		red := security.Redact(raw, cfg.Redaction)
		return finish(agentbridge.ToolCallOutcome{Text: red.Text, RedactedCount: red.Count})

	case "shell_send_terminal":
		cmd := strArg(args, "command")
		enter := boolArg(args, "enter", true)
		audit.Summary = "pty: " + strings.TrimSpace(cmd)
		sid, entry, _, err := a.resolveOrConnectSession(ctx, strArg(args, "sessionId"), strArg(args, "host"), cfg, true)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		audit.SessionID = sid
		audit.Host = entry.profile.Name
		eval := security.EvaluateCommand(cmd, cfg.Policy, entry.profile.AgentAccess)
		if eval.Decision == security.DecisionDeny {
			return finish(agentbridge.ToolCallOutcome{Text: "策略已拦截：" + eval.Reason, IsError: true, Risk: eval.Risk, Decision: "policy-deny"})
		}
		decLabel := "auto-allow"
		if eval.Decision == security.DecisionAsk {
			d, ok := a.requestApproval(ctx, source, clientName, entry.profile.Name, sid, toolName, cmd, eval, sid+"|pty|"+toolName)
			decLabel = d
			if !ok {
				return finish(agentbridge.ToolCallOutcome{Text: "用户已拒绝发送到终端", IsError: true, Risk: eval.Risk, Decision: decLabel})
			}
		}
		payload := cmd
		if enter && !strings.HasSuffix(payload, "\n") {
			payload += "\n"
		}
		if err := entry.session.Write([]byte(payload)); err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true, Risk: eval.Risk, Decision: decLabel})
		}
		return finish(agentbridge.ToolCallOutcome{Text: "已将命令发送到交互终端。", Risk: eval.Risk, Decision: decLabel})

	case "shell_sftp_list":
		dir := strings.TrimSpace(strArg(args, "path"))
		if dir == "" {
			dir = "."
		}
		audit.Summary = "sftp ls " + dir
		sid, entry, _, err := a.resolveOrConnectSession(ctx, strArg(args, "sessionId"), strArg(args, "host"), cfg, false)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		audit.SessionID = sid
		audit.Host = entry.profile.Name
		eval := security.EvaluatePath(dir, false, cfg.Policy, entry.profile.AgentAccess)
		if eval.Decision == security.DecisionDeny {
			return finish(agentbridge.ToolCallOutcome{Text: eval.Reason, IsError: true, Risk: eval.Risk, Decision: "policy-deny"})
		}
		items, err := entry.session.List(dir)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		return finish(agentbridge.ToolCallOutcome{Text: mustJSONText(items)})

	case "shell_sftp_read":
		p := strings.TrimSpace(strArg(args, "path"))
		audit.Summary = "sftp read " + p
		if p == "" {
			return finish(agentbridge.ToolCallOutcome{Text: "请输入远端文件路径", IsError: true})
		}
		sid, entry, _, err := a.resolveOrConnectSession(ctx, strArg(args, "sessionId"), strArg(args, "host"), cfg, false)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		audit.SessionID = sid
		audit.Host = entry.profile.Name
		eval := security.EvaluatePath(p, false, cfg.Policy, entry.profile.AgentAccess)
		if eval.Decision == security.DecisionDeny {
			return finish(agentbridge.ToolCallOutcome{Text: "敏感路径已拦截：" + eval.Reason, IsError: true, Risk: eval.Risk, Decision: "policy-deny"})
		}
		decLabel := "auto-allow"
		if eval.Decision == security.DecisionAsk {
			d, ok := a.requestApproval(ctx, source, clientName, entry.profile.Name, sid, toolName, "读取文件 "+p, eval, sid+"|sftp_read")
			decLabel = d
			if !ok {
				return finish(agentbridge.ToolCallOutcome{Text: "用户已拒绝读取文件", IsError: true, Risk: eval.Risk, Decision: decLabel})
			}
		}
		text, err := entry.session.ReadText(p)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true, Decision: decLabel})
		}
		red := security.Redact(text, cfg.Redaction)
		return finish(agentbridge.ToolCallOutcome{Text: red.Text, Decision: decLabel, RedactedCount: red.Count})

	case "shell_sftp_write":
		p := strings.TrimSpace(strArg(args, "path"))
		content := strArg(args, "content")
		audit.Summary = fmt.Sprintf("sftp write %s (%d bytes)", p, len(content))
		if p == "" {
			return finish(agentbridge.ToolCallOutcome{Text: "请输入远端文件路径", IsError: true})
		}
		sid, entry, _, err := a.resolveOrConnectSession(ctx, strArg(args, "sessionId"), strArg(args, "host"), cfg, false)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		audit.SessionID = sid
		audit.Host = entry.profile.Name
		eval := security.EvaluatePath(p, true, cfg.Policy, entry.profile.AgentAccess)
		if eval.Decision == security.DecisionDeny {
			return finish(agentbridge.ToolCallOutcome{Text: eval.Reason, IsError: true, Risk: eval.Risk, Decision: "policy-deny"})
		}
		decLabel := "auto-allow"
		if eval.Decision == security.DecisionAsk {
			d, ok := a.requestApproval(ctx, source, clientName, entry.profile.Name, sid, toolName, fmt.Sprintf("写入远端文件 %s (%d 字节)", p, len(content)), eval, sid+"|sftp_write|"+path.Dir(p))
			decLabel = d
			if !ok {
				return finish(agentbridge.ToolCallOutcome{Text: "用户已拒绝写入远端文件", IsError: true, Risk: eval.Risk, Decision: decLabel})
			}
		}
		if err := entry.session.WriteText(p, content); err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true, Risk: eval.Risk, Decision: decLabel})
		}
		return finish(agentbridge.ToolCallOutcome{Text: "已原子写入远端文件：" + p, Risk: eval.Risk, Decision: decLabel})

	case "shell_server_stats":
		audit.Summary = "server telemetry stats"
		sid, entry, _, err := a.resolveOrConnectSession(ctx, strArg(args, "sessionId"), strArg(args, "host"), cfg, false)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		audit.SessionID = sid
		audit.Host = entry.profile.Name
		if entry.profile.AgentAccess == "deny" {
			return finish(agentbridge.ToolCallOutcome{Text: "该主机已禁止 Agent 访问", IsError: true, Risk: "danger", Decision: "policy-deny"})
		}
		stats, err := entry.session.FetchStats(ctx)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		return finish(agentbridge.ToolCallOutcome{Text: mustJSONText(stats)})

	case "shell_tunnels":
		action := strings.ToLower(strings.TrimSpace(strArg(args, "action")))
		if action == "" {
			action = "list"
		}
		audit.Summary = "tunnels " + action
		sid, entry, _, err := a.resolveOrConnectSession(ctx, strArg(args, "sessionId"), strArg(args, "host"), cfg, false)
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		audit.SessionID = sid
		audit.Host = entry.profile.Name
		if entry.profile.AgentAccess == "deny" {
			return finish(agentbridge.ToolCallOutcome{Text: "该主机已禁止 Agent 访问", IsError: true, Risk: "danger", Decision: "policy-deny"})
		}
		if action == "list" {
			return finish(agentbridge.ToolCallOutcome{Text: mustJSONText(entry.session.ListTunnels())})
		}
		eval := security.EvaluateMutatingTool("端口转发操作 ("+action+")", cfg.Policy, entry.profile.AgentAccess)
		if eval.Decision == security.DecisionDeny {
			return finish(agentbridge.ToolCallOutcome{Text: eval.Reason, IsError: true, Risk: eval.Risk, Decision: "policy-deny"})
		}
		decLabel := "auto-allow"
		if eval.Decision == security.DecisionAsk {
			d, ok := a.requestApproval(ctx, source, clientName, entry.profile.Name, sid, toolName, fmt.Sprintf("端口转发 %s (%s :%d)", action, strArg(args, "mode"), intArg(args, "bindPort", 0)), eval, "")
			decLabel = d
			if !ok {
				return finish(agentbridge.ToolCallOutcome{Text: "用户已拒绝端口转发操作", IsError: true, Risk: eval.Risk, Decision: decLabel})
			}
		}
		if action == "stop" {
			entry.session.StopTunnel(strArg(args, "tunnelId"))
			return finish(agentbridge.ToolCallOutcome{Text: "已停止端口转发", Risk: eval.Risk, Decision: decLabel})
		}
		tid := strArg(args, "tunnelId")
		if tid == "" {
			var b [6]byte
			_, _ = rand.Read(b[:])
			tid = "mcp-" + hex.EncodeToString(b[:])
		}
		st, err := entry.session.StartTunnel(remote.TunnelSpec{
			ID:         tid,
			Name:       strArg(args, "name"),
			Mode:       strArg(args, "mode"),
			BindHost:   strArg(args, "bindHost"),
			BindPort:   intArg(args, "bindPort", 0),
			TargetHost: strArg(args, "targetHost"),
			TargetPort: intArg(args, "targetPort", 0),
		})
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true, Risk: eval.Risk, Decision: decLabel})
		}
		return finish(agentbridge.ToolCallOutcome{Text: mustJSONText(st), Risk: eval.Risk, Decision: decLabel})

	default:
		return finish(agentbridge.ToolCallOutcome{Text: "未知工具：" + toolName, IsError: true})
	}
}

// --- Built-in AI Agent Mode (reuses the user's configured OpenAI-compatible API or local CLI!) ---

func (a *App) RunAIAgent(sessionID, goal string) (string, error) {
	ctx, done := a.beginAI()
	defer done()

	hostLabel := ""
	if sid, entry, err := a.resolveLiveSession(sessionID); err == nil {
		sessionID = sid
		hostLabel = fmt.Sprintf("%s (%s@%s:%d, sessionId=%s)", entry.profile.Name, entry.profile.User, entry.profile.Host, entry.profile.Port, sid)
	}

	mcpTools := a.ListTools()
	defs := make([]local.ToolDefinition, 0, len(mcpTools))
	for _, t := range mcpTools {
		defs = append(defs, local.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}

	return local.RunAgentLoop(
		ctx,
		goal,
		hostLabel,
		defs,
		func(callCtx context.Context, toolName string, args map[string]any) (string, string, string, int, error) {
			if sessionID != "" && args != nil {
				if _, ok := args["sessionId"]; !ok && toolName != "shell_list_hosts" && toolName != "shell_list_sessions" && toolName != "shell_connect" {
					args["sessionId"] = sessionID
				}
			}
			out := a.CallTool(callCtx, "builtin-ai", "Shell AI Agent", toolName, args)
			if out.IsError {
				return "", out.Risk, out.Decision, out.RedactedCount, errors.New(out.Text)
			}
			return out.Text, out.Risk, out.Decision, out.RedactedCount, nil
		},
		func(ev local.AgentStepEvent) {
			runtime.EventsEmit(a.ctx, "ai:agent_step", ev)
		},
	)
}
