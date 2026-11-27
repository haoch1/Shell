package local

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
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
	resolved, err := exec.LookPath(bin)
	if err != nil && strings.EqualFold(bin, "agy") {
		fb := filepath.Join(os.Getenv("LOCALAPPDATA"), "agy", "bin", "agy.exe")
		if st, stErr := os.Stat(fb); stErr == nil && !st.IsDir() {
			resolved = fb
			err = nil
		}
	}
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
