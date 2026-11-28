package local

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// DetectedAICLI describes a locally installed AI CLI tool discovered on PATH.
type DetectedAICLI struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
}

var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// IsCLI reports whether the AI settings are configured to invoke a local AI CLI tool.
func (s AISettings) IsCLI() bool {
	return strings.EqualFold(strings.TrimSpace(s.Mode), "cli")
}

// Ready reports whether the current AI settings are configured enough to execute requests.
func (s AISettings) Ready() bool {
	if s.IsCLI() {
		return strings.TrimSpace(s.CLICommand) != ""
	}
	return strings.TrimSpace(s.Endpoint) != "" && strings.TrimSpace(s.Model) != ""
}

// DetectAICLIs scans PATH (and standard install directories) for local AI CLI tools.
func DetectAICLIs() []DetectedAICLI {
	specs := []struct {
		id        string
		name      string
		command   string
		fallbacks []string
	}{
		{id: "claude", name: "Claude Code", command: "claude"},
		{id: "codex", name: "Codex CLI", command: "codex"},
		{
			id:      "agy",
			name:    "Antigravity CLI",
			command: "agy",
			fallbacks: []string{
				filepath.Join(os.Getenv("LOCALAPPDATA"), "agy", "bin", "agy.exe"),
			},
		},
	}
	out := make([]DetectedAICLI, 0, len(specs))
	for _, sp := range specs {
		p, err := exec.LookPath(sp.command)
		if err != nil || p == "" {
			for _, fb := range sp.fallbacks {
				if fb != "" {
					if st, stErr := os.Stat(fb); stErr == nil && !st.IsDir() {
						p = fb
						err = nil
						break
					}
				}
			}
		}
		out = append(out, DetectedAICLI{
			ID:        sp.id,
			Name:      sp.name,
			Command:   sp.command,
			Installed: err == nil && p != "",
			Path:      p,
		})
	}
	return out
}

func validateCLISettings(s AISettings) error {
	cmd := strings.TrimSpace(s.CLICommand)
	if cmd == "" {
		return errors.New("请选择或填写本地 AI CLI 命令（例如 agy、claude、codex、gemini）")
	}
	if len(cmd) > 512 || strings.IndexFunc(cmd, unicode.IsControl) >= 0 {
		return errors.New("CLI 命令格式无效")
	}
	if len(s.CLIArgs) > 1024 || strings.IndexFunc(s.CLIArgs, unicode.IsControl) >= 0 {
		return errors.New("附加参数包含非法控制字符")
	}
	if len(s.CLIModel) > 256 || strings.IndexFunc(s.CLIModel, unicode.IsControl) >= 0 {
		return errors.New("CLI 模型名称格式无效")
	}
	return nil
}

// splitCLIArgs parses shell-like quoted arguments without invoking a shell.
func splitCLIArgs(raw string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	escaped := false
	inToken := false
	for _, r := range raw {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			inToken = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' {
				escaped = true
			} else {
				cur.WriteRune(r)
			}
			continue
		}
		switch {
		case unicode.IsSpace(r):
			if inToken {
				args = append(args, cur.String())
				cur.Reset()
				inToken = false
			}
		case r == '"' || r == '\'':
			quote = r
			inToken = true
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}
	if inToken {
		args = append(args, cur.String())
	}
	return args
}

