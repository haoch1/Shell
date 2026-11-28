package local

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func boolPtr(v bool) *bool { return &v }

// AppSettings stores persistent UI, terminal, and UX preferences.
type AppSettings struct {
	Theme            string   `json:"theme"`            // "light", "dark", "system"
	TerminalTheme    string   `json:"terminalTheme"`    // "auto", "shell-dark", "tokyo-night", "catppuccin-mocha", "shell-light", "warm-paper"
	FontFamily       string   `json:"fontFamily"`
	FontSize         int      `json:"fontSize"`
	LineHeight       float64  `json:"lineHeight"`
	CursorStyle      string   `json:"cursorStyle"` // "bar", "block", "underline"
	CursorBlink      bool     `json:"cursorBlink"`
	Scrollback       int      `json:"scrollback"`
	CopyOnSelect     bool     `json:"copyOnSelect"`
	RightClickPaste  bool     `json:"rightClickPaste"`
	ShowTelemetry    bool     `json:"showTelemetry"`
	WebGLEnabled     bool     `json:"webglEnabled"`
	DefaultKeepAlive int      `json:"defaultKeepAlive"`
	ProxyMode        string   `json:"proxyMode,omitempty"` // "direct" (default), "system", "socks5", "http"
	ProxyHost        string   `json:"proxyHost,omitempty"`
	ProxyPort        int      `json:"proxyPort,omitempty"`
	ProxyUser        string   `json:"proxyUser,omitempty"`
	ConfirmOnClose   bool     `json:"confirmOnClose"`
	CloseToTray           bool     `json:"closeToTray"`
	AutoCheckUpdate       bool     `json:"autoCheckUpdate"`
	HideHostIP            bool     `json:"hideHostIp,omitempty"`
	GroupOrder            []string `json:"groupOrder,omitempty"`
	SyntaxHighlight       *bool    `json:"syntaxHighlight,omitempty"`
	AutoReconnect         *bool    `json:"autoReconnect,omitempty"`
	ConfirmMultilinePaste *bool    `json:"confirmMultilinePaste,omitempty"`
	WindowWidth           int      `json:"windowWidth,omitempty"`
	WindowHeight          int      `json:"windowHeight,omitempty"`
	WindowMaximised       bool     `json:"windowMaximised,omitempty"`
}

func DefaultSettings() AppSettings {
	return AppSettings{
		Theme:                 "light",
		TerminalTheme:         "auto",
		FontFamily:            `"SF Mono", "JetBrains Mono", "Cascadia Mono", Consolas, monospace`,
		FontSize:              14,
		LineHeight:            1.28,
		CursorStyle:           "bar",
		CursorBlink:           true,
		Scrollback:            10000,
		CopyOnSelect:          false,
		RightClickPaste:       false,
		ShowTelemetry:         true,
		WebGLEnabled:          false,
		DefaultKeepAlive:      30,
		ProxyMode:             "direct",
		ConfirmOnClose:        true,
		CloseToTray:           true,
		AutoCheckUpdate:       true,
		HideHostIP:            false,
		SyntaxHighlight:       boolPtr(true),
		AutoReconnect:         boolPtr(true),
		ConfirmMultilinePaste: boolPtr(true),
		WindowWidth:           1280,
		WindowHeight:          820,
		WindowMaximised:       false,
	}
}

func cloneSettings(s AppSettings) AppSettings {
	if s.GroupOrder != nil {
		s.GroupOrder = append([]string(nil), s.GroupOrder...)
	}
	if s.SyntaxHighlight != nil {
		s.SyntaxHighlight = boolPtr(*s.SyntaxHighlight)
	}
	if s.AutoReconnect != nil {
		s.AutoReconnect = boolPtr(*s.AutoReconnect)
	}
	if s.ConfirmMultilinePaste != nil {
		s.ConfirmMultilinePaste = boolPtr(*s.ConfirmMultilinePaste)
	}
	return s
}

func normalizeSettings(s AppSettings) AppSettings {
	d := DefaultSettings()
	switch strings.ToLower(strings.TrimSpace(s.Theme)) {
	case "light", "dark", "system":
		s.Theme = strings.ToLower(strings.TrimSpace(s.Theme))
	default:
		s.Theme = d.Theme
	}
	s.TerminalTheme = strings.TrimSpace(s.TerminalTheme)
	if s.TerminalTheme == "" {
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
	switch strings.ToLower(strings.TrimSpace(s.ProxyMode)) {
	case "", "none", "direct":
		s.ProxyMode = "direct"
	case "system", "socks5", "http":
		s.ProxyMode = strings.ToLower(strings.TrimSpace(s.ProxyMode))
	default:
		s.ProxyMode = "direct"
	}
	s.ProxyHost = strings.TrimSpace(s.ProxyHost)
	s.ProxyUser = strings.TrimSpace(s.ProxyUser)
	if s.ProxyPort < 0 || s.ProxyPort > 65535 {
		s.ProxyPort = 0
	}
	if s.SyntaxHighlight == nil {
		s.SyntaxHighlight = boolPtr(true)
	} else {
		s.SyntaxHighlight = boolPtr(*s.SyntaxHighlight)
	}
	if s.AutoReconnect == nil {
		s.AutoReconnect = boolPtr(true)
	} else {
		s.AutoReconnect = boolPtr(*s.AutoReconnect)
	}
	if s.ConfirmMultilinePaste == nil {
		s.ConfirmMultilinePaste = boolPtr(true)
	} else {
		s.ConfirmMultilinePaste = boolPtr(*s.ConfirmMultilinePaste)
	}
	if len(s.GroupOrder) > 0 {
		seen := make(map[string]struct{}, len(s.GroupOrder))
		cleaned := make([]string, 0, len(s.GroupOrder))
		for _, g := range s.GroupOrder {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			if _, ok := seen[g]; ok {
				continue
			}
			seen[g] = struct{}{}
			cleaned = append(cleaned, g)
		}
		if len(cleaned) > 0 {
			s.GroupOrder = cleaned
		} else {
			s.GroupOrder = nil
		}
	} else {
		s.GroupOrder = nil
	}
	if s.WindowWidth < 880 || s.WindowWidth > 3840 {
		s.WindowWidth = d.WindowWidth
	}
	if s.WindowHeight < 560 || s.WindowHeight > 2160 {
		s.WindowHeight = d.WindowHeight
	}
	return s
}

func LoadSettings() (AppSettings, error) {
	result := DefaultSettings()
	err := withStore(func(dir string) error {
		return readSettings(dir, &result)
	})
	return result, err
}

func readSettings(dir string, result *AppSettings) error {
	data, err := readLimited(filepath.Join(dir, "settings.json"), 256<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if json.Unmarshal(data, result) != nil {
		return errors.New("应用配置文件格式无效")
	}
	*result = normalizeSettings(*result)
	return nil
}

func writeSettings(dir string, s AppSettings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "settings.json"), data)
}

func SaveSettings(s AppSettings) (AppSettings, error) {
	s = normalizeSettings(s)
	err := withStore(func(dir string) error { return writeSettings(dir, s) })
	return s, err
}

// UpdateSettings edits the latest settings under the same cross-process lock used for writing.
func UpdateSettings(edit func(*AppSettings) error) (AppSettings, error) {
	s := DefaultSettings()
	err := withStore(func(dir string) error {
		if err := readSettings(dir, &s); err != nil {
			return err
		}
		before := cloneSettings(s)
		if err := edit(&s); err != nil {
			return err
		}
		s = normalizeSettings(s)
		if reflect.DeepEqual(before, s) {
			return nil
		}
		return writeSettings(dir, s)
	})
	return s, err
}
