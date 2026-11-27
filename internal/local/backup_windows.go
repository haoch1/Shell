package local

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/scrypt"
)

type ExportBundle struct {
	Version  int               `json:"version"`
	Profiles []Profile         `json:"profiles"`
	Snippets []Snippet         `json:"snippets"`
	Settings *AppSettings      `json:"settings,omitempty"`
	Secrets  map[string]string `json:"secrets,omitempty"`
}

type encryptedEnvelope struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

const vaultFormat = "shell-vault-aes256gcm-scrypt"

func deriveVaultKey(passphrase string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(passphrase), salt, 32768, 8, 1, 32)
}

// ExportData serializes profiles, snippets, and settings.
// When passphrase is non-empty, output is encrypted with AES-256-GCM + scrypt and can optionally include DPAPI secrets.
func ExportData(includeSecrets bool, passphrase string) ([]byte, error) {
	profiles, err := Profiles()
	if err != nil {
		return nil, err
	}
	snippets, err := Snippets()
	if err != nil {
		return nil, err
	}
	settings, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	bundle := ExportBundle{
		Version:  2,
		Profiles: profiles,
		Snippets: snippets,
		Settings: &settings,
	}
	if includeSecrets {
		if passphrase == "" {
			return nil, errors.New("包含凭据的导出必须设置备份加密口令")
		}
		bundle.Secrets = make(map[string]string)
		for _, p := range profiles {
			if s, err := LoadSecret(p); err == nil && s != "" {
				bundle.Secrets[p.ID] = s
			}
		}
	}
	plain, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	if passphrase == "" {
		return plain, nil
	}
	defer clear(plain)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := deriveVaultKey(passphrase, salt)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, []byte(vaultFormat))
	env := encryptedEnvelope{
		Format:     vaultFormat,
		Version:    2,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(sealed),
	}
	return json.MarshalIndent(env, "", "  ")
}

// ImportSummary reports counts imported from a backup file.
type ImportSummary struct {
	Profiles int `json:"profiles"`
	Snippets int `json:"snippets"`
	Secrets  int `json:"secrets"`
}

// ImportData merges profiles, snippets, optional settings, and optional encrypted secrets from a backup payload.
func ImportData(raw []byte, passphrase string) (ImportSummary, error) {
	if len(raw) == 0 || len(raw) > 8<<20 {
		return ImportSummary{}, errors.New("备份文件为空或超过 8 MiB")
	}
	var env encryptedEnvelope
	if json.Unmarshal(raw, &env) == nil && env.Format == vaultFormat {
		if passphrase == "" {
			return ImportSummary{}, errors.New("此备份已加密，请输入备份口令")
		}
		salt, err := base64.StdEncoding.DecodeString(env.Salt)
		if err != nil {
			return ImportSummary{}, errors.New("备份文件损坏（无效盐值）")
		}
		nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
		if err != nil {
			return ImportSummary{}, errors.New("备份文件损坏（无效随机值）")
		}
		ct, err := base64.StdEncoding.DecodeString(env.Ciphertext)
		if err != nil {
			return ImportSummary{}, errors.New("备份文件损坏（无效密文）")
		}
		key, err := deriveVaultKey(passphrase, salt)
		if err != nil {
			return ImportSummary{}, err
		}
		defer clear(key)
		block, err := aes.NewCipher(key)
		if err != nil {
			return ImportSummary{}, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return ImportSummary{}, err
		}
		plain, err := gcm.Open(nil, nonce, ct, []byte(vaultFormat))
		if err != nil {
			return ImportSummary{}, errors.New("解密失败，请检查备份口令是否正确")
		}
		defer clear(plain)
		raw = plain
	}

	var bundle ExportBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return ImportSummary{}, fmt.Errorf("备份文件格式无效：%w", err)
	}
	var summary ImportSummary
	oldToNewID := make(map[string]string, len(bundle.Profiles))
	importedProfiles := make([]Profile, 0, len(bundle.Profiles))
	for _, p := range bundle.Profiles {
		oldID := p.ID
		p.ID = ""
		p.JumpHostID = ""
		saved, err := SaveProfile(p)
		if err != nil {
			continue
		}
		if oldID != "" {
			oldToNewID[oldID] = saved.ID
		}
		importedProfiles = append(importedProfiles, saved)
		summary.Profiles++
		if bundle.Secrets != nil && oldID != "" {
			if sec := bundle.Secrets[oldID]; sec != "" {
				if err := RememberSecret(saved, sec); err == nil {
					summary.Secrets++
				}
			}
		}
	}
	// Restore JumpHostID references among imported profiles.
	for i, orig := range bundle.Profiles {
		if orig.JumpHostID == "" || i >= len(importedProfiles) {
			continue
		}
		if mapped, ok := oldToNewID[orig.JumpHostID]; ok && mapped != importedProfiles[i].ID {
			importedProfiles[i].JumpHostID = mapped
			_, _ = SaveProfile(importedProfiles[i])
		}
	}
	for _, s := range bundle.Snippets {
		s.ID = ""
		if _, err := SaveSnippet(s); err == nil {
			summary.Snippets++
		}
	}
	if bundle.Settings != nil {
		_, _ = SaveSettings(*bundle.Settings)
	}
	return summary, nil
}

// ExportToFile writes exported backup data to disk.
func ExportToFile(destPath string, includeSecrets bool, passphrase string) error {
	data, err := ExportData(includeSecrets, passphrase)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0600)
}

// ImportFromFile reads and imports a backup file from disk.
func ImportFromFile(srcPath string, passphrase string) (ImportSummary, error) {
	data, err := readLimited(srcPath, 8<<20)
	if err != nil {
		return ImportSummary{}, err
	}
	return ImportData(data, passphrase)
}