func buildCLICommandArgs(s AISettings, fullPrompt string) (bin string, args []string, stdinPrompt string) {
	bin = strings.TrimSpace(s.CLICommand)
	extra := splitCLIArgs(strings.TrimSpace(s.CLIArgs))
	model := strings.TrimSpace(s.CLIModel)
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(bin), filepath.Ext(bin)))

	// If user explicitly provided {prompt} placeholder in extra args, substitute directly (single-line safe).
	usedPlaceholder := false
	for i, a := range extra {
		if strings.Contains(a, "{prompt}") {
			extra[i] = strings.ReplaceAll(a, "{prompt}", fullPrompt)
			usedPlaceholder = true
		}
	}
	if usedPlaceholder {
		return bin, extra, ""
	}

	switch base {
	case "claude":
		args = append(args, "-p", "--output-format", "text")
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, extra...)
		return bin, args, fullPrompt
	case "codex":
		args = append(args, "exec", "--skip-git-repo-check")
		if model != "" {
			args = append(args, "-m", model)
		}
		args = append(args, extra...)
		args = append(args, "-")
		return bin, args, fullPrompt
	case "agy":
		args = append(args, "-p", "--output-format", "text", "--disable-slash-commands")
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, extra...)
		return bin, args, fullPrompt
	case "gemini":
		if model != "" {
			args = append(args, "-m", model)
		}
		args = append(args, extra...)
		return bin, args, fullPrompt
	default:
		return bin, extra, fullPrompt
	}
}

