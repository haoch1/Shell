package local

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// AppSettings stores persistent UI, terminal, and UX preferences.
type AppSettings struct {
	Theme            string  `json:"theme"`            // "light", "dark", "system"
	TerminalTheme    string  `json:"terminalTheme"`    // "apple-dark", "apple-light", "one-half-dark", "tokyo-night", "catppuccin-mocha", "solarized-light", "github-light"
	FontFamily       string  `json:"fontFamily"`
	FontSize         int     `json:"fontSize"`
	LineHeight       float64 `json:"lineHeight"`
	CursorStyle      string  `json:"cursorStyle"` // "bar", "block", "underline"
	CursorBlink      bool    `json:"cursorBlink"`
	Scrollback       int     `json:"scrollback"`
	CopyOnSelect     bool    `json:"copyOnSelect"`
	RightClickPaste  bool    `json:"rightClickPaste"`
	ShowTelemetry    bool    `json:"showTelemetry"`
	WebGLEnabled     bool    `json:"webglEnabled"`
	DefaultKeepAlive int     `json:"defaultKeepAlive"`
	ConfirmOnClose   bool    `json:"confirmOnClose"`
	CloseToTray      bool    `json:"closeToTray"`
}

func DefaultSettings() AppSettings {
	return AppSettings{
		Theme:            "light",
		TerminalTheme:    "apple-dark",
		FontFamily:       `"SF Mono", "JetBrains Mono", "Cascadia Mono", Consolas, monospace`,
		FontSize:         14,
		LineHeight:       1.28,
		CursorStyle:      "bar",
		CursorBlink:      true,
		Scrollback:       10000,
		CopyOnSelect:     false,
		RightClickPaste:  false,
		ShowTelemetry:    true,
		WebGLEnabled:     true,
		DefaultKeepAlive: 30,
		ConfirmOnClose:   true,
		CloseToTray:      true,
	}
}

func normalizeSettings(s AppSettings) AppSettings {
	d := DefaultSettings()
	switch strings.ToLower(strings.TrimSpace(s.Theme)) {
	case "light", "dark", "system":
		s.Theme = strings.ToLower(strings.TrimSpace(s.Theme))
	default:
		s.Theme = d.Theme
	}
	if strings.TrimSpace(s.TerminalTheme) == "" {
		s.TerminalTheme = d.TerminalTheme
	}
	if strings.TrimSpace(s.FontFamily) == "" {
		s.FontFamily = d.FontFamily
	}
	if s.FontSize < 10 || s.FontSize > 28 {
		s.FontSize = d.FontSize
	}
	if s.LineHeight < 1.0 || s.LineHeight > 2.0 {
		s.LineHeight = d.LineHeight
	}
	switch s.CursorStyle {
	case "block", "bar", "underline":
	default:
		s.CursorStyle = d.CursorStyle
	}
	if s.Scrollback < 500 || s.Scrollback > 100000 {
		s.Scrollback = d.Scrollback
	}
	if s.DefaultKeepAlive < 0 || s.DefaultKeepAlive > 3600 {
		s.DefaultKeepAlive = d.DefaultKeepAlive
	}
	return s
}

func LoadSettings() (AppSettings, error) {
	result := DefaultSettings()
	err := withStore(func(dir string) error {
		data, err := readLimited(filepath.Join(dir, "settings.json"), 256<<10)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !json.Valid(data) || json.NewDecoder(bytes.NewReader(data)).Decode(&result) != nil {
			return errors.New("应用配置文件格式无效")
		}
		result = normalizeSettings(result)
		return nil
	})
	return result, err
}

func SaveSettings(s AppSettings) (AppSettings, error) {
	s = normalizeSettings(s)
	err := withStore(func(dir string) error {
		data, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return err
		}
		return writeAtomic(filepath.Join(dir, "settings.json"), data)
	})
	return s, err
}
