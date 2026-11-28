package local

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/haoch1/Shell/internal/security"
	"golang.org/x/sys/windows"
)

// AgentConfig controls external Agent MCP/HTTP bridge, execution mode, command permissions, and text redaction.
type AgentConfig struct {
	Enabled   bool                     `json:"enabled"`
	Port      int                      `json:"port"`
	ExecMode  string                   `json:"execMode,omitempty"` // "silent" (default, background headless/tray) or "visible" (open/focus GUI terminal)
	Policy    security.PolicyConfig    `json:"policy"`
	Redaction security.RedactionConfig `json:"redaction"`
}

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Enabled:   false,
		Port:      17822,
		ExecMode:  "silent",
		Policy:    security.DefaultPolicyConfig(),
		Redaction: security.DefaultRedactionConfig(),
	}
}

// NormalizeAgentConfig supplies the same defaults to storage and the desktop listener.
func NormalizeAgentConfig(c AgentConfig) AgentConfig {
	d := DefaultAgentConfig()
	if c.Port < 1024 || c.Port > 65535 {
		c.Port = d.Port
	}
	switch c.ExecMode {
	case "silent", "visible":
	default:
		c.ExecMode = d.ExecMode
	}
	switch c.Policy.Mode {
	case security.ModeReadOnly, security.ModeSmart, security.ModeAskAll, security.ModeTrusted:
	default:
		c.Policy.Mode = d.Policy.Mode
	}
	if c.Policy.DeniedPaths == nil {
		c.Policy.DeniedPaths = d.Policy.DeniedPaths
	}
	return c
}

func LoadAgentConfig() (AgentConfig, error) {
	cfg := DefaultAgentConfig()
	err := withStore(func(dir string) error {
		data, err := readLimited(configPath(dir, "agent.json"), 512<<10)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !json.Valid(data) || json.NewDecoder(bytes.NewReader(data)).Decode(&cfg) != nil {
			return errors.New("Agent 配置文件格式无效")
		}
		cfg = NormalizeAgentConfig(cfg)
		return nil
	})
	return cfg, err
}

func SaveAgentConfig(cfg AgentConfig) (AgentConfig, error) {
	cfg = NormalizeAgentConfig(cfg)
	err := withStore(func(dir string) error {
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return writeAtomic(configPath(dir, "agent.json"), data)
	})
	return cfg, err
}

type agentTokenBlob struct {
	Token string `json:"token"`
}

// EnsureAgentToken returns the DPAPI-protected Bearer token for the local MCP/HTTP bridge, generating one on first use.
func EnsureAgentToken() (string, error) {
	var token string
	err := withStore(func(dir string) error {
		p := securityPath(dir, "agent-token.dpapi")
		var blob agentTokenBlob
		sealed, err := readLimited(p, 128<<10)
		if err == nil {
			if err := decodeProtected(sealed, &blob); err != nil {
				return fmt.Errorf("MCP 桥接令牌无法解密，请重置访问令牌：%w", err)
			}
			if len(blob.Token) < 32 {
				return errors.New("MCP 桥接令牌无效，请重置访问令牌")
			}
			token = blob.Token
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("无法读取 Shell MCP 桥接令牌：%w", err)
		}
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		token = "sh_" + hex.EncodeToString(b[:])
		return writeProtected(p, agentTokenBlob{Token: token})
	})
	return token, err
}

// RotateAgentToken generates and persists a brand-new DPAPI-protected Bearer token.
func RotateAgentToken() (string, error) {
	var token string
	err := withStore(func(dir string) error {
		other, err := otherDesktopOwnsBridge(dir)
		if err != nil {
			return err
		}
		if other {
			return errors.New("请在正在运行 MCP 服务的 Shell 窗口重置访问令牌")
		}
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		token = "sh_" + hex.EncodeToString(b[:])
		return writeProtected(securityPath(dir, "agent-token.dpapi"), agentTokenBlob{Token: token})
	})
	return token, err
}

// BridgeRuntimeInfo is written to %LOCALAPPDATA%\Shell\runtime\bridge.json while the GUI MCP bridge is running
// so `Shell.exe --mcp` stdio proxy can auto-discover the live local port cleanly.
type BridgeRuntimeInfo struct {
	Port   int    `json:"port"`
	PID    int    `json:"pid"`
	PipeID string `json:"pipeID,omitempty"`
}

func WriteBridgeRuntime(port int, pipeID string) error {
	return withStore(func(dir string) error {
		data, err := json.Marshal(BridgeRuntimeInfo{Port: port, PID: os.Getpid(), PipeID: pipeID})
		if err != nil {
			return err
		}
		return writeAtomic(runtimePath(dir, "bridge.json"), data)
	})
}

func ClearBridgeRuntime() {
	_ = withStore(func(dir string) error {
		other, err := otherDesktopOwnsBridge(dir)
		if err != nil || other {
			return err
		}
		return os.Remove(runtimePath(dir, "bridge.json"))
	})
}

// Called with the store lock so a different desktop cannot publish between the check and the change.
func otherDesktopOwnsBridge(dir string) (bool, error) {
	data, err := readLimited(runtimePath(dir, "bridge.json"), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var info BridgeRuntimeInfo
	if json.Unmarshal(data, &info) != nil || info.PID <= 0 || info.PID == os.Getpid() {
		return false, nil
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(info.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) { // Process already exited.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, 0)
	return state != windows.WAIT_OBJECT_0, err
}

// ReadBridgeCredentials discovers the running GUI without taking the settings writer lock.
// Both files are published by atomic replacement; the HTTP bridge authenticates the pair.
func ReadBridgeCredentials() (BridgeRuntimeInfo, string, error) {
	dir, err := DataDir()
	if err != nil {
		return BridgeRuntimeInfo{}, "", err
	}
	data, err := readBridgeFile(runtimePath(dir, "bridge.json"), 4096)
	if err != nil {
		return BridgeRuntimeInfo{}, "", fmt.Errorf("未检测到运行中的 Shell MCP 桥接服务：%w", err)
	}
	var info BridgeRuntimeInfo
	if json.Unmarshal(data, &info) != nil || info.Port < 1024 || info.Port > 65535 {
		return BridgeRuntimeInfo{}, "", errors.New("Shell MCP 桥接端口无效")
	}
	sealed, err := readBridgeFile(securityPath(dir, "agent-token.dpapi"), 128<<10)
	if err != nil {
		return BridgeRuntimeInfo{}, "", fmt.Errorf("无法读取 Shell MCP 桥接令牌：%w", err)
	}
	var blob agentTokenBlob
	if err := decodeProtected(sealed, &blob); err != nil {
		return BridgeRuntimeInfo{}, "", err
	}
	if len(blob.Token) < 32 {
		return BridgeRuntimeInfo{}, "", errors.New("Shell MCP 桥接令牌无效")
	}
	return info, blob.Token, nil
}

// FILE_SHARE_DELETE lets the GUI replace or clear a file while discovery reads its snapshot.
func readBridgeFile(path string, limit int64) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(handle), path)
	defer f.Close()
	return readBounded(f, limit)
}