// cappedBuffer stores up to limit bytes while draining further writes so subprocess pipes never deadlock.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remain := c.limit - c.buf.Len(); remain > 0 {
		if len(p) > remain {
			c.buf.Write(p[:remain])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

// splitValidUTF8 separates a valid UTF-8 prefix from up to 3 trailing incomplete rune bytes.
func splitValidUTF8(buf []byte) (ready, carry []byte) {
	if len(buf) == 0 || utf8.Valid(buf) {
		return buf, nil
	}
	cut := len(buf)
	for back := 1; back <= 3 && back <= len(buf); back++ {
		if utf8.RuneStart(buf[len(buf)-back]) {
			if !utf8.FullRune(buf[len(buf)-back:]) {
				cut = len(buf) - back
			}
			break
		}
	}
	return buf[:cut], buf[cut:]
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

func runLocalAICLI(ctx context.Context, s AISettings, systemPrompt, userPrompt string, onDelta func(string)) (string, error) {
	if err := validateCLISettings(s); err != nil {
		return "", err
	}
	bin := strings.TrimSpace(s.CLICommand)
	resolved, err := resolveCLIBinary(bin)
	if err != nil {
		return "", fmt.Errorf("未在本机 PATH 中找到命令「%s」，请先安装对应 CLI 或填写可执行文件完整路径", bin)
	}

	fullPrompt := userPrompt
	if strings.TrimSpace(systemPrompt) != "" {
		fullPrompt = strings.TrimSpace(systemPrompt) + "\n\n" + userPrompt
	}
	_, args, stdinText := buildCLICommandArgs(s, fullPrompt)

	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if stdinText != "" {
		cmd.Stdin = strings.NewReader(stdinText + "\n")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr := cappedBuffer{limit: 64 << 10}
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("无法启动本地 CLI「%s」：%v", bin, err)
	}

	const maxBytes = 256 << 10
	var out strings.Builder
	reader := bufio.NewReader(stdout)
	buf := make([]byte, 4096)
	var carry []byte
	var rawTotal int
	for {
		n, rErr := reader.Read(buf)
		if n > 0 && rawTotal < maxBytes {
			take := n
			if rawTotal+take > maxBytes {
				take = maxBytes - rawTotal
			}
			rawTotal += take
			combined := buf[:take]
			if len(carry) > 0 {
				combined = append(append([]byte(nil), carry...), combined...)
				carry = carry[:0]
			}
			ready, tail := splitValidUTF8(combined)
			if len(tail) > 0 {
				carry = append(carry[:0], tail...)
			}
			if len(ready) > 0 {
				chunk := ansiEscapeRe.ReplaceAllString(string(ready), "")
				out.WriteString(chunk)
				if onDelta != nil && chunk != "" {
					onDelta(chunk)
				}
			}
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				if len(carry) > 0 {
					chunk := ansiEscapeRe.ReplaceAllString(string(carry), "")
					out.WriteString(chunk)
					if onDelta != nil && chunk != "" {
						onDelta(chunk)
					}
				}
				break
			}
			_ = cmd.Wait()
			return "", networkError(ctx, rErr)
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return "", networkError(ctx, ctx.Err())
		}
		errMsg := strings.TrimSpace(ansiEscapeRe.ReplaceAllString(stderr.buf.String(), ""))
		if errMsg == "" {
			errMsg = strings.TrimSpace(out.String())
		}
		if len(errMsg) > 320 {
			errMsg = truncateUTF8(errMsg, 320) + "…"
		}
		if errMsg != "" {
			return "", fmt.Errorf("本地 CLI「%s」执行失败：%s", bin, errMsg)
		}
		return "", fmt.Errorf("本地 CLI「%s」退出异常：%v", bin, err)
	}

	result := strings.TrimSpace(out.String())
	if result == "" {
		return "", fmt.Errorf("本地 CLI「%s」未返回输出，请检查登录状态或命令行参数", bin)
	}
	return result, nil
}

func cliBaseName(cmd string) string {
	return strings.ToLower(strings.TrimSuffix(filepath.Base(cmd), filepath.Ext(cmd)))
}

func resolveCLIBinary(cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", exec.ErrNotFound
	}
	if resolved, err := exec.LookPath(cmd); err == nil && resolved != "" {
		return resolved, nil
	}
	if cliBaseName(cmd) == "agy" && !strings.ContainsAny(cmd, `/\`) {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			fb := filepath.Join(localAppData, "agy", "bin", "agy.exe")
			if st, stErr := os.Stat(fb); stErr == nil && !st.IsDir() {
				return fb, nil
			}
		}
	}
	return "", exec.ErrNotFound
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func runCLISubcommand(ctx context.Context, resolved string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Stdin = bytes.NewReader(nil)
	stdout := cappedBuffer{limit: 512 << 10}
	stderr := cappedBuffer{limit: 64 << 10}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.buf.String(), stderr.buf.String(), err
}

var cliModelNoiseWords = map[string]struct{}{
	"id":          {},
	"model":       {},
	"models":      {},
	"name":        {},
	"slug":        {},
	"provider":    {},
	"description": {},
	"status":      {},
	"error":       {},
	"warning":     {},
	"warn":        {},
	"info":        {},
	"hint":        {},
	"note":        {},
	"usage":       {},
	"help":        {},
	"options":     {},
	"option":      {},
	"commands":    {},
	"command":     {},
	"subcommands": {},
	"flags":       {},
	"arguments":   {},
	"version":     {},
	"fetching":    {},
	"loading":     {},
	"connecting":  {},
	"available":   {},
	"unknown":     {},
	"invalid":     {},
	"unrecognized": {},
	"failed":      {},
	"unable":      {},
	"please":      {},
	"try":         {},
	"run":         {},
	"see":         {},
	"use":         {},
	"none":        {},
	"null":        {},
	"true":        {},
	"false":       {},
	"default":     {},
	"total":       {},
	"found":       {},
	"showing":     {},
	"listing":     {},
	"no":          {},
	"not":         {},
}

var cliNoiseLinePrefixes = []string{
	"fetching ",
	"loading ",
	"connecting ",
	"authenticating ",
	"usage:",
	"error:",
	"warning:",
	"warn:",
	"info:",
	"hint:",
	"note:",
	"available ",
	"unknown ",
	"unrecognized ",
	"invalid ",
	"command ",
	"failed ",
	"unable ",
	"please ",
	"try ",
	"run ",
	"see ",
	"no models",
	"not logged",
	"not authenticated",
	"total ",
	"found ",
	"showing ",
}

func isValidCLIModelID(tok string) bool {
	if tok == "" || len(tok) > 128 || utf8.RuneCountInString(tok) > 128 {
		return false
	}
	if strings.Contains(tok, "..") || strings.Contains(tok, "://") {
		return false
	}
	first := tok[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')) {
		return false
	}
	last := tok[len(tok)-1]
	if last == ':' || last == '.' || last == '/' || last == '-' || last == '_' || last == '+' || last == '@' {
		return false
	}
	hasLetter := false
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			hasLetter = true
		case (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == ':' || c == '/' || c == '@' || c == '+':
		default:
			return false
		}
	}
	if !hasLetter {
		return false
	}
	_, isNoise := cliModelNoiseWords[strings.ToLower(tok)]
	return !isNoise
}

func hasModelIDStructure(tok string) bool {
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == ':' || c == '/' || c == '@' {
			return true
		}
	}
	return false
}

func isCLINoiseLine(trimmed string) bool {
	if trimmed == "" {
		return true
	}
	if strings.HasSuffix(trimmed, "...") || strings.HasSuffix(trimmed, "…") {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range cliNoiseLinePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func parseCLITextModelsOutput(raw string) []string {
	cleaned := ansiEscapeRe.ReplaceAllString(raw, "")
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")

	seen := make(map[string]struct{})
	var out []string
	for _, line := range strings.Split(cleaned, "\n") {
		trimmed := strings.TrimSpace(line)
		if isCLINoiseLine(trimmed) {
			continue
		}
		var tok string
		isStructuredCols := false
		if tabIdx := strings.IndexByte(trimmed, '\t'); tabIdx >= 0 {
			tok = strings.TrimSpace(trimmed[:tabIdx])
			isStructuredCols = true
		} else {
			fields := strings.Fields(trimmed)
			if len(fields) == 0 {
				continue
			}
			tok = fields[0]
			isStructuredCols = len(fields) == 1 || strings.Contains(trimmed, "  ")
		}
		if !isValidCLIModelID(tok) {
			continue
		}
		if !isStructuredCols && !hasModelIDStructure(tok) {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out
}

// parseAgyModelsOutput parses `agy models` stdout (`<model-id>\t<display-name>`),
// stripping ANSI codes, ignoring progress/header/error lines, and deduplicating in order.
func parseAgyModelsOutput(raw string) []string {
	return parseCLITextModelsOutput(raw)
}

func looksLikeCLIHelpOrUsage(cleaned string) bool {
	lower := strings.ToLower(cleaned)
	hasUsage := false
	hasHelpSection := false
	for _, line := range strings.Split(strings.ReplaceAll(lower, "\r", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "usage:") {
			hasUsage = true
		}
		if t == "options:" || t == "flags:" || t == "commands:" || t == "subcommands:" ||
			strings.HasPrefix(t, "global flags:") || strings.HasPrefix(t, "available commands:") ||
			strings.Contains(t, "--help") {
			hasHelpSection = true
		}
	}
	return hasUsage && hasHelpSection
}

func parseCLIModelsJSON(data []byte) ([]string, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid(trimmed) {
		return nil, false
	}

	var rawIDs []string
	if trimmed[0] == '[' {
		var strList []string
		if json.Unmarshal(trimmed, &strList) == nil {
			rawIDs = strList
		} else {
			var objList []struct {
				Slug  string `json:"slug"`
				ID    string `json:"id"`
				Name  string `json:"name"`
				Model string `json:"model"`
			}
			if json.Unmarshal(trimmed, &objList) == nil {
				for _, item := range objList {
					id := firstNonEmpty(item.Slug, item.ID, item.Name, item.Model)
					if id != "" {
						rawIDs = append(rawIDs, strings.TrimPrefix(id, "models/"))
					}
				}
			}
		}
	} else {
		rawIDs = parseOpenAIOrOllamaModels(trimmed, true)
		if len(rawIDs) == 0 {
			var strPayload struct {
				Data   []string `json:"data"`
				Models []string `json:"models"`
			}
			if json.Unmarshal(trimmed, &strPayload) == nil {
				if len(strPayload.Data) > 0 {
					rawIDs = strPayload.Data
				} else if len(strPayload.Models) > 0 {
					rawIDs = strPayload.Models
				}
			}
		}
		if len(rawIDs) == 0 {
			var slugPayload struct {
				Data []struct {
					Slug string `json:"slug"`
				} `json:"data"`
				Models []struct {
					Slug string `json:"slug"`
				} `json:"models"`
			}
			if json.Unmarshal(trimmed, &slugPayload) == nil {
				for _, item := range slugPayload.Data {
					if s := strings.TrimSpace(item.Slug); s != "" {
						rawIDs = append(rawIDs, s)
					}
				}
				for _, item := range slugPayload.Models {
					if s := strings.TrimSpace(item.Slug); s != "" {
						rawIDs = append(rawIDs, s)
					}
				}
			}
		}
	}

	return dedupeValidCLIModels(rawIDs), true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func dedupeValidCLIModels(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, item := range ids {
		id := strings.TrimSpace(item)
		if id == "" || utf8.RuneCountInString(id) > 256 || strings.IndexFunc(id, unicode.IsControl) >= 0 || strings.IndexFunc(id, unicode.IsSpace) >= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// parseGenericCLIModelsOutput parses JSON (`{"models":[...]}` / `{"data":[...]}` / `[...]`)
// or line/tab-delimited CLI model output while rejecting help/usage noise.
func parseGenericCLIModelsOutput(raw string) []string {
	cleaned := strings.TrimSpace(ansiEscapeRe.ReplaceAllString(raw, ""))
	if cleaned == "" {
		return nil
	}
	if models, ok := parseCLIModelsJSON([]byte(cleaned)); ok {
		return models
	}
	if looksLikeCLIHelpOrUsage(cleaned) {
		return nil
	}
	return parseCLITextModelsOutput(cleaned)
}

// parseCodexModelsCache decodes `~/.codex/models_cache.json`, filters visible entries
// (`visibility == "" || "list"`), sorts stably by positive priority ascending, and deduplicates.
func parseCodexModelsCache(data []byte) []string {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var payload struct {
		Models []struct {
			Slug       string `json:"slug"`
			ID         string `json:"id"`
			Visibility string `json:"visibility"`
			Priority   int    `json:"priority"`
		} `json:"models"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &payload) != nil || len(payload.Models) == 0 {
		return nil
	}
	type entry struct {
		id       string
		priority int
	}
	kept := make([]entry, 0, len(payload.Models))
	for _, item := range payload.Models {
		vis := strings.TrimSpace(item.Visibility)
		if vis != "" && !strings.EqualFold(vis, "list") {
			continue
		}
		id := strings.TrimSpace(item.Slug)
		if id == "" {
			id = strings.TrimSpace(item.ID)
		}
		if id == "" || utf8.RuneCountInString(id) > 256 || strings.IndexFunc(id, unicode.IsControl) >= 0 || strings.IndexFunc(id, unicode.IsSpace) >= 0 {
			continue
		}
		kept = append(kept, entry{id: id, priority: item.Priority})
	}
	sort.SliceStable(kept, func(i, j int) bool {
		pi, pj := kept[i].priority, kept[j].priority
		if pi > 0 && pj > 0 {
			return pi < pj
		}
		return pi > 0 && pj <= 0
	})
	raw := make([]string, 0, len(kept))
	for _, item := range kept {
		raw = append(raw, item.id)
	}
	return dedupeValidCLIModels(raw)
}

