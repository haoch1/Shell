package local

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func dpapi(input []byte, protect bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, errors.New("加密设置为空")
	}
	source := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&source, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&source, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, errors.New("Windows 无法使用当前账户加密或解密凭据")
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(out.Data))))
	buffer := unsafe.Slice(out.Data, out.Size)
	defer clear(buffer)
	return append([]byte(nil), buffer...), nil
}

func writeProtected(path string, value any) error {
	plain, err := json.Marshal(value)
	if err != nil {
		return err
	}
	defer clear(plain)
	sealed, err := dpapi(plain, true)
	if err != nil {
		return err
	}
	return writeAtomic(path, sealed)
}

func readProtected(path string, value any) error {
	sealed, err := readLimited(path, 128<<10)
	if err != nil {
		return err
	}
	plain, err := dpapi(sealed, false)
	if err != nil {
		return err
	}
	defer clear(plain)
	if !json.Valid(plain) || json.Unmarshal(plain, value) != nil {
		return errors.New("加密设置格式无效")
	}
	return nil
}

// authIdentity captures only the peer & authentication target fields that bind a stored credential.
// Renaming, re-grouping, changing tags, color, notes, or tunnels does NOT invalidate the credential.
type authIdentity struct {
	ID      string `json:"id"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	Auth    string `json:"auth"`
	KeyPath string `json:"keyPath"`
}

func identityKey(p Profile) authIdentity {
	return authIdentity{
		ID:      p.ID,
		Host:    p.Host,
		Port:    p.Port,
		User:    p.User,
		Auth:    p.Auth,
		KeyPath: p.KeyPath,
	}
}

func secretPath(dir, id string) string {
	digest := sha256.Sum256([]byte(id))
	return filepath.Join(dir, "credential-"+hex.EncodeToString(digest[:])+".dpapi")
}

func removeSecret(dir, id string) error {
	err := os.Remove(secretPath(dir, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func ForgetSecret(id string) error {
	return withStore(func(dir string) error { return removeSecret(dir, id) })
}

// HasSecret supplies the profile list's indicator without decrypting credentials.
func HasSecret(id string) (bool, error) {
	var saved bool
	err := withStore(func(dir string) error {
		_, err := os.Stat(secretPath(dir, id))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		saved = err == nil
		return err
	})
	return saved, err
}

// SecretIDs checks which profile IDs currently have saved DPAPI credentials under a single lock.
func SecretIDs(ids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	err := withStore(func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		existing := make(map[string]bool, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "credential-") && strings.HasSuffix(e.Name(), ".dpapi") {
				existing[e.Name()] = true
			}
		}
		for _, id := range ids {
			if id == "" {
				continue
			}
			digest := sha256.Sum256([]byte(id))
			if existing["credential-"+hex.EncodeToString(digest[:])+".dpapi"] {
				result[id] = true
			}
		}
		return nil
	})
	return result, err
}

type credential struct {
	For    authIdentity `json:"for"`
	Secret string       `json:"secret"`
}

func matchesStored(dir string, p Profile) (bool, error) {
	list, err := readProfiles(dir)
	if err != nil {
		return false, err
	}
	for _, item := range list {
		if item.ID == p.ID {
			return identityKey(item) == identityKey(p), nil
		}
	}
	return false, nil
}

func RememberSecret(p Profile, secret string) error {
	if p.ID == "" || (p.Auth != "password" && p.Auth != "key") || secret == "" || len(secret) > 8192 {
		return errors.New("所选连接无法保存此凭据")
	}
	return withStore(func(dir string) error {
		match, err := matchesStored(dir, p)
		if err != nil {
			return err
		}
		if !match {
			return errors.New("连接资料已变化，本次凭据未保存")
		}
		return writeProtected(secretPath(dir, p.ID), credential{For: identityKey(p), Secret: secret})
	})
}

func LoadSecret(p Profile) (string, error) {
	var secret string
	err := withStore(func(dir string) error {
		match, err := matchesStored(dir, p)
		if err != nil || !match {
			return err
		}
		var item credential
		err = readProtected(secretPath(dir, p.ID), &item)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if item.For == identityKey(p) {
			secret = item.Secret
		}
		return nil
	})
	return secret, err
}
