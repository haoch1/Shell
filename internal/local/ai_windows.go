package local

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type AISettings struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
}

// AIResult is a structured command suggestion with safety classification.
type AIResult struct {
	Command     string `json:"command"`
	Explanation string `json:"explanation"`
	Risk        string `json:"risk"` // "safe", "caution", "danger"
}

func LoadAI() (AISettings, error) {
	var result AISettings
	err := withStore(func(dir string) error {
		err := readProtected(filepath.Join(dir, "ai.dpapi"), &result)
		if errors.Is(err, os.ErrNotExist) {
			result.Endpoint = "http://127.0.0.1:11434/v1"
			return nil
		}
		return err
	})
	return result, err
}

func completionURL(s AISettings) (string, error) {
	u, err := url.Parse(s.Endpoint)
	if err != nil || len(s.Endpoint) > 2048 || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(s.Endpoint, "\\?#") || strings.IndexFunc(s.Endpoint, unicode.IsSpace) >= 0 {
		return "", errors.New("请填写完整的服务地址，例如 https://api.openai.com/v1")
	}
	ip, _ := netip.ParseAddr(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || ip.IsLoopback())) {
		return "", errors.New("远程 AI 服务需要 HTTPS，本机回环地址可以使用 HTTP")
	}
	if s.Model == "" || utf8.RuneCountInString(s.Model) > 256 || strings.IndexFunc(s.Model, unicode.IsControl) >= 0 {
		return "", errors.New("请在 AI 设置中填写有效的模型名称")
	}
	if len(s.APIKey) > 4096 || strings.IndexFunc(s.APIKey, func(c rune) bool { return c < 0x21 || c > 0x7e }) >= 0 {
		return "", errors.New("API 密钥格式无效，请检查空格或换行")
	}
	endpoint := strings.TrimRight(u.String(), "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	return endpoint, nil
}

func SaveAI(s AISettings) error {
	s.Endpoint, s.Model, s.APIKey = strings.TrimSpace(s.Endpoint), strings.TrimSpace(s.Model), strings.TrimSpace(s.APIKey)
	if _, err := completionURL(s); err != nil {
		return err
	}
	return withStore(func(dir string) error { return writeProtected(filepath.Join(dir, "ai.dpapi"), s) })
}

var aiClient = &http.Client{
	Timeout: 90 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func doChatRequest(ctx context.Context, s AISettings, messages []message, stream bool) (*http.Response, error) {
	endpoint, err := completionURL(s)
	if err != nil {
		return nil, err
	}
	payload := struct {
		Model    string    `json:"model"`
		Messages []message `json:"messages"`
		Stream   bool      `json:"stream,omitempty"`
	}{Model: s.Model, Messages: messages, Stream: stream}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream, application/json")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if s.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.APIKey)
	}
	resp, err := aiClient.Do(req)
	if err != nil {
		return nil, networkError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		hint := "请检查服务地址、模型名称及服务状态"
		switch resp.StatusCode {
		case 401, 403:
			hint = "请检查密钥及模型访问权限"
		case 429:
			hint = "请检查配额或稍后重试"
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			hint = "请在设置中填写重定向后的最终服务地址"
		}
		return nil, fmt.Errorf("AI 服务返回 HTTP %d，%s", resp.StatusCode, hint)
	}
	return resp, nil
}