func parseTOMLStringValue(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	switch s[0] {
	case '"':
		var b strings.Builder
		escaped := false
		for i := 1; i < len(s); i++ {
			c := s[i]
			if escaped {
				b.WriteByte(c)
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				return strings.TrimSpace(b.String())
			}
			b.WriteByte(c)
		}
		return ""
	case '\'':
		if end := strings.IndexByte(s[1:], '\''); end >= 0 {
			return strings.TrimSpace(s[1 : 1+end])
		}
		return ""
	default:
		if hash := strings.IndexByte(s, '#'); hash >= 0 {
			s = s[:hash]
		}
		return strings.TrimSpace(s)
	}
}

// parseCodexConfigModel extracts the top-level `model = "..."` value from `~/.codex/config.toml`.
func parseCodexConfigModel(data []byte) string {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		return ""
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			// Top-level keys in TOML must appear before any [table] header.
			break
		}
		key, val, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "model" {
			continue
		}
		model := parseTOMLStringValue(val)
		if model == "" || utf8.RuneCountInString(model) > 256 || strings.IndexFunc(model, unicode.IsControl) >= 0 || strings.IndexFunc(model, unicode.IsSpace) >= 0 {
			return ""
		}
		return model
	}
	return ""
}

func codexHomeDir() string {
	if h := strings.TrimSpace(os.Getenv("CODEX_HOME")); h != "" {
		return h
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".codex")
	}
	return ""
}

