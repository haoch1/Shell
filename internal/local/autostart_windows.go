package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

type autoStartConfig struct {
	runKeyPath      string
	approvedKeyPath string
	valueName       string
	resolveExe      func() (string, error)
}

var autoStart = autoStartConfig{
	runKeyPath:      `Software\Microsoft\Windows\CurrentVersion\Run`,
	approvedKeyPath: `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`,
	valueName:       "Shell",
	resolveExe:      os.Executable,
}

func isTempGoBuildExe(exePath string) bool {
	cleanExe := filepath.Clean(exePath)
	if resolved, err := filepath.EvalSymlinks(cleanExe); err == nil && resolved != "" {
		cleanExe = filepath.Clean(resolved)
	}
	lowerExe := strings.ToLower(cleanExe)
	if !strings.Contains(lowerExe, "go-build") {
		return false
	}
	rawTmp := filepath.Clean(os.TempDir())
	if rawTmp == "" {
		return false
	}
	candidates := []string{rawTmp}
	if resolvedTmp, err := filepath.EvalSymlinks(rawTmp); err == nil && resolvedTmp != "" {
		candidates = append(candidates, filepath.Clean(resolvedTmp))
	}
	for _, tmp := range candidates {
		lowerTmp := strings.ToLower(tmp)
		if !strings.HasSuffix(lowerTmp, string(os.PathSeparator)) {
			lowerTmp += string(os.PathSeparator)
		}
		if strings.HasPrefix(lowerExe, lowerTmp) {
			return true
		}
	}
	return false
}

func resolvedAutoStartExe() (string, error) {
	exe, err := autoStart.resolveExe()
	if err != nil || strings.TrimSpace(exe) == "" {
		return "", errors.New("无法定位当前程序路径")
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != "" {
		abs = filepath.Clean(resolved)
	}
	return abs, nil
}

func formatAutoStartCommand(absExe string) string {
	return fmt.Sprintf(`"%s" --autostart`, absExe)
}

func extractExeFromRunValue(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		rest := s[1:]
		if idx := strings.IndexByte(rest, '"'); idx > 0 {
			return strings.TrimSpace(rest[:idx])
		}
		return ""
	}
	if idx := strings.Index(s, " --"); idx > 0 {
		return strings.TrimSpace(s[:idx])
	}
	if info, err := os.Stat(s); err == nil && !info.IsDir() {
		return s
	}
	if idx := strings.IndexAny(s, " \t"); idx > 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}

func sameCleanedPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	if ra, err := filepath.EvalSymlinks(ca); err == nil && ra != "" {
		ca = filepath.Clean(ra)
	}
	if rb, err := filepath.EvalSymlinks(cb); err == nil && rb != "" {
		cb = filepath.Clean(rb)
	}
	return strings.EqualFold(ca, cb)
}

func deleteRegValueIfExists(keyPath, valueName string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	err = k.DeleteValue(valueName)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

func isStartupApprovedDisabled() bool {
	ak, err := registry.OpenKey(registry.CURRENT_USER, autoStart.approvedKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	bin, _, binErr := ak.GetBinaryValue(autoStart.valueName)
	ak.Close()
	return binErr == nil && len(bin) >= 1 && (bin[0]&0x01) == 1
}

// IsAutoStartEnabled reports whether Shell is registered and active for Windows user logon auto-start.
func IsAutoStartEnabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStart.runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	val, _, err := k.GetStringValue(autoStart.valueName)
	k.Close()
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	exe := extractExeFromRunValue(val)
	if exe == "" {
		return false, nil
	}
	info, statErr := os.Stat(exe)
	if statErr != nil || info.IsDir() {
		return false, nil
	}

	// Respect Windows Task Manager StartupApproved disable state (odd first byte: 0x03, 0x07, ...).
	if isStartupApprovedDisabled() {
		return false, nil
	}
	return true, nil
}

// EnsureDefaultAutoStart enables Windows logon auto-start once on first launch of a packaged binary,
// while respecting explicit user opt-outs, Task Manager StartupApproved disables, and isolated test runs.
func EnsureDefaultAutoStart() error {
	if strings.TrimSpace(os.Getenv("SHELL_EXIT_ON_CLOSE")) == "1" || strings.TrimSpace(os.Getenv("SHELL_DATA_DIR")) != "" {
		return nil
	}
	exe, err := resolvedAutoStartExe()
	if err != nil || isTempGoBuildExe(exe) {
		return nil
	}
	if info, err := os.Stat(exe); err != nil || info.IsDir() {
		return nil
	}
	st, err := LoadSettings()
	if err != nil {
		return err
	}
	if st.AutoStartInitialized {
		return nil
	}
	if !isStartupApprovedDisabled() {
		if err := SetAutoStart(true); err != nil {
			return err
		}
	}
	_, err = UpdateSettings(func(cur *AppSettings) error {
		cur.AutoStartInitialized = true
		return nil
	})
	return err
}

// SetAutoStart enables or disables Windows logon auto-start cleanly under HKCU without UAC.
func SetAutoStart(enabled bool) error {
	if !enabled {
		return CleanAllRegistryResidue()
	}
	exe, err := resolvedAutoStartExe()
	if err != nil {
		return err
	}
	if isTempGoBuildExe(exe) {
		return errors.New("开发构建临时程序不支持注册开机自启，请使用正式发布版 Shell.exe")
	}
	info, err := os.Stat(exe)
	if err != nil || info.IsDir() {
		return errors.New("无法验证当前程序文件，未能注册开机自启")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, autoStart.runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("无法写入开机自启注册表项：%w", err)
	}
	setErr := k.SetStringValue(autoStart.valueName, formatAutoStartCommand(exe))
	closeErr := k.Close()
	if setErr != nil {
		return fmt.Errorf("写入开机自启注册表值失败：%w", setErr)
	}
	if closeErr != nil {
		return closeErr
	}
	// Clear any stale Task Manager disable override so re-enabling works immediately.
	if err := deleteRegValueIfExists(autoStart.approvedKeyPath, autoStart.valueName); err != nil {
		return fmt.Errorf("清理开机自启状态项失败：%w", err)
	}
	return nil
}

// SyncAutoStartPath silently updates HKCU\...\Run\Shell when a portable Shell.exe has moved to a new directory.
func SyncAutoStartPath() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStart.runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	val, _, err := k.GetStringValue(autoStart.valueName)
	k.Close()
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	curExe, err := resolvedAutoStartExe()
	if err != nil || isTempGoBuildExe(curExe) {
		return nil
	}
	if info, err := os.Stat(curExe); err != nil || info.IsDir() {
		return nil
	}
	recordedExe := extractExeFromRunValue(val)
	wantCmd := formatAutoStartCommand(curExe)
	if sameCleanedPath(recordedExe, curExe) && strings.EqualFold(strings.TrimSpace(val), wantCmd) {
		return nil
	}
	wk, err := registry.OpenKey(registry.CURRENT_USER, autoStart.runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	setErr := wk.SetStringValue(autoStart.valueName, wantCmd)
	closeErr := wk.Close()
	return errors.Join(setErr, closeErr)
}

// CleanAllRegistryResidue removes both HKCU Run and StartupApproved entries for Shell cleanly.
func CleanAllRegistryResidue() error {
	errRun := deleteRegValueIfExists(autoStart.runKeyPath, autoStart.valueName)
	errApproved := deleteRegValueIfExists(autoStart.approvedKeyPath, autoStart.valueName)
	return errors.Join(errRun, errApproved)
}
