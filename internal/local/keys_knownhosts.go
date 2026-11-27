package local

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh"
)

// KnownHostEntry describes one pinned host key record in %LOCALAPPDATA%\Shell\known_hosts.
type KnownHostEntry struct {
	Line        int      `json:"line"`
	Hosts       []string `json:"hosts"`
	KeyType     string   `json:"keyType"`
	Fingerprint string   `json:"fingerprint"`
}

func ListKnownHosts() ([]KnownHostEntry, error) {
	var out []KnownHostEntry
	err := withStore(func(dir string) error {
		data, err := readLimited(filepath.Join(dir, "known_hosts"), 8<<20)
		if errors.Is(err, os.ErrNotExist) {
			out = []KnownHostEntry{}
			return nil
		}
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			raw := strings.TrimSpace(scanner.Text())
			if raw == "" || strings.HasPrefix(raw, "#") {
				continue
			}
			_, hosts, pubKey, _, _, err := ssh.ParseKnownHosts([]byte(raw))
			if err != nil || pubKey == nil {
				continue
			}
			out = append(out, KnownHostEntry{
				Line:        lineNo,
				Hosts:       hosts,
				KeyType:     pubKey.Type(),
				Fingerprint: ssh.FingerprintSHA256(pubKey),
			})
		}
		return scanner.Err()
	})
	if out == nil {
		out = []KnownHostEntry{}
	}
	return out, err
}

func RemoveKnownHost(line int) error {
	if line < 1 {
		return errors.New("无效的主机记录行号")
	}
	return RemoveKnownHosts([]int{line})
}

// RemoveKnownHosts removes multiple known_hosts records by 1-based line numbers atomically.
func RemoveKnownHosts(targetLines []int) error {
	if len(targetLines) == 0 {
		return nil
	}
	drop := make(map[int]bool, len(targetLines))
	for _, l := range targetLines {
		if l >= 1 {
			drop[l] = true
		}
	}
	if len(drop) == 0 {
		return nil
	}
	return withStore(func(dir string) error {
		p := filepath.Join(dir, "known_hosts")
		data, err := readLimited(p, 8<<20)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		var kept []string
		for idx, l := range lines {
			if drop[idx+1] {
				continue
			}
			if idx == len(lines)-1 && l == "" {
				continue
			}
			kept = append(kept, l)
		}
		var next []byte
		if len(kept) > 0 {
			next = []byte(strings.Join(kept, "\n") + "\n")
		}
		return writeAtomic(p, next)
	})
}

// ClearKnownHosts removes all pinned host key entries atomically.
func ClearKnownHosts() error {
	return withStore(func(dir string) error {
		p := filepath.Join(dir, "known_hosts")
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return writeAtomic(p, nil)
	})
}

