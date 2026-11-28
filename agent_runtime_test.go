//go:build windows

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/security"
)

func TestLocalTerminalAgentToolsPolicyRedactionAndRecentOutput(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	app := newApp()
	defer app.DisconnectAllSessions()

	const sid = "local-agent-test-1"
	if _, err := app.StartLocalTerminal(sid, "cmd", t.TempDir(), 90, 28); err != nil {
		t.Fatalf("StartLocalTerminal(cmd) failed: %v", err)
	}

	// 1. Default trusted policy allows read-only local shell_exec and redacts secrets in stdout.
	secretToken := "sk-live-abcdef1234567890abcdef1234567890"
	out := app.CallTool(context.Background(), "builtin-ai", "Shell AI Agent", "shell_exec", map[string]any{
		"sessionId": sid,
		"command":   "echo LOCAL_OK token=" + secretToken,
	})
	if out.IsError {
		t.Fatalf("local shell_exec failed: %s", out.Text)
	}
	var res struct {
		SessionID     string `json:"sessionId"`
		ExitCode      int    `json:"exitCode"`
		Stdout        string `json:"stdout"`
		Stderr        string `json:"stderr"`
		RedactedCount int    `json:"redactedCount"`
	}
	if err := json.Unmarshal([]byte(out.Text), &res); err != nil {
		t.Fatalf("invalid shell_exec json: %v (%s)", err, out.Text)
	}
	if res.SessionID != sid || res.ExitCode != 0 || !strings.Contains(res.Stdout, "LOCAL_OK") {
		t.Fatalf("unexpected local shell_exec result: %+v", res)
	}
	if strings.Contains(res.Stdout, secretToken) || res.RedactedCount == 0 {
		t.Fatalf("expected secret in local shell_exec stdout to be redacted, got %+v", res)
	}

	// 2. Policy enforcement: readonly mode blocks mutating local commands and DenyPatterns block matches.
	roCfg := local.DefaultAgentConfig()
	roCfg.Policy.Mode = security.ModeReadOnly
	roCfg.Policy.DenyPatterns = []string{"whoami*"}
	app.setCachedAgentConfig(roCfg)

	deniedMut := app.CallTool(context.Background(), "builtin-ai", "Shell AI Agent", "shell_exec", map[string]any{
		"sessionId": sid,
		"command":   "mkdir forbidden_dir_test",
	})
	if !deniedMut.IsError || deniedMut.Decision != "policy-deny" {
		t.Fatalf("expected readonly mode to deny mutating local command, got %+v", deniedMut)
	}
	deniedPat := app.CallTool(context.Background(), "builtin-ai", "Shell AI Agent", "shell_exec", map[string]any{
		"sessionId": sid,
		"command":   "whoami",
	})
	if !deniedPat.IsError || deniedPat.Decision != "policy-deny" {
		t.Fatalf("expected DenyPatterns to block local command, got %+v", deniedPat)
	}
	deniedSend := app.CallTool(context.Background(), "builtin-ai", "Shell AI Agent", "shell_send_terminal", map[string]any{
		"sessionId": sid,
		"command":   "mkdir blocked_pty_dir",
	})
	if !deniedSend.IsError || deniedSend.Decision != "policy-deny" {
		t.Fatalf("expected readonly mode to deny mutating shell_send_terminal, got %+v", deniedSend)
	}

	// 3. Allowed shell_send_terminal writes to ConPTY with \r and shell_read_terminal reads redacted ring output.
	allowCfg := local.DefaultAgentConfig()
	app.setCachedAgentConfig(allowCfg)

	sent := app.CallTool(context.Background(), "builtin-ai", "Shell AI Agent", "shell_send_terminal", map[string]any{
		"sessionId": sid,
		"command":   "echo RING_READ_OK_42 key=" + secretToken,
		"enter":     true,
	})
	if sent.IsError {
		t.Fatalf("local shell_send_terminal failed: %s", sent.Text)
	}

	deadline := time.Now().Add(6 * time.Second)
	var readOut string
	var readRedacted int
	for time.Now().Before(deadline) {
		r := app.CallTool(context.Background(), "builtin-ai", "Shell AI Agent", "shell_read_terminal", map[string]any{
			"sessionId": sid,
			"lines":     50,
		})
		if !r.IsError && strings.Contains(r.Text, "RING_READ_OK_42") {
			readOut = r.Text
			readRedacted = r.RedactedCount
			break
		}
		time.Sleep(60 * time.Millisecond)
	}
	if !strings.Contains(readOut, "RING_READ_OK_42") {
		t.Fatalf("shell_read_terminal did not observe local PTY output; last=%q", readOut)
	}
	if strings.Contains(readOut, secretToken) || readRedacted == 0 {
		t.Fatalf("expected secret in local PTY output to be redacted; out=%q redacted=%d", readOut, readRedacted)
	}
}

