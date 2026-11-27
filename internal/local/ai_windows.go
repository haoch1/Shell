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
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type AISettings struct {
	Mode       string `json:"mode,omitempty"` // "" / "api" or "cli"
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	APIKey     string `json:"apiKey"`
	CLICommand string `json:"cliCommand,omitempty"`
	CLIArgs    string `json:"cliArgs,omitempty"`
	CLIModel   string `json:"cliModel,omitempty"`
}

// AIConfigInput carries AI configuration fields from the settings form (including optional keepKey reuse).
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
	s.Mode = strings.ToLower(strings.TrimSpace(s.Mode))
	s.Endpoint, s.Model, s.APIKey = strings.TrimSpace(s.Endpoint), strings.TrimSpace(s.Model), strings.TrimSpace(s.APIKey)
	s.CLICommand, s.CLIArgs, s.CLIModel = strings.TrimSpace(s.CLICommand), strings.TrimSpace(s.CLIArgs), strings.TrimSpace(s.CLIModel)
	if s.IsCLI() {
		if err := validateCLISettings(s); err != nil {
			return err
		}
	} else {
		if _, err := completionURL(s); err != nil {
			return err
		}
	}
	return withStore(func(dir string) error { return writeProtected(filepath.Join(dir, "ai.dpapi"), s) })
}

var aiTransport = http.DefaultTransport.(*http.Transport).Clone()

var aiClient = &http.Client{
	Timeout:   90 * time.Second,
	Transport: aiTransport,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// SetAIProxyFunc configures an outbound proxy resolver (e.g., Windows system proxy) for AI HTTP requests.
func SetAIProxyFunc(fn func(*http.Request) (*url.URL, error)) {
	if fn == nil {
		aiTransport.Proxy = http.ProxyFromEnvironment
		return
	}
	aiTransport.Proxy = fn
}

func isAnthropicEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "anthropic.com" || strings.HasSuffix(host, ".anthropic.com")
}

func isGeminiEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "generativelanguage.googleapis.com" || strings.HasSuffix(host, ".generativelanguage.googleapis.com")
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
		Model     string    `json:"model"`
		Messages  []message `json:"messages"`
		Stream    bool      `json:"stream,omitempty"`
		MaxTokens int       `json:"max_tokens,omitempty"`
	}{Model: s.Model, Messages: messages, Stream: stream}
	if isAnthropicEndpoint(endpoint) {
		payload.MaxTokens = 4096
	}
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
	if isAnthropicEndpoint(endpoint) {
		req.Header.Set("anthropic-version", "2023-06-01")
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
	const sys = "你是 SSH 终端的命令助手。按用户指定的系统建议一条命令，未指定时使用 POSIX shell。仅输出一条单行命令，不附加解释、Markdown 或控制字符。"
	if s.IsCLI() {
		raw, err := runLocalAICLI(ctx, s, sys, request, nil)
		if err != nil {
			return "", err
		}
		res, err := parseStructuredResult(raw)
		if err != nil {
			return "", err
		}
		return res.Command, nil
	}
	resp, err := doChatRequest(ctx, s, []message{
		{"system", sys},
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
		body := &io.LimitedReader{R: resp.Body, N: limit + 1}
		scanner := bufio.NewScanner(body)
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
		if body.N == 0 {
			return "", errors.New("AI 回复超过 256 KiB，请缩短问题后重试")
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
	if s.IsCLI() {
		raw, err := runLocalAICLI(ctx, s, sys, request, onDelta)
		if err != nil {
			return AIResult{}, err
		}
		return parseStructuredResult(raw)
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
	const sys = "你是资深 Linux/SSH 运维工程师。用简洁清晰的中文解释终端输出或报错原因，并给出具体可执行的排查或修复命令。"
	if s.IsCLI() {
		return runLocalAICLI(ctx, s, sys, prompt, onDelta)
	}
	resp, err := doChatRequest(ctx, s, []message{
		{"system", sys},
		{"user", prompt},
	}, true)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return readStreamOrJSON(ctx, resp, onDelta)
}

// TestAI verifies connectivity to the configured Chat Completions endpoint or local AI CLI.
func TestAI(ctx context.Context, s AISettings) error {
	s.Mode = strings.ToLower(strings.TrimSpace(s.Mode))
	s.Endpoint, s.Model, s.APIKey = strings.TrimSpace(s.Endpoint), strings.TrimSpace(s.Model), strings.TrimSpace(s.APIKey)
	s.CLICommand, s.CLIArgs, s.CLIModel = strings.TrimSpace(s.CLICommand), strings.TrimSpace(s.CLIArgs), strings.TrimSpace(s.CLIModel)
	if s.IsCLI() {
		testCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		_, err := runLocalAICLI(testCtx, s, "Reply with OK only.", "Reply with OK", nil)
		return err
	}
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

var nonChatModelSubstrings = []string{
	"embedding",
	"embed",
	"whisper",
	"tts-",
	"dall-e",
	"moderation",
	"text-moderation",
	"omni-moderation",
	"babbage",
	"davinci",
	"gpt-image",
	"imagen",
	"veo",
	"lyria",
	"aqa",
	"realtime",
	"transcribe",
	"audio-preview",
	"sora",
}

func normalizeModelsBaseURL(raw string) (*url.URL, string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(trimmed)
	if err != nil || trimmed == "" || len(trimmed) > 2048 || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(trimmed, "\\?#") || strings.IndexFunc(trimmed, unicode.IsSpace) >= 0 {
		return nil, "", errors.New("请填写完整的服务地址，例如 https://api.openai.com/v1")
	}
	ip, _ := netip.ParseAddr(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || ip.IsLoopback())) {
		return nil, "", errors.New("远程 AI 服务需要 HTTPS，本机回环地址可以使用 HTTP")
	}
	base := strings.TrimRight(u.String(), "/")
	if strings.HasSuffix(strings.ToLower(base), "/chat/completions") {
		base = base[:len(base)-len("/chat/completions")]
	}
	return u, base, nil
}

func resolveAIModelFetchKey(input AIConfigInput) (string, error) {
	key := strings.TrimSpace(input.APIKey)
	if key == "" && input.KeepKey {
		old, err := LoadAI()
		if err != nil {
			return "", err
		}
		if strings.TrimRight(strings.TrimSpace(old.Endpoint), "/") == strings.TrimRight(strings.TrimSpace(input.Endpoint), "/") {
			key = strings.TrimSpace(old.APIKey)
		}
	}
	if len(key) > 4096 || strings.IndexFunc(key, func(c rune) bool { return c < 0x21 || c > 0x7e }) >= 0 {
		return "", errors.New("API 密钥格式无效，请检查空格或换行")
	}
	return key, nil
}

func appendModelsPath(base string) string {
	if strings.HasSuffix(strings.ToLower(base), "/models") {
		return base
	}
	return base + "/models"
}

func modelFetchNetworkError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("获取模型列表超时，请检查网络或代理设置：%w", context.DeadlineExceeded)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("已取消获取模型列表：%w", ctx.Err())
	}
	return fmt.Errorf("获取模型列表失败，请检查网络、证书和服务地址：%w", err)
}

func modelFetchStatusError(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return errors.New("API 密钥无效或未填写，请检查密钥及访问权限")
	case status == http.StatusNotFound:
		return errors.New("该接口不支持自动拉取模型列表，请手动输入")
	case status == http.StatusTooManyRequests:
		return errors.New("请求过于频繁，请稍后重试")
	case status >= 300 && status < 400:
		return errors.New("接口发生重定向，请在设置中填写重定向后的最终服务地址")
	default:
		return fmt.Errorf("获取模型列表失败（HTTP %d），请检查服务地址及状态", status)
	}
}

func doModelsGET(ctx context.Context, reqURL string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := aiClient.Do(req)
	if err != nil {
		return 0, nil, modelFetchNetworkError(ctx, err)
	}
	defer resp.Body.Close()

	const limit = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, nil, modelFetchNetworkError(ctx, err)
	}
	if len(data) > limit {
		return resp.StatusCode, nil, errors.New("模型列表响应过大，请手动输入模型名称")
	}
	return resp.StatusCode, data, nil
}

