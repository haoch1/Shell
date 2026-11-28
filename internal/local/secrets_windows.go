package local

import (
	"encoding/json"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const credentialVaultLimit = 2 << 20

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
	return readProtectedLimited(path, 128<<10, value)
}

func readProtectedLimited(path string, limit int64, value any) error {
	sealed, err := readLimited(path, limit)
	if err != nil {
		return err
	}
	return decodeProtected(sealed, value)
}

func decodeProtected(sealed []byte, value any) error {
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

type credential struct {
	For    authIdentity `json:"for"`
	Secret string       `json:"secret"`
}

type credentialVault struct {
	Items map[string]credential `json:"items"`
}

func credentialsVaultPath(dir string) string {
	return securityPath(dir, "credentials.dpapi")
}

func readCredentialVault(dir string) (map[string]credential, error) {
	var vault credentialVault
	err := readProtectedLimited(credentialsVaultPath(dir), credentialVaultLimit, &vault)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]credential{}, nil
	}
	if err != nil {
		return nil, err
	}
	if vault.Items == nil {
		return map[string]credential{}, nil
	}
	return vault.Items, nil
}

func writeCredentialVault(dir string, items map[string]credential) error {
	p := credentialsVaultPath(dir)
	if len(items) == 0 {
		err := os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return writeProtected(p, credentialVault{Items: items})
}

func removeSecrets(dir string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	items, err := readCredentialVault(dir)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	changed := false
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := items[id]; ok {
			delete(items, id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return writeCredentialVault(dir, items)
}

func removeSecret(dir, id string) error {
	if id == "" {
		return nil
	}
	return removeSecrets(dir, []string{id})
}

func ForgetSecret(id string) error {
	return withStore(func(dir string) error { return removeSecret(dir, id) })
}

// ForgetProfileSecret clears only credentials still bound to the edited profile.
func ForgetProfileSecret(p Profile) error {
	return withStore(func(dir string) error {
		match, err := matchesStored(dir, p)
		if err != nil {
			return err
		}
		if !match {
			return errors.New("连接资料已变化，请重新打开后再修改凭据")
		}
		return removeSecret(dir, p.ID)
	})
}

// HasSecret reports whether a profile currently has a saved credential in the consolidated vault.
func HasSecret(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var saved bool
	err := withStore(func(dir string) error {
		items, err := readCredentialVault(dir)
		if err != nil {
			return err
		}
		item, ok := items[id]
		saved = ok && item.Secret != ""
		return nil
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
		items, err := readCredentialVault(dir)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if id == "" {
				continue
			}
			if item, ok := items[id]; ok && item.Secret != "" {
				result[id] = true
			}
		}
		return nil
	})
	return result, err
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
		items, err := readCredentialVault(dir)
		if err != nil {
			return err
		}
		items[p.ID] = credential{For: identityKey(p), Secret: secret}
		return writeCredentialVault(dir, items)
	})
}

func LoadSecret(p Profile) (string, error) {
	var secret string
	err := withStore(func(dir string) error {
		match, err := matchesStored(dir, p)
		if err != nil || !match {
			return err
		}
		items, err := readCredentialVault(dir)
		if err != nil {
			return err
		}
		if item, ok := items[p.ID]; ok && item.For == identityKey(p) {
			secret = item.Secret
		}
		return nil
	})
	return secret, err
}