func claudeConfigDir() string {
	if d := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".claude")
	}
	return ""
}

type claudeSettingsInfo struct {
	model     string
	baseURL   string
	apiKey    string
	authToken string
}

func parseClaudeSettings(data []byte) claudeSettingsInfo {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var payload struct {
		Model string            `json:"model"`
		Env   map[string]string `json:"env"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &payload) != nil {
		return claudeSettingsInfo{}
	}
	return claudeSettingsInfo{
		model:     strings.TrimSpace(payload.Model),
		baseURL:   strings.TrimSpace(payload.Env["ANTHROPIC_BASE_URL"]),
		apiKey:    strings.TrimSpace(payload.Env["ANTHROPIC_API_KEY"]),
		authToken: strings.TrimSpace(payload.Env["ANTHROPIC_AUTH_TOKEN"]),
	}
}

func isValidHeaderToken(s string) bool {
	return s != "" && len(s) <= 4096 && strings.IndexFunc(s, func(c rune) bool {
		return c < 0x21 || c > 0x7e
	}) < 0
}

func fetchClaudeLiveModels(ctx context.Context, baseURL, apiKey, authToken string) []string {
	if !isValidHeaderToken(apiKey) {
		apiKey = ""
	}
	if !isValidHeaderToken(authToken) {
		authToken = ""
	}
	if apiKey == "" && authToken == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	u, base, err := normalizeModelsBaseURL(baseURL)
	if err != nil {
		return nil
	}
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
	if authToken != "" {
		headers["Authorization"] = "Bearer " + authToken
	}
	subCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	status, data, err := doModelsGET(subCtx, reqURL, headers)
	if err != nil || status < 200 || status >= 300 {
		return nil
	}
	return filterAndOrderModels(parseOpenAIOrOllamaModels(data, false), false)
}

func fetchCLIAvailableModels(ctx context.Context, input AIConfigInput) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, modelFetchNetworkError(ctx, err)
	}
	cmd := strings.TrimSpace(input.CLICommand)
	if cmd == "" {
		return nil, errors.New("请先选择或填写本地 AI CLI 命令")
	}
	if len(cmd) > 512 || strings.IndexFunc(cmd, unicode.IsControl) >= 0 {
		return nil, errors.New("CLI 命令格式无效")
	}

	base := cliBaseName(cmd)
	switch base {
	case "agy":
		resolved, err := resolveCLIBinary(cmd)
		if err != nil {
			return nil, errors.New("未在本机找到 Antigravity CLI（agy），请先安装或填写完整路径")
		}
		subCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		stdout, stderr, runErr := runCLISubcommand(subCtx, resolved, "models")
		if runErr != nil {
			if subCtx.Err() != nil {
				return nil, modelFetchNetworkError(subCtx, subCtx.Err())
			}
			errMsg := strings.TrimSpace(ansiEscapeRe.ReplaceAllString(stderr, ""))
			if errMsg == "" {
				errMsg = strings.TrimSpace(ansiEscapeRe.ReplaceAllString(stdout, ""))
			}
			if len(errMsg) > 240 {
				errMsg = truncateUTF8(errMsg, 240) + "…"
			}
			if errMsg != "" {
				return nil, fmt.Errorf("读取 Antigravity 模型列表失败：%s", errMsg)
			}
			return nil, errors.New("读取 Antigravity 模型列表失败，请确认已完成登录或手动输入模型名")
		}
		models := parseAgyModelsOutput(stdout)
		if len(models) == 0 {
			return nil, errors.New("未从 Antigravity CLI 获取到可用模型，请确认已完成登录或手动输入模型名")
		}
		return models, nil

	case "codex":
		home := codexHomeDir()
		cachePath := ""
		configPath := ""
		if home != "" {
			cachePath = filepath.Join(home, "models_cache.json")
			configPath = filepath.Join(home, "config.toml")
		}
		_, binErr := resolveCLIBinary(cmd)
		cacheExists := fileExists(cachePath)
		if binErr != nil && !cacheExists {
			return nil, errors.New("未在本机找到 Codex CLI，请先安装并登录后重试")
		}
		var models []string
		if cacheExists {
			if data, err := readLimited(cachePath, 16<<20); err == nil {
				models = parseCodexModelsCache(data)
			}
		}
		if fileExists(configPath) {
			if data, err := readLimited(configPath, 128<<10); err == nil {
				if cfgModel := parseCodexConfigModel(data); cfgModel != "" {
					found := false
					for _, m := range models {
						if m == cfgModel {
							found = true
							break
						}
					}
					if !found {
						models = append([]string{cfgModel}, models...)
					}
				}
			}
		}
		if len(models) == 0 {
			return nil, errors.New("未找到 Codex 本地模型缓存，请先运行一次 codex 登录，或手动输入模型名")
		}
		return models, nil

	case "claude":
		cfgDir := claudeConfigDir()
		settingsPath := ""
		if cfgDir != "" {
			settingsPath = filepath.Join(cfgDir, "settings.json")
		}
		_, binErr := resolveCLIBinary(cmd)
		cfgExists := fileExists(settingsPath)
		if binErr != nil && !cfgExists {
			return nil, errors.New("未在本机找到 Claude Code（claude），请先安装并登录后重试")
		}
		var info claudeSettingsInfo
		if cfgExists {
			if data, err := readLimited(settingsPath, 256<<10); err == nil {
				info = parseClaudeSettings(data)
			}
		}
		baseURL := firstNonEmpty(info.baseURL, os.Getenv("ANTHROPIC_BASE_URL"))
		apiKey := firstNonEmpty(info.apiKey, os.Getenv("ANTHROPIC_API_KEY"))
		authToken := firstNonEmpty(info.authToken, os.Getenv("ANTHROPIC_AUTH_TOKEN"))

		var merged []string
		if info.model != "" {
			merged = append(merged, info.model)
		}
		if live := fetchClaudeLiveModels(ctx, baseURL, apiKey, authToken); len(live) > 0 {
			merged = append(merged, live...)
		}
		if ctx.Err() != nil {
			return nil, modelFetchNetworkError(ctx, ctx.Err())
		}
		merged = append(merged, "sonnet", "opus", "haiku")
		return dedupeValidCLIModels(merged), nil

	default:
		resolved, err := resolveCLIBinary(cmd)
		if err != nil {
			return nil, fmt.Errorf("未在本机 PATH 中找到命令「%s」，请先安装或填写完整路径", cmd)
		}
		subCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		stdout, _, runErr := runCLISubcommand(subCtx, resolved, "models")
		if ctx.Err() != nil {
			return nil, modelFetchNetworkError(ctx, ctx.Err())
		}
		if runErr == nil {
			if models := parseGenericCLIModelsOutput(stdout); len(models) > 0 {
				return models, nil
			}
		}
		if base == "gemini" {
			return []string{"gemini-2.5-pro", "gemini-2.5-flash"}, nil
		}
		return nil, errors.New("该 CLI 工具暂不支持自动读取模型列表，请手动输入模型名")
	}
}