// LocalKeyInfo describes a private key discovered or generated in %USERPROFILE%\.ssh.
type LocalKeyInfo struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	PublicPath  string `json:"publicPath"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	HasPublic   bool   `json:"hasPublic"`
	Encrypted   bool   `json:"encrypted"`
}

func ListLocalKeys() ([]LocalKeyInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sshDir := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []LocalKeyInfo{}, nil
		}
		return nil, err
	}
	var out []LocalKeyInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".pub") || lower == "config" || lower == "known_hosts" || lower == "known_hosts.old" || lower == "authorized_keys" {
			continue
		}
		full := filepath.Join(sshDir, name)
		data, err := readLimited(full, 256<<10)
		if err != nil || !bytes.Contains(data, []byte("PRIVATE KEY-----")) {
			continue
		}
		info := LocalKeyInfo{
			Name: name,
			Path: full,
		}
		if signer, err := ssh.ParsePrivateKey(data); err == nil {
			pub := signer.PublicKey()
			info.KeyType = pub.Type()
			info.Fingerprint = ssh.FingerprintSHA256(pub)
			info.PublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
			info.HasPublic = true
		} else {
			var locked *ssh.PassphraseMissingError
			if errors.As(err, &locked) {
				info.Encrypted = true
			}
		}
		pubPath := full + ".pub"
		if pubBytes, err := readLimited(pubPath, 64<<10); err == nil {
			info.PublicPath = pubPath
			info.HasPublic = true
			trimmed := strings.TrimSpace(string(pubBytes))
			if trimmed != "" {
				info.PublicKey = trimmed
				if pub, _, _, _, err := ssh.ParseAuthorizedKey(pubBytes); err == nil {
					info.KeyType = pub.Type()
					info.Fingerprint = ssh.FingerprintSHA256(pub)
				}
			}
		}
		clear(data)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// GenerateKey creates a new Ed25519 or RSA-4096 SSH keypair inside %USERPROFILE%\.ssh without overwriting existing files.
func GenerateKey(name, algo, passphrase, comment string) (LocalKeyInfo, error) {
	name = strings.TrimSpace(name)
	algo = strings.ToLower(strings.TrimSpace(algo))
	comment = strings.TrimSpace(comment)
	if name == "" || len(name) > 64 || strings.ContainsAny(name, `/\:*?"<>|`) || name == "." || name == ".." {
		return LocalKeyInfo{}, errors.New("请输入有效的密钥文件名（例如 id_ed25519_work）")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 || strings.IndexFunc(comment, unicode.IsControl) >= 0 {
		return LocalKeyInfo{}, errors.New("密钥名称或备注不能包含控制字符")
	}
	if algo == "" {
		algo = "ed25519"
	}
	var priv any
	var pub ssh.PublicKey
	switch algo {
	case "ed25519":
		p, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return LocalKeyInfo{}, err
		}
		priv = k
		pub, err = ssh.NewPublicKey(p)
		if err != nil {
			return LocalKeyInfo{}, err
		}
	case "rsa", "rsa-4096":
		k, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			return LocalKeyInfo{}, err
		}
		priv = k
		pub, err = ssh.NewPublicKey(&k.PublicKey)
		if err != nil {
			return LocalKeyInfo{}, err
		}
	default:
		return LocalKeyInfo{}, errors.New("密钥算法仅支持 ed25519 或 rsa-4096")
	}

	var block *pem.Block
	var err error
	if passphrase != "" {
		secret := []byte(passphrase)
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, comment, secret)
		clear(secret)
	} else {
		block, err = ssh.MarshalPrivateKey(priv, comment)
	}
	if err != nil {
		return LocalKeyInfo{}, err
	}
	privBytes := pem.EncodeToMemory(block)
	defer clear(privBytes)

	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		pubLine += " " + comment
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return LocalKeyInfo{}, err
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return LocalKeyInfo{}, err
	}
	privPath := filepath.Join(sshDir, name)
	pubPath := privPath + ".pub"
	f, err := os.OpenFile(privPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LocalKeyInfo{}, fmt.Errorf("密钥文件 %s 已存在，请更换名称", name)
		}
		return LocalKeyInfo{}, err
	}
	if _, err := f.Write(privBytes); err != nil {
		f.Close()
		os.Remove(privPath)
		return LocalKeyInfo{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(privPath)
		return LocalKeyInfo{}, err
	}
	pubFile, err := os.OpenFile(pubPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		os.Remove(privPath)
		if errors.Is(err, os.ErrExist) {
			return LocalKeyInfo{}, fmt.Errorf("公钥文件 %s.pub 已存在，请更换名称", name)
		}
		return LocalKeyInfo{}, err
	}
	_, writeErr := pubFile.WriteString(pubLine + "\n")
	closeErr := pubFile.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		os.Remove(pubPath)
		os.Remove(privPath)
		return LocalKeyInfo{}, err
	}
	return LocalKeyInfo{
		Name:        name,
		Path:        privPath,
		PublicPath:  pubPath,
		KeyType:     pub.Type(),
		Fingerprint: ssh.FingerprintSHA256(pub),
		PublicKey:   pubLine,
		HasPublic:   true,
		Encrypted:   passphrase != "",
	}, nil
}