// Suggest keeps the simple single-command API (and passes unit tests).
func Suggest(ctx context.Context, request string) (string, error) {
	request = strings.TrimSpace(request)
	if request == "" || utf8.RuneCountInString(request) > 4000 {
		return "", errors.New("请输入需求，最多 4000 个字符")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s, err := LoadAI()
	if err != nil {
		return "", err
	}
	resp, err := doChatRequest(ctx, s, []message{
		{"system", "你是 SSH 终端的命令助手。按用户指定的系统建议一条命令，未指定时使用 POSIX shell。仅输出一条单行命令，不附加解释、Markdown 或控制字符。"},
		{"user", request},
	}, false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	const limit = 128 << 10
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", networkError(ctx, err)
	}
	if len(data) > limit {
		return "", errors.New("AI 回复超过 128 KiB，请缩短问题后重试")
	}
	return readCommand(data)
}

var (
	dangerPattern  = regexp.MustCompile(`(?i)(rm\s+-[a-z]*r[a-z]*f|rm\s+-[a-z]*f[a-z]*r|mkfs|dd\s+if=|:\(\)\s*\{|chmod\s+-R\s+777|>\s*/dev/sd|shutdown|reboot|poweroff|init\s+0|iptables\s+-F|drop\s+database)`)
	cautionPattern = regexp.MustCompile(`(?i)(\bsudo\b|\brm\b|\bkill\b|\bpkill\b|\bsystemctl\s+(stop|restart|disable)|\bdocker\s+(rm|rmi|stop|prune)|\bchmod\b|\bchown\b|\bmv\b|\bufw\b|\biptables\b)`)
)

func classifyRisk(cmd string) string {
	if dangerPattern.MatchString(cmd) {
		return "danger"
	}
	if cautionPattern.MatchString(cmd) {
		return "caution"
	}
	return "safe"
}

func readStreamOrJSON(ctx context.Context, resp *http.Response, onDelta func(string)) (string, error) {
	const limit = 256 << 10
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/event-stream") {
		var full strings.Builder
		scanner := bufio.NewScanner(io.LimitReader(resp.Body, limit))
		scanner.Buffer(make([]byte, 0, 16<<10), 128<<10)
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return "", networkError(ctx, err)
			}
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(payload), &chunk) != nil || len(chunk.Choices) == 0 {
				continue
			}
			delta := chunk.Choices[0].Delta.Content
			if delta == "" {
				delta = chunk.Choices[0].Message.Content
			}
			if delta != "" {
				full.WriteString(delta)
				if onDelta != nil {
					onDelta(delta)
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return "", networkError(ctx, err)
		}
		return full.String(), nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", networkError(ctx, err)
	}
	if len(data) > limit {
		return "", errors.New("AI 回复超过 256 KiB，请缩短问题后重试")
	}
	var result struct {
		Choices []struct {
			Message message `json:"message"`
			Finish  string  `json:"finish_reason"`
		} `json:"choices"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &result) != nil || len(result.Choices) == 0 {
		return "", errors.New("未收到有效的 AI 回复，请确认服务兼容 Chat Completions")
	}
	content := result.Choices[0].Message.Content
	if onDelta != nil && content != "" {
		onDelta(content)
	}
	return content, nil
}

// SuggestStructured returns a command, short Chinese explanation, and risk level, streaming progress via onDelta.
func SuggestStructured(ctx context.Context, request, osHint string, onDelta func(string)) (AIResult, error) {
	request = strings.TrimSpace(request)
	if request == "" || utf8.RuneCountInString(request) > 4000 {
		return AIResult{}, errors.New("请输入需求，最多 4000 个字符")
	}
	s, err := LoadAI()
	if err != nil {
		return AIResult{}, err
	}
	sys := "你是专业的 SSH 运维命令助手。根据用户需求输出 JSON 对象，格式严格为：{\"command\":\"单行可执行的POSIX命令\",\"explanation\":\"简洁中文说明（1-2句）\",\"risk\":\"safe或caution或danger\"}。不要输出多余文字或 Markdown 代码块。command 不得包含换行或控制字符。"
	if osHint != "" {
		sys += " 目标系统参考：" + osHint
	}
	resp, err := doChatRequest(ctx, s, []message{
		{"system", sys},
		{"user", request},
	}, true)
	if err != nil {
		return AIResult{}, err
	}
	defer resp.Body.Close()
	raw, err := readStreamOrJSON(ctx, resp, onDelta)
	if err != nil {
		return AIResult{}, err
	}
	return parseStructuredResult(raw)
}

func parseStructuredResult(raw string) (AIResult, error) {
	trimmed := strings.TrimSpace(raw)
	const fence = "```"
	if strings.HasPrefix(trimmed, fence) && strings.HasSuffix(trimmed, fence) {
		if _, rest, ok := strings.Cut(trimmed, "\n"); ok {
			trimmed = strings.TrimSpace(strings.TrimSuffix(rest, fence))
		}
	}
	// Try extracting JSON object.
	if start := strings.IndexByte(trimmed, '{'); start >= 0 {
		if end := strings.LastIndexByte(trimmed, '}'); end > start {
			var parsed AIResult
			if json.Unmarshal([]byte(trimmed[start:end+1]), &parsed) == nil && strings.TrimSpace(parsed.Command) != "" {
				parsed.Command = sanitizeSingleLineCommand(parsed.Command)
				if parsed.Command == "" {
					return AIResult{}, errors.New("AI 返回的命令包含非法控制字符")
				}
				parsed.Explanation = strings.TrimSpace(parsed.Explanation)
				parsed.Risk = mergeRisk(parsed.Risk, classifyRisk(parsed.Command))
				return parsed, nil
			}
		}
	}
	// Fallback: treat first non-empty line as command.
	for _, line := range strings.Split(trimmed, "\n") {
		c := sanitizeSingleLineCommand(strings.Trim(line, "` "))
		if c != "" {
			return AIResult{
				Command:     c,
				Explanation: "由 AI 生成的命令建议，请在执行前确认参数。",
				Risk:        classifyRisk(c),
			}, nil
		}
	}
	return AIResult{}, errors.New("未能从 AI 回复中解析出有效命令，请重试")
}

