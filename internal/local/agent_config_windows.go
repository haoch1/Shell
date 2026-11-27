package local

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/haoch1/Shell/internal/security"
)

// AgentConfig controls external Agent MCP/HTTP bridge, command permissions, and text redaction.
type AgentConfig struct {
	Enabled   bool                     `json:"enabled"`
	Port      int                      `json:"port"`
	Policy    security.PolicyConfig    `json:"policy"`
	Redaction security.RedactionConfig `json:"redaction"`
}

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Enabled:   false,
		Port:      17822,
		Policy:    security.DefaultPolicyConfig(),
		Redaction: security.DefaultRedactionConfig(),
	}
}

func normalizeAgentConfig(c AgentConfig) AgentConfig {
	d := DefaultAgentConfig()
	if c.Port < 1024 || c.Port > 65535 {
		c.Port = d.Port
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
		data, err := readLimited(filepath.Join(dir, "agent.json"), 512<<10)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !json.Valid(data) || json.NewDecoder(bytes.NewReader(data)).Decode(&cfg) != nil {
			return errors.New("Agent 配置文件格式无效")
		}
		cfg = normalizeAgentConfig(cfg)
		return nil
	})
	return cfg, err
}

func SaveAgentConfig(cfg AgentConfig) (AgentConfig, error) {
	cfg = normalizeAgentConfig(cfg)
	err := withStore(func(dir string) error {
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return writeAtomic(filepath.Join(dir, "agent.json"), data)
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
		p := filepath.Join(dir, "agent-token.dpapi")
		var blob agentTokenBlob
		err := readProtected(p, &blob)
		if err == nil && len(blob.Token) >= 32 {
			token = blob.Token
			return nil
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
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		token = "sh_" + hex.EncodeToString(b[:])
		return writeProtected(filepath.Join(dir, "agent-token.dpapi"), agentTokenBlob{Token: token})
	})
	return token, err
}

// BridgeRuntimeInfo is written to %LOCALAPPDATA%\Shell\bridge-runtime.json while the GUI MCP bridge is running
// so `Shell.exe --mcp` stdio proxy can auto-discover the live local port cleanly.
type BridgeRuntimeInfo struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

func WriteBridgeRuntime(port int) error {
	return withStore(func(dir string) error {
		data, err := json.Marshal(BridgeRuntimeInfo{Port: port, PID: os.Getpid()})
		if err != nil {
			return err
		}
		return writeAtomic(filepath.Join(dir, "bridge-runtime.json"), data)
	})
}

func ClearBridgeRuntime() {
	_ = withStore(func(dir string) error {
		err := os.Remove(filepath.Join(dir, "bridge-runtime.json"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}

func ReadBridgeRuntime() (BridgeRuntimeInfo, error) {
	var info BridgeRuntimeInfo
	err := withStore(func(dir string) error {
		data, err := readLimited(filepath.Join(dir, "bridge-runtime.json"), 4096)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, &info)
	})
	return info, err
}
