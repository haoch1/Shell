package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

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
	Token     string                   `json:"token"`
	ExePath   string                   `json:"exePath"`
	Policy    security.PolicyConfig    `json:"policy"`
	Redaction security.RedactionConfig `json:"redaction"`
}

func (a *App) initAgentBridge() {
	cfg, err := local.LoadAgentConfig()
	if err != nil {
		return
	}
	if !cfg.Enabled {
		local.ClearBridgeRuntime()
		return
	}
	token, err := local.EnsureAgentToken()
	if err != nil {
		return
	}
	port, err := a.bridge.Start(cfg.Port, token)
	if err == nil {
		_ = local.WriteBridgeRuntime(port)
	}
}

func (a *App) GetAgentStatus() (AgentStatusView, error) {
	cfg, err := local.LoadAgentConfig()
	if err != nil {
		return AgentStatusView{}, err
	}
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
	token, err := local.EnsureAgentToken()
	if err != nil {
		return AgentStatusView{}, err
	}
	if saved.Enabled {
		port, err := a.bridge.Start(saved.Port, token)
		if err != nil {
			return AgentStatusView{}, err
		}
		_ = local.WriteBridgeRuntime(port)
	} else {
		a.bridge.Stop()
		local.ClearBridgeRuntime()
	}
	return a.GetAgentStatus()
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
	// Check per-session approval cache (never cache high-risk danger actions!).
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
			Description: "List saved SSH host profiles accessible to the Agent (never returns passwords or private keys).",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "shell_list_sessions",
			Description: "List currently open SSH sessions inside Shell with their sessionId, host, state, and latency.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "shell_connect",
			Description: "Request Shell GUI to open a new SSH session tab for a saved host by profile ID or name.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"host": map[string]any{"type": "string", "description": "Saved profile ID, display name, or hostname"},
				},
				"required": []string{"host"},
			},
		},
		{
			Name:        "shell_exec",
			Description: "Execute a shell command over an active SSH session via a multiplexed exec channel. Output is automatically redacted for secrets and PII.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId":  map[string]any{"type": "string", "description": "Active sessionId (optional if only one session is open)"},
					"command":    map[string]any{"type": "string", "description": "POSIX shell command to run"},
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
					"sessionId": map[string]any{"type": "string", "description": "Active sessionId (optional if only one session is open)"},
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
					"sessionId": map[string]any{"type": "string", "description": "Active sessionId (optional if only one session is open)"},
					"command":   map[string]any{"type": "string", "description": "Command to send to the terminal"},
					"enter":     map[string]any{"type": "boolean", "description": "Whether to press Enter after the command (default true)"},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "shell_sftp_list",
			Description: "List files and directories at a remote path over SFTP.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string"},
					"path":      map[string]any{"type": "string", "description": "Remote directory path (default '.')"},
				},
			},
		},
		{
			Name:        "shell_sftp_read",
			Description: "Read a UTF-8 remote text file over SFTP (sensitive paths are blocked and secrets/PII are automatically redacted).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string"},
					"path":      map[string]any{"type": "string", "description": "Remote file path"},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "shell_sftp_write",
			Description: "Write or update a UTF-8 remote text file atomically over SFTP (requires approval unless trusted).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string"},
					"path":      map[string]any{"type": "string", "description": "Remote file path"},
					"content":   map[string]any{"type": "string", "description": "Full text content to save"},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "shell_server_stats",
			Description: "Fetch live CPU, memory, root disk, load average, uptime, and network metrics from an active SSH session.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{"type": "string"},
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