func mergeRisk(modelRisk, heuristic string) string {
	rank := map[string]int{"safe": 0, "caution": 1, "danger": 2}
	m := strings.ToLower(strings.TrimSpace(modelRisk))
	if rank[heuristic] >= rank[m] {
		return heuristic
	}
	if _, ok := rank[m]; ok {
		return m
	}
	return heuristic
}

func sanitizeSingleLineCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || utf8.RuneCountInString(cmd) > 4096 {
		return ""
	}
	if strings.IndexFunc(cmd, func(c rune) bool {
		return unicode.IsControl(c) || unicode.Is(unicode.Bidi_Control, c) || c == '\u2028' || c == '\u2029'
	}) >= 0 {
		return ""
	}
	return cmd
}

// ExplainStream analyzes selected terminal output or an error message and streams concise Chinese troubleshooting advice.
func ExplainStream(ctx context.Context, selectedText, question string, onDelta func(string)) (string, error) {
	selectedText = strings.TrimSpace(selectedText)
	question = strings.TrimSpace(question)
	if selectedText == "" && question == "" {
		return "", errors.New("请提供需要分析的终端内容或问题")
	}
	if utf8.RuneCountInString(selectedText) > 8000 {
		runes := []rune(selectedText)
		selectedText = string(runes[len(runes)-8000:])
	}
	s, err := LoadAI()
	if err != nil {
		return "", err
	}
	prompt := question
	if selectedText != "" {
		if prompt != "" {
			prompt += "\n\n终端输出片段：\n" + selectedText
		} else {
			prompt = "请分析以下终端输出/错误，给出原因和解决命令：\n" + selectedText
		}
	}
	resp, err := doChatRequest(ctx, s, []message{
		{"system", "你是资深 Linux/SSH 运维工程师。用简洁清晰的中文解释终端输出或报错原因，并给出具体可执行的排查或修复命令。"},
		{"user", prompt},
	}, true)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return readStreamOrJSON(ctx, resp, onDelta)
}

// TestAI verifies connectivity to the configured Chat Completions endpoint.
func TestAI(ctx context.Context, s AISettings) error {
	s.Endpoint, s.Model, s.APIKey = strings.TrimSpace(s.Endpoint), strings.TrimSpace(s.Model), strings.TrimSpace(s.APIKey)
	testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := doChatRequest(testCtx, s, []message{
		{"user", "Reply with OK"},
	}, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16<<10))
	return nil
}

func networkError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("等待 AI 回复超时，请检查服务状态：%w", context.DeadlineExceeded)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("AI 请求已取消：%w", ctx.Err())
	}
	return fmt.Errorf("AI 请求失败，请检查网络、证书和服务地址：%w", err)
}

func readCommand(data []byte) (string, error) {
	var result struct {
		Choices []struct {
			Message message `json:"message"`
			Finish  string  `json:"finish_reason"`
		} `json:"choices"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &result) != nil || len(result.Choices) == 0 {
		return "", errors.New("未收到有效的 AI 回复，请确认服务兼容 Chat Completions")
	}
	choice := result.Choices[0]
	if choice.Finish == "length" || choice.Finish == "content_filter" {
		return "", errors.New("AI 回复未完成，请简化问题后重试")
	}
	command := strings.Trim(choice.Message.Content, " \r\n")
	const fence = "\x60\x60\x60"
	if strings.HasPrefix(command, fence) && strings.HasSuffix(command, "\n"+fence) {
		if _, lines, ok := strings.Cut(command, "\n"); ok {
			command = strings.Trim(strings.TrimSuffix(lines, fence), " \r\n")
		}
	}
	if command == "" || utf8.RuneCountInString(command) > 4096 || strings.IndexFunc(command, func(c rune) bool {
		return unicode.IsControl(c) || unicode.Is(unicode.Bidi_Control, c) || c == '\u2028' || c == '\u2029'
	}) >= 0 {
		return "", errors.New("AI 回复需要是一条完整的单行命令，请调整问题后重试")
	}
	return command, nil
}