func TestRunAIAgentMultiTurnHistoryRedactionAndLocalTools(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	app := newApp()
	defer app.DisconnectAllSessions()

	const sid = "local-agent-e2e"
	if _, err := app.StartLocalTerminal(sid, "cmd", t.TempDir(), 90, 28); err != nil {
		t.Fatalf("StartLocalTerminal failed: %v", err)
	}

	secretInGoal := "ghp_1234567890abcdefghijklmnopqrstuvwxyz1234"
	var turn int
	var seenToolNames []string
	var seenHistoryOK bool
	var goalRedacted bool
	var toolExecutedOK bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request failed: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if turn == 1 {
			for _, tl := range req.Tools {
				seenToolNames = append(seenToolNames, tl.Function.Name)
			}
			// Expect: system, prior user, prior assistant, current user
			if len(req.Messages) == 4 &&
				req.Messages[1].Role == "user" && strings.Contains(req.Messages[1].Content, "上次查看了什么") &&
				req.Messages[2].Role == "assistant" && strings.Contains(req.Messages[2].Content, "上次查看了磁盘") {
				seenHistoryOK = true
			}
			lastUser := req.Messages[len(req.Messages)-1].Content
			if !strings.Contains(lastUser, secretInGoal) && strings.Contains(lastUser, "[REDACTED:") {
				goalRedacted = true
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"shell_exec","arguments":"{\"command\":\"echo LOCAL_AGENT_LOOP_OK\"}"}}]}}]}`)
			return
		}
		lastMsg := req.Messages[len(req.Messages)-1]
		if lastMsg.Role == "tool" && strings.Contains(lastMsg.Content, "LOCAL_AGENT_LOOP_OK") {
			toolExecutedOK = true
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"已在当前本机终端完成验证。"}}]}`)
	}))
	defer srv.Close()

	if err := local.SaveAI(local.AISettings{
		Mode:     "api",
		Endpoint: srv.URL + "/v1",
		Model:    "demo-agent",
		APIKey:   "test-key",
	}); err != nil {
		t.Fatal(err)
	}

	history := []local.AgentTurn{
		{Role: "user", Content: "上次查看了什么？"},
		{Role: "assistant", Content: "上次查看了磁盘占用。"},
	}
	got, err := app.RunAIAgent(sid, "检查当前终端并忽略这个令牌 "+secretInGoal, history)
	if err != nil {
		t.Fatalf("RunAIAgent failed: %v", err)
	}
	if !strings.Contains(got, "本机终端完成验证") {
		t.Fatalf("unexpected RunAIAgent response: %q", got)
	}
	if !seenHistoryOK {
		t.Fatal("expected prior multi-turn history to be passed to model")
	}
	if !goalRedacted {
		t.Fatal("expected secret in goal to be redacted before sending to model")
	}
	if !toolExecutedOK {
		t.Fatal("expected local shell_exec tool output to reach turn 2")
	}
	for _, forbidden := range []string{"shell_sftp_read", "shell_sftp_write", "shell_sftp_list", "shell_server_stats", "shell_tunnels"} {
		for _, name := range seenToolNames {
			if name == forbidden {
				t.Fatalf("SSH-only tool %q should not be exposed for local terminal session; got %v", forbidden, seenToolNames)
			}
		}
	}
}
