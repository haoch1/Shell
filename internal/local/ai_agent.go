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

// RunAgentLoop executes an OpenAI-compatible Function Calling Agent loop using the user's configured API.
// Every tool call passes through Shell's unified PolicyEngine + Redaction Engine + Approval Dialog + Audit Log.
func RunAgentLoop(
	ctx context.Context,
	goal string,
	hostLabel string,
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
		return runCLIAgentLoop(ctx, s, goal, hostLabel, tools, execTool, onStep)
	}
	endpoint, err := completionURL(s)
	if err != nil {
		return "", err
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

	sysPrompt := "你是运行在 Shell SSH 客户端内的资深 Linux/运维 Agent。" +
		"你可以通过提供的工具检查当前远端主机、执行命令、读取/编辑文件与查看状态。" +
		"所有工具输出都已自动进行敏感信息脱敏（[REDACTED:...]），切勿尝试绕过安全策略。" +
		"优先使用只读命令排查问题，修改操作前要明确最小影响范围；完成任务后用简洁清晰的中文总结结果。"
	if hostLabel != "" {
		sysPrompt += " 当前活动会话主机：" + hostLabel
	}

	msgs := []chatToolMsg{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: goal},
	}

	const maxSteps = 8
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", networkError(ctx, err)
		}
		payload := map[string]any{
			"model":       s.Model,
			"messages":    msgs,
			"tools":       openAITools,
			"tool_choice": "auto",
		}
		if isAnthropicEndpoint(endpoint) {
			payload["max_tokens"] = 4096
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
		if isAnthropicEndpoint(endpoint) {
			req.Header.Set("anthropic-version", "2023-06-01")
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
			return "", fmt.Errorf("AI 服务返回 HTTP %d，请检查模型是否支持工具调用 (Tool Calling)", resp.StatusCode)
		}

		var parsed struct {
			Choices []struct {
				Message struct {
					Role      string         `json:"role"`
					Content   string         `json:"content"`
					ToolCalls []chatToolCall `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(rawResp, &parsed) != nil || len(parsed.Choices) == 0 {
			return "", errors.New("未收到有效的模型响应")
		}
		assistantMsg := parsed.Choices[0].Message

		if len(assistantMsg.ToolCalls) == 0 {
			finalText := strings.TrimSpace(assistantMsg.Content)
			if finalText == "" {
				finalText = "Agent 已完成执行。"
			}
			if onStep != nil {
				onStep(AgentStepEvent{Step: step, Kind: "final", Output: finalText})
			}
			return finalText, nil
		}

		if strings.TrimSpace(assistantMsg.Content) != "" && onStep != nil {
			onStep(AgentStepEvent{Step: step, Kind: "thought", Output: strings.TrimSpace(assistantMsg.Content)})
		}
		msgs = append(msgs, chatToolMsg{
			Role:      "assistant",
			Content:   assistantMsg.Content,
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

func runCLIAgentLoop(
	ctx context.Context,
	s AISettings,
	goal string,
	hostLabel string,
	tools []ToolDefinition,
	execTool ToolExecutor,
	onStep func(AgentStepEvent),
) (string, error) {
	var toolSummary strings.Builder
	for _, t := range tools {
		params, _ := json.Marshal(t.Parameters)
		fmt.Fprintf(&toolSummary, "- %s: %s | params=%s\n", t.Name, t.Description, string(params))
	}
	sysPrompt := "你是运行在 Shell SSH 客户端内的资深 Linux/运维 Agent。" +
		"所有工具输出已自动脱敏（[REDACTED:...]），切勿绕过安全策略。" +
		"每轮必须且仅输出一个合法 JSON 对象，格式二选一：\n" +
		"1) 调用工具：{\"thought\":\"简短思路\",\"tool\":\"工具名\",\"arguments\":{...}}\n" +
		"2) 完成总结：{\"final\":\"简洁清晰的中文总结\"}\n" +
		"可用工具：\n" + toolSummary.String()
	if hostLabel != "" {
		sysPrompt += "\n当前活动会话主机：" + hostLabel
	}

	var transcript strings.Builder
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
		var act struct {
			Thought   string         `json:"thought"`
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
			Final     string         `json:"final"`
		}
		trimmed := strings.TrimSpace(raw)
		parsedOK := false
		if start := strings.IndexByte(trimmed, '{'); start >= 0 {
			if end := strings.LastIndexByte(trimmed, '}'); end > start {
				if json.Unmarshal([]byte(trimmed[start:end+1]), &act) == nil && (act.Tool != "" || act.Final != "") {
					parsedOK = true
				}
			}
		}
		if !parsedOK {
			if onStep != nil {
				onStep(AgentStepEvent{Step: step, Kind: "final", Output: trimmed})
			}
			return trimmed, nil
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
