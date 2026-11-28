package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ToolDefinition describes an OpenAI Chat Completions function tool for Built-in AI Agent mode.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// AgentTurn represents a prior user or assistant turn in a multi-turn AI drawer session.
type AgentTurn struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// AgentStepEvent reports live ReAct loop progress back to the Shell AI Drawer UI.
type AgentStepEvent struct {
	Step          int    `json:"step"`
	Kind          string `json:"kind"` // "thought", "tool_call", "tool_result", "final", "error"
	Tool          string `json:"tool,omitempty"`
	Arguments     string `json:"arguments,omitempty"`
	Output        string `json:"output,omitempty"`
	Risk          string `json:"risk,omitempty"`
	Decision      string `json:"decision,omitempty"`
	RedactedCount int    `json:"redactedCount,omitempty"`
	Error         string `json:"error,omitempty"`
}

// ToolExecutor runs a single tool call through Shell's unified permission, approval, redaction, and audit pipeline.
type ToolExecutor func(ctx context.Context, toolName string, args map[string]any) (output string, risk string, decision string, redacted int, err error)

type chatToolMsg struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func trimAgentHistory(history []AgentTurn, maxTurns, maxBytes int) []AgentTurn {
	if len(history) == 0 || maxTurns <= 0 {
		return nil
	}
	valid := make([]AgentTurn, 0, len(history))
	for _, h := range history {
		role := strings.ToLower(strings.TrimSpace(h.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(h.Content)
		if content == "" {
			continue
		}
		if maxBytes > 0 && len(content) > maxBytes {
			content = truncateUTF8(content, maxBytes) + "…"
		}
		valid = append(valid, AgentTurn{Role: role, Content: content})
	}
	if len(valid) > maxTurns {
		valid = valid[len(valid)-maxTurns:]
	}
	return valid
}

func buildAgentSystemPrompt(hostLabel string) string {
	sysPrompt := "你是运行在 Shell 终端客户端内的直连式运维与排障助手。" +
		"用户期望你直接动手完成排查或任务，不要让用户自己去复制粘贴命令执行。" +
		"只读排查请尽量合并为 1 条简洁命令一次查清，减少无谓轮次，快速给出结论。" +
		"如果用户问的是纯知识问答或与主机无关的问题，无需调用工具，直接简洁回答。" +
		"所有工具输出都已自动脱敏（[REDACTED:...]），切勿尝试绕过安全策略；修改或高危操作会自动弹出确认，变更前请明确最小影响范围。" +
		"当目标是本机 Windows 终端时，请严格使用对应本机 Shell 语法（PowerShell/CMD/Git Bash/WSL）执行。" +
		"最终总结简洁直击重点（中文，必要时仅给简短关键数据或代码块，不要长篇大论）。"
	if hostLabel != "" {
		sysPrompt += "\n当前活动目标：" + hostLabel
	} else {
		sysPrompt += "\n当前未连接活动会话。如果是纯问答请直接回答；若用户指定了已保存主机，可调用 shell_list_hosts 查询或直接传 host 给 shell_exec 自动连接执行。"
	}
	return sysPrompt
}

// RunAgentLoop executes an AI Agent loop using the user's configured API (OpenAI-compatible or native Anthropic Messages) or local CLI.
// Every tool call passes through Shell's unified PolicyEngine + Redaction Engine + Approval Dialog + Audit Log.
func RunAgentLoop(
	ctx context.Context,
	goal string,
	hostLabel string,
	history []AgentTurn,
	tools []ToolDefinition,
	execTool ToolExecutor,
	onStep func(AgentStepEvent),
) (string, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "", errors.New("请输入要交给 Agent 完成的任务")
	}
	s, err := LoadAI()
	if err != nil {
		return "", err
	}
	if s.IsCLI() {
		return runCLIAgentLoop(ctx, s, goal, hostLabel, history, tools, execTool, onStep)
	}
	endpoint, err := completionURL(s)
	if err != nil {
		return "", err
	}

	sysPrompt := buildAgentSystemPrompt(hostLabel)

	if isAnthropicProtocol(endpoint) {
		return runAnthropicAgentLoop(ctx, s, endpoint, sysPrompt, goal, history, tools, execTool, onStep)
	}

	openAITools := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		openAITools = append(openAITools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}

	prior := trimAgentHistory(history, 6, 2400)
	msgs := make([]chatToolMsg, 0, 2+len(prior))
	msgs = append(msgs, chatToolMsg{Role: "system", Content: sysPrompt})
	for _, h := range prior {
		msgs = append(msgs, chatToolMsg{Role: h.Role, Content: h.Content})
	}
	msgs = append(msgs, chatToolMsg{Role: "user", Content: goal})

	const maxSteps = 8
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", networkError(ctx, err)
		}
		payload := map[string]any{
			"model":    s.Model,
			"messages": msgs,
		}
		if len(openAITools) > 0 {
			payload["tools"] = openAITools
			payload["tool_choice"] = "auto"
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if s.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+s.APIKey)
		}
		resp, err := aiClient.Do(req)
		if err != nil {
			return "", networkError(ctx, err)
		}
		rawResp, readErr := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
		resp.Body.Close()
		if readErr != nil {
			return "", networkError(ctx, readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", formatAPIStatusError(resp.StatusCode, rawResp, s.APIKey, true)
		}

		var parsed struct {
			Choices []struct {
				Message struct {
					Role      string          `json:"role"`
					Content   json.RawMessage `json:"content"`
					ToolCalls []chatToolCall  `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(rawResp, &parsed) != nil || len(parsed.Choices) == 0 {
			return "", errors.New("未收到有效的模型响应")
		}
		assistantMsg := parsed.Choices[0].Message
		contentText := extractContentText(assistantMsg.Content)
		cleanText := stripThinkBlocks(contentText)

		if len(assistantMsg.ToolCalls) == 0 {
			finalText := cleanText
			if finalText == "" {
				finalText = strings.TrimSpace(contentText)
			}
			if finalText == "" {
				finalText = "Agent 已完成执行。"
			}
			if onStep != nil {
				onStep(AgentStepEvent{Step: step, Kind: "final", Output: finalText})
			}
			return finalText, nil
		}

		if cleanText != "" && onStep != nil {
			onStep(AgentStepEvent{Step: step, Kind: "thought", Output: cleanText})
		}
		msgs = append(msgs, chatToolMsg{
			Role:      "assistant",
			Content:   contentText,
			ToolCalls: assistantMsg.ToolCalls,
		})

		for _, tc := range assistantMsg.ToolCalls {
			args := map[string]any{}
			if strings.TrimSpace(tc.Function.Arguments) != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			if onStep != nil {
				onStep(AgentStepEvent{
					Step:      step,
					Kind:      "tool_call",
					Tool:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
			out, risk, decision, redacted, toolErr := execTool(ctx, tc.Function.Name, args)
			if toolErr != nil {
				errMsg := "工具执行失败或被拒绝：" + toolErr.Error()
				if onStep != nil {
					onStep(AgentStepEvent{
						Step:     step,
						Kind:     "tool_result",
						Tool:     tc.Function.Name,
						Risk:     risk,
						Decision: decision,
						Error:    toolErr.Error(),
					})
				}
				msgs = append(msgs, chatToolMsg{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    errMsg,
				})
				continue
			}
			if onStep != nil {
				preview := out
				if len(preview) > 1200 {
					preview = truncateUTF8(preview, 1200) + "\n…(已截断预览)"
				}
				onStep(AgentStepEvent{
					Step:          step,
					Kind:          "tool_result",
					Tool:          tc.Function.Name,
					Output:        preview,
					Risk:          risk,
					Decision:      decision,
					RedactedCount: redacted,
				})
			}
			// Keep context window compact
			if len(out) > 16000 {
				out = truncateUTF8(out, 16000) + "\n...[output truncated]"
			}
			msgs = append(msgs, chatToolMsg{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    out,
			})
		}
	}
	return "已达到单次最大执行步数（8 步），如需继续请补充指令。", nil
}

type anthropicAgentMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func runAnthropicAgentLoop(
	ctx context.Context,
	s AISettings,
	endpoint string,
	sysPrompt string,
	goal string,
	history []AgentTurn,
	tools []ToolDefinition,
	execTool ToolExecutor,
	onStep func(AgentStepEvent),
) (string, error) {
	anthropicTools := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		schema := map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
		if len(t.Parameters) > 0 {
			schema = make(map[string]any, len(t.Parameters)+1)
			for k, v := range t.Parameters {
				schema[k] = v
			}
			if _, ok := schema["type"]; !ok {
				schema["type"] = "object"
			}
		}
		anthropicTools = append(anthropicTools, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": schema,
		})
	}

	// Build strictly alternating user/assistant messages starting with user.
	prior := trimAgentHistory(history, 6, 2400)
	msgs := make([]anthropicAgentMsg, 0, len(prior)+1)
	for _, h := range prior {
		if len(msgs) == 0 && h.Role != "user" {
			continue
		}
		if len(msgs) > 0 && msgs[len(msgs)-1].Role == h.Role {
			if prev, ok := msgs[len(msgs)-1].Content.(string); ok {
				msgs[len(msgs)-1].Content = prev + "\n\n" + h.Content
				continue
			}
		}
		msgs = append(msgs, anthropicAgentMsg{Role: h.Role, Content: h.Content})
	}
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == "user" {
		if prev, ok := msgs[len(msgs)-1].Content.(string); ok {
			msgs[len(msgs)-1].Content = prev + "\n\n" + goal
		} else {
			msgs = append(msgs, anthropicAgentMsg{Role: "user", Content: goal})
		}
	} else {
		msgs = append(msgs, anthropicAgentMsg{Role: "user", Content: goal})
	}

	const maxSteps = 8
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", networkError(ctx, err)
		}
		payload := map[string]any{
			"model":      s.Model,
			"max_tokens": 4096,
			"system":     sysPrompt,
			"messages":   msgs,
		}
		if len(anthropicTools) > 0 {
			payload["tools"] = anthropicTools
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("anthropic-version", anthropicAPIVersion)
		if s.APIKey != "" {
			req.Header.Set("x-api-key", s.APIKey)
		}
		resp, err := aiClient.Do(req)
		if err != nil {
			return "", networkError(ctx, err)
		}
		rawResp, readErr := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
		resp.Body.Close()
		if readErr != nil {
			return "", networkError(ctx, readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", formatAPIStatusError(resp.StatusCode, rawResp, s.APIKey, true)
		}

		var parsed struct {
			Type       string            `json:"type"`
			StopReason string            `json:"stop_reason"`
			Content    []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(rawResp, &parsed) != nil || len(parsed.Content) == 0 {
			return "", errors.New("未收到有效的模型响应")
		}

		type anthropicBlock struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		var textParts []string
		var toolUses []anthropicBlock
		for _, rawBlock := range parsed.Content {
			var blk anthropicBlock
			if json.Unmarshal(rawBlock, &blk) != nil {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(blk.Type)) {
			case "text":
				if blk.Text != "" {
					textParts = append(textParts, blk.Text)
				}
			case "tool_use":
				if strings.TrimSpace(blk.Name) != "" {
					toolUses = append(toolUses, blk)
				}
			}
		}

		joinedText := strings.Join(textParts, "\n")
		cleanText := stripThinkBlocks(joinedText)
		if len(toolUses) == 0 {
			finalText := cleanText
			if finalText == "" {
				finalText = strings.TrimSpace(joinedText)
			}
			if finalText == "" {
				finalText = "Agent 已完成执行。"
			}
			if onStep != nil {
				onStep(AgentStepEvent{Step: step, Kind: "final", Output: finalText})
			}
			return finalText, nil
		}

		if cleanText != "" && onStep != nil {
			onStep(AgentStepEvent{Step: step, Kind: "thought", Output: cleanText})
		}
		msgs = append(msgs, anthropicAgentMsg{
			Role:    "assistant",
			Content: parsed.Content,
		})

		toolResults := make([]map[string]any, 0, len(toolUses))
		for _, tu := range toolUses {
			args := tu.Input
			if args == nil {
				args = map[string]any{}
			}
			argBytes, _ := json.Marshal(args)
			if onStep != nil {
				onStep(AgentStepEvent{
					Step:      step,
					Kind:      "tool_call",
					Tool:      tu.Name,
					Arguments: string(argBytes),
				})
			}
			out, risk, decision, redacted, toolErr := execTool(ctx, tu.Name, args)
			if toolErr != nil {
				errMsg := "工具执行失败或被拒绝：" + toolErr.Error()
				if onStep != nil {
					onStep(AgentStepEvent{
						Step:     step,
						Kind:     "tool_result",
						Tool:     tu.Name,
						Risk:     risk,
						Decision: decision,
						Error:    toolErr.Error(),
					})
				}
				toolResults = append(toolResults, map[string]any{
					"type":        "tool_result",
					"tool_use_id": tu.ID,
					"content":     errMsg,
					"is_error":    true,
				})
				continue
			}
			if onStep != nil {
				preview := out
				if len(preview) > 1200 {
					preview = truncateUTF8(preview, 1200) + "\n…(已截断预览)"
				}
				onStep(AgentStepEvent{
					Step:          step,
					Kind:          "tool_result",
					Tool:          tu.Name,
					Output:        preview,
					Risk:          risk,
					Decision:      decision,
					RedactedCount: redacted,
				})
			}
			if len(out) > 16000 {
				out = truncateUTF8(out, 16000) + "\n...[output truncated]"
			}
			toolResults = append(toolResults, map[string]any{
				"type":        "tool_result",
				"tool_use_id": tu.ID,
				"content":     out,
			})
		}
		msgs = append(msgs, anthropicAgentMsg{
			Role:    "user",
			Content: toolResults,
		})
	}
	return "已达到单次最大执行步数（8 步），如需继续请补充指令。", nil
}

func runCLIAgentLoop(
	ctx context.Context,
	s AISettings,
	goal string,
	hostLabel string,
	history []AgentTurn,
	tools []ToolDefinition,
	execTool ToolExecutor,
	onStep func(AgentStepEvent),
) (string, error) {
	var toolSummary strings.Builder
	for _, t := range tools {
		params, _ := json.Marshal(t.Parameters)
		fmt.Fprintf(&toolSummary, "- %s: %s | params=%s\n", t.Name, t.Description, string(params))
	}
	sysPrompt := buildAgentSystemPrompt(hostLabel) +
		"\n每轮必须且仅输出一个合法 JSON 对象，格式二选一：\n" +
		"1) 调用工具：{\"thought\":\"简短思路\",\"tool\":\"工具名\",\"arguments\":{...}}\n" +
		"2) 完成总结：{\"final\":\"简洁清晰的中文总结\"}\n" +
		"可用工具：\n" + toolSummary.String()

	var transcript strings.Builder
	if prior := trimAgentHistory(history, 4, 800); len(prior) > 0 {
		transcript.WriteString("近期对话上下文：\n")
		for _, h := range prior {
			label := "用户"
			if h.Role == "assistant" {
				label = "助手"
			}
			fmt.Fprintf(&transcript, "%s：%s\n", label, h.Content)
		}
		transcript.WriteString("\n")
	}
	fmt.Fprintf(&transcript, "用户任务：%s\n", goal)

	const maxSteps = 8
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", networkError(ctx, err)
		}
		raw, err := runLocalAICLI(ctx, s, sysPrompt, transcript.String()+"\n请输出下一步 JSON：", nil)
		if err != nil {
			return "", err
		}
		type cliAgentAction struct {
			Thought   string         `json:"thought"`
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
			Final     string         `json:"final"`
		}
		cleaned := stripThinkBlocks(raw)
		if cleaned == "" {
			cleaned = strings.TrimSpace(raw)
		}
		var act cliAgentAction
		parsedOK := false
		for _, candidate := range extractBalancedJSONObjects(cleaned) {
			var probe cliAgentAction
			if json.Unmarshal([]byte(candidate), &probe) == nil && (strings.TrimSpace(probe.Tool) != "" || strings.TrimSpace(probe.Final) != "") {
				act = probe
				parsedOK = true
				break
			}
		}
		if !parsedOK {
			if onStep != nil {
				onStep(AgentStepEvent{Step: step, Kind: "final", Output: cleaned})
			}
			return cleaned, nil
		}
		if strings.TrimSpace(act.Final) != "" && strings.TrimSpace(act.Tool) == "" {
			finalText := strings.TrimSpace(act.Final)
			if onStep != nil {
				onStep(AgentStepEvent{Step: step, Kind: "final", Output: finalText})
			}
			return finalText, nil
		}
		if strings.TrimSpace(act.Thought) != "" && onStep != nil {
			onStep(AgentStepEvent{Step: step, Kind: "thought", Output: strings.TrimSpace(act.Thought)})
		}
		if act.Arguments == nil {
			act.Arguments = map[string]any{}
		}
		argBytes, _ := json.Marshal(act.Arguments)
		if onStep != nil {
			onStep(AgentStepEvent{
				Step:      step,
				Kind:      "tool_call",
				Tool:      act.Tool,
				Arguments: string(argBytes),
			})
		}
		out, risk, decision, redacted, toolErr := execTool(ctx, act.Tool, act.Arguments)
		if toolErr != nil {
			if onStep != nil {
				onStep(AgentStepEvent{
					Step:     step,
					Kind:     "tool_result",
					Tool:     act.Tool,
					Risk:     risk,
					Decision: decision,
					Error:    toolErr.Error(),
				})
			}
			fmt.Fprintf(&transcript, "\n[步骤 %d] 调用 %s(%s) 失败：%s\n", step, act.Tool, string(argBytes), toolErr.Error())
			continue
		}
		if onStep != nil {
			preview := out
			if len(preview) > 1200 {
				preview = truncateUTF8(preview, 1200) + "\n…(已截断预览)"
			}
			onStep(AgentStepEvent{
				Step:          step,
				Kind:          "tool_result",
				Tool:          act.Tool,
				Output:        preview,
				Risk:          risk,
				Decision:      decision,
				RedactedCount: redacted,
			})
		}
		if len(out) > 12000 {
			out = truncateUTF8(out, 12000) + "\n...[output truncated]"
		}
		fmt.Fprintf(&transcript, "\n[步骤 %d] 调用 %s(%s) 返回：\n%s\n", step, act.Tool, string(argBytes), out)
	}
	return "已达到单次最大执行步数（8 步），如需继续请补充指令。", nil
}