func supportsGenerateContent(methods []string) bool {
	if len(methods) == 0 {
		return true
	}
	for _, m := range methods {
		if strings.EqualFold(strings.TrimSpace(m), "generateContent") {
			return true
		}
	}
	return false
}

func parseOpenAIOrOllamaModels(data []byte, stripModelsPrefix bool) []string {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID                         string   `json:"id"`
			Name                       string   `json:"name"`
			Model                      string   `json:"model"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &payload) != nil {
		return nil
	}
	if len(payload.Data) > 0 {
		out := make([]string, 0, len(payload.Data))
		for _, item := range payload.Data {
			id := strings.TrimSpace(item.ID)
			if stripModelsPrefix {
				id = strings.TrimPrefix(id, "models/")
			}
			if id != "" {
				out = append(out, id)
			}
		}
		return out
	}
	if len(payload.Models) > 0 {
		out := make([]string, 0, len(payload.Models))
		for _, item := range payload.Models {
			if !supportsGenerateContent(item.SupportedGenerationMethods) {
				continue
			}
			id := strings.TrimSpace(item.ID)
			if id == "" {
				id = strings.TrimSpace(item.Name)
			}
			if id == "" {
				id = strings.TrimSpace(item.Model)
			}
			if stripModelsPrefix || len(item.SupportedGenerationMethods) > 0 {
				id = strings.TrimPrefix(id, "models/")
			}
			if id != "" {
				out = append(out, id)
			}
		}
		return out
	}
	return nil
}

func parseGeminiNativeModels(data []byte) []string {
	var payload struct {
		Models []struct {
			Name                       string   `json:"name"`
			ID                         string   `json:"id"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &payload) != nil || len(payload.Models) == 0 {
		return nil
	}
	out := make([]string, 0, len(payload.Models))
	for _, item := range payload.Models {
		if !supportsGenerateContent(item.SupportedGenerationMethods) {
			continue
		}
		id := strings.TrimSpace(item.Name)
		if id == "" {
			id = strings.TrimSpace(item.ID)
		}
		id = strings.TrimPrefix(id, "models/")
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func isNonChatModel(id string) bool {
	lower := strings.ToLower(id)
	for _, needle := range nonChatModelSubstrings {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

type modelToken struct {
	isNum bool
	text  string // digits trimmed of leading zeros when isNum, lowercase text otherwise
}

func tokenizeModelID(id string) []modelToken {
	lower := strings.ToLower(id)
	tokens := make([]modelToken, 0, 8)
	for i := 0; i < len(lower); {
		isDigit := lower[i] >= '0' && lower[i] <= '9'
		j := i + 1
		for j < len(lower) && ((lower[j] >= '0' && lower[j] <= '9') == isDigit) {
			j++
		}
		seg := lower[i:j]
		if isDigit {
			seg = strings.TrimLeft(seg, "0")
		}
		tokens = append(tokens, modelToken{isNum: isDigit, text: seg})
		i = j
	}
	return tokens
}

func compareModelIDs(a, b string) int {
	ta, tb := tokenizeModelID(a), tokenizeModelID(b)
	n := len(ta)
	if len(tb) < n {
		n = len(tb)
	}
	for i := 0; i < n; i++ {
		xa, xb := ta[i], tb[i]
		if xa.isNum != xb.isNum {
			if xa.isNum {
				return -1
			}
			return 1
		}
		if xa.isNum {
			if len(xa.text) != len(xb.text) {
				if len(xa.text) > len(xb.text) {
					return -1
				}
				return 1
			}
			if xa.text != xb.text {
				if xa.text > xb.text {
					return -1
				}
				return 1
			}
		} else if xa.text != xb.text {
			if xa.text < xb.text {
				return -1
			}
			return 1
		}
	}
	if len(ta) != len(tb) {
		if len(ta) < len(tb) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func filterAndOrderModels(raw []string, sortDesc bool) []string {
	seen := make(map[string]struct{}, len(raw))
	deduped := make([]string, 0, len(raw))
	for _, item := range raw {
		id := strings.TrimSpace(item)
		if id == "" || utf8.RuneCountInString(id) > 256 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		deduped = append(deduped, id)
	}
	if len(deduped) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(deduped))
	for _, id := range deduped {
		if !isNonChatModel(id) {
			filtered = append(filtered, id)
		}
	}
	result := filtered
	if len(result) == 0 {
		result = deduped
	}
	if sortDesc {
		sort.Slice(result, func(i, j int) bool {
			return compareModelIDs(result[i], result[j]) < 0
		})
	}
	return result
}

// FetchAIModels queries the configured provider endpoint for available text/chat model IDs.
func FetchAIModels(ctx context.Context, input AIConfigInput) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, modelFetchNetworkError(ctx, err)
	}
	u, base, err := normalizeModelsBaseURL(input.Endpoint)
	if err != nil {
		return nil, err
	}
	apiKey, err := resolveAIModelFetchKey(input)
	if err != nil {
		return nil, err
	}

	if isAnthropicEndpoint(base) {
		var reqURL string
		switch {
		case u.Path == "" || u.Path == "/":
			reqURL = base + "/v1/models?limit=100"
		case strings.HasSuffix(strings.ToLower(base), "/models"):
			reqURL = base + "?limit=100"
		default:
			reqURL = base + "/models?limit=100"
		}
		headers := map[string]string{
			"anthropic-version": "2023-06-01",
		}
		if apiKey != "" {
			headers["x-api-key"] = apiKey
		}
		status, data, err := doModelsGET(ctx, reqURL, headers)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, modelFetchStatusError(status)
		}
		models := filterAndOrderModels(parseOpenAIOrOllamaModels(data, false), false)
		if len(models) == 0 {
			return nil, errors.New("未从接口获取到可用模型，请手动输入")
		}
		return models, nil
	}

	if isGeminiEndpoint(base) {
		if strings.Contains(strings.ToLower(u.Path), "/openai") {
			headers := map[string]string{}
			if apiKey != "" {
				headers["Authorization"] = "Bearer " + apiKey
			}
			status, data, err := doModelsGET(ctx, appendModelsPath(base), headers)
			if err != nil {
				return nil, err
			}
			if status >= 200 && status < 300 {
				if models := filterAndOrderModels(parseOpenAIOrOllamaModels(data, true), true); len(models) > 0 {
					return models, nil
				}
			}
			if ctx.Err() != nil {
				return nil, modelFetchNetworkError(ctx, ctx.Err())
			}
			nativeURL := u.Scheme + "://" + u.Host + "/v1beta/models?pageSize=100"
			nativeHeaders := map[string]string{}
			if apiKey != "" {
				nativeHeaders["x-goog-api-key"] = apiKey
			}
			nStatus, nData, nErr := doModelsGET(ctx, nativeURL, nativeHeaders)
			if nErr != nil {
				return nil, nErr
			}
			if nStatus < 200 || nStatus >= 300 {
				return nil, modelFetchStatusError(nStatus)
			}
			models := filterAndOrderModels(parseGeminiNativeModels(nData), true)
			if len(models) == 0 {
				return nil, errors.New("未从接口获取到可用模型，请手动输入")
			}
			return models, nil
		}

		nativeHeaders := map[string]string{}
		if apiKey != "" {
			nativeHeaders["x-goog-api-key"] = apiKey
		}
		status, data, err := doModelsGET(ctx, appendModelsPath(base)+"?pageSize=100", nativeHeaders)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, modelFetchStatusError(status)
		}
		raw := parseGeminiNativeModels(data)
		if len(raw) == 0 {
			raw = parseOpenAIOrOllamaModels(data, true)
		}
		models := filterAndOrderModels(raw, true)
		if len(models) == 0 {
			return nil, errors.New("未从接口获取到可用模型，请手动输入")
		}
		return models, nil
	}

	headers := map[string]string{}
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}
	status, data, err := doModelsGET(ctx, appendModelsPath(base), headers)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, modelFetchStatusError(status)
	}
	models := filterAndOrderModels(parseOpenAIOrOllamaModels(data, false), true)
	if len(models) == 0 {
		return nil, errors.New("未从接口获取到可用模型，请手动输入")
	}
	return models, nil
}