func (a *App) resolveLiveSession(requestedID string) (string, *liveSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	requestedID = strings.TrimSpace(requestedID)
	if requestedID != "" {
		if e := a.live[requestedID]; e != nil && e.session != nil {
			return requestedID, e, nil
		}
		return "", nil, fmt.Errorf("未找到活动的 SSH 会话：%s", requestedID)
	}
	var chosenID string
	var chosen *liveSession
	count := 0
	for id, e := range a.live {
		if e != nil && e.session != nil {
			chosenID = id
			chosen = e
			count++
		}
	}
	if count == 0 {
		return "", nil, errors.New("当前没有已连接的 SSH 会话，请先在 Shell 中连接主机或调用 shell_connect")
	}
	if count > 1 {
		return "", nil, errors.New("当前存在多个活动会话，请先调用 shell_list_sessions 并传入具体的 sessionId")
	}
	return chosenID, chosen, nil
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
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func (a *App) CallTool(ctx context.Context, source, clientName, toolName string, args map[string]any) agentbridge.ToolCallOutcome {
	t0 := time.Now()
	cfg, _ := local.LoadAgentConfig()

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
		_ = security.AppendAudit(audit)
		return out
	}

	switch toolName {
	case "shell_list_hosts":
		audit.Summary = "list saved hosts"
		profs, err := local.Profiles()
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		type safeHost struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Group       string   `json:"group,omitempty"`
			Host        string   `json:"host"`
			Port        int      `json:"port"`
			User        string   `json:"user"`
			Tags        []string `json:"tags,omitempty"`
			AgentAccess string   `json:"agentAccess"`
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
			LatencyMs   int64  `json:"latencyMs"`
			AgentAccess string `json:"agentAccess"`
		}
		list := make([]sessInfo, 0, len(a.live))
		for id, e := range a.live {
			if e == nil || e.session == nil || e.profile.AgentAccess == "deny" {
				continue
			}
			access := e.profile.AgentAccess
			if access == "" {
				access = "inherit"
			}
			list = append(list, sessInfo{
				SessionID: id, Name: e.profile.Name, Host: e.profile.Host,
				Port: e.profile.Port, User: e.profile.User,
				LatencyMs: e.session.LatencyMs(), AgentAccess: access,
			})
		}
		a.mu.Unlock()
		return finish(agentbridge.ToolCallOutcome{Text: mustJSONText(list)})

	case "shell_connect":
		target := strings.TrimSpace(strArg(args, "host"))
		audit.Summary = "connect " + target
		profs, err := local.Profiles()
		if err != nil {
			return finish(agentbridge.ToolCallOutcome{Text: err.Error(), IsError: true})
		}
		var matched *local.Profile
		for i := range profs {
			p := &profs[i]
			if p.ID == target || strings.EqualFold(p.Name, target) || strings.EqualFold(p.Host, target) {
				matched = p
				break
			}
		}
		if matched == nil {
			return finish(agentbridge.ToolCallOutcome{Text: "未找到匹配的已保存主机：" + target, IsError: true})
		}
		audit.Host = matched.Name
		eval := security.EvaluateMutatingTool("打开主机 "+matched.Name+" 的 SSH 会话", cfg.Policy, matched.AgentAccess)
		if eval.Decision == security.DecisionDeny {
			return finish(agentbridge.ToolCallOutcome{Text: eval.Reason, IsError: true, Risk: eval.Risk, Decision: "policy-deny"})
		}
		decLabel := "auto-allow"
		if eval.Decision == security.DecisionAsk {
			d, ok := a.requestApproval(ctx, source, clientName, matched.Name, "", toolName, "连接主机 "+matched.User+"@"+matched.Host, eval, "")
			decLabel = d
			if !ok {
				return finish(agentbridge.ToolCallOutcome{Text: "用户已拒绝连接请求", IsError: true, Risk: eval.Risk, Decision: decLabel})
			}
		}
		runtime.EventsEmit(a.ctx, "agent:connect_profile", matched.ID)
		return finish(agentbridge.ToolCallOutcome{
			Text:     fmt.Sprintf("已通知 Shell 界面打开主机「%s」(%s@%s:%d) 的会话标签，请稍后调用 shell_list_sessions 获取 sessionId。", matched.Name, matched.User, matched.Host, matched.Port),
			Risk:     eval.Risk,
			Decision: decLabel,
		})

	case "shell_exec":
		cmd := strings.TrimSpace(strArg(args, "command"))
		audit.Summary = cmd
		sid, entry, err := a.resolveLiveSession(strArg(args, "sessionId"))
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
			d, ok := a.requestApproval(ctx, source, clientName, entry.profile.Name, sid, toolName, cmd, eval, sig)
			decLabel = d
			if !ok {
				return finish(agentbridge.ToolCallOutcome{Text: "用户已拒绝执行命令：" + cmd, IsError: true, Risk: eval.Risk, Decision: decLabel})
			}
		}
		timeoutSec := intArg(args, "timeoutSec", 30)
		if timeoutSec < 3 || timeoutSec > 120 {
			timeoutSec = 30
		}
		execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
		res, execErr := entry.session.ExecDetailed(execCtx, cmd, 256<<10)
		rOut := security.Redact(res.Stdout, cfg.Redaction)
		rErr := security.Redact(res.Stderr, cfg.Redaction)
		totalRedacted := rOut.Count + rErr.Count
		payload := map[string]any{
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
		sid, entry, err := a.resolveLiveSession(strArg(args, "sessionId"))
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
		sid, entry, err := a.resolveLiveSession(strArg(args, "sessionId"))
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
		sid, entry, err := a.resolveLiveSession(strArg(args, "sessionId"))
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
		sid, entry, err := a.resolveLiveSession(strArg(args, "sessionId"))
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
		sid, entry, err := a.resolveLiveSession(strArg(args, "sessionId"))
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
		sid, entry, err := a.resolveLiveSession(strArg(args, "sessionId"))
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
		sid, entry, err := a.resolveLiveSession(strArg(args, "sessionId"))
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

// --- Built-in AI Agent Mode (reuses the user's configured OpenAI-compatible API!) ---

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
