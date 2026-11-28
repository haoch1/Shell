package local

import (
	"fmt"
	"os"
	"path/filepath"
)

// DataDir returns the root Shell local data directory (%LOCALAPPDATA%\Shell).
func DataDir() (string, error) {
	root, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows.
	if err != nil {
		return "", fmt.Errorf("无法定位本地设置目录：%w", err)
	}
	return filepath.Join(root, "Shell"), nil
}

// ConfigDir returns %LOCALAPPDATA%\Shell\config.
func ConfigDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config"), nil
}

// SecurityDir returns %LOCALAPPDATA%\Shell\security.
func SecurityDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "security"), nil
}

// LogsDir returns %LOCALAPPDATA%\Shell\logs.
func LogsDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}

// RuntimeDir returns %LOCALAPPDATA%\Shell\runtime.
func RuntimeDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runtime"), nil
}

// UpdatesDir returns %LOCALAPPDATA%\Shell\runtime\updates.
func UpdatesDir() (string, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "updates"), nil
}

// WebViewCacheDir returns %LOCALAPPDATA%\Shell\cache\WebView2.
func WebViewCacheDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cache", "WebView2"), nil
}

// ConfigFile returns %LOCALAPPDATA%\Shell\config\<name>.
func ConfigFile(name string) (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func configPath(root, name string) string {
	return filepath.Join(root, "config", name)
}

func securityPath(root, name string) string {
	return filepath.Join(root, "security", name)
}

func runtimePath(root, name string) string {
	return filepath.Join(root, "runtime", name)
}

func ensureParentDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0700)
}
