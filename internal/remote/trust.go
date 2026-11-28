package remote

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type pinCacheEntry struct {
	path    string
	modTime time.Time
	size    int64
	check   ssh.HostKeyCallback
}

var (
	pinCacheMu sync.RWMutex
	pinCache   pinCacheEntry
)

func invalidatePinCache(path string) {
	pinCacheMu.Lock()
	if pinCache.path == path {
		pinCache = pinCacheEntry{}
	}
	pinCacheMu.Unlock()
}

type hostKeyMismatchError struct {
	OldFingerprint string
	Path           string
	Cause          error
}

func (e *hostKeyMismatchError) Error() string {
	return fmt.Sprintf("主机指纹已变更或已撤销，连接已停止。请核实服务器身份后检查 %s：%v", e.Path, e.Cause)
}

func (e *hostKeyMismatchError) Unwrap() error { return e.Cause }

func hostVerifier(
	life context.Context,
	verify func(string, string, string) bool,
	verifyChanged func(address, algorithm, newFingerprint, oldFingerprint string) bool,
) (ssh.HostKeyCallback, error) {
	dir, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows.
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "Shell", "known_hosts")
	return func(host string, peer net.Addr, key ssh.PublicKey) error {
		ctx, cancel := context.WithTimeout(life, 60*time.Second)
		defer cancel()
		trusted, err := verifyPin(ctx, path, host, peer, key, false, false)
		if err != nil {
			var mismatch *hostKeyMismatchError
			if errors.As(err, &mismatch) && verifyChanged != nil {
				choice := make(chan bool, 1)
				go func() {
					choice <- verifyChanged(host, key.Type(), ssh.FingerprintSHA256(key), mismatch.OldFingerprint)
				}()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case accept := <-choice:
					if !accept {
						return errors.New("已拒绝变更后的主机指纹")
					}
				}
				_, repErr := verifyPin(ctx, path, host, peer, key, true, true)
				return repErr
			}
			return err
		}
		if trusted {
			return nil
		}
		if verify == nil {
			return errors.New("首次连接需要确认主机指纹")
		}
		// The UI can decline or dismiss the dialog while dialing is being cancelled.
		choice := make(chan bool, 1)
		go func() { choice <- verify(host, key.Type(), ssh.FingerprintSHA256(key)) }()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case accept := <-choice:
			if !accept {
				return errors.New("已拒绝此主机指纹")
			}
		}
		// A concurrent connection may have accepted a different key while the dialog was open.
		_, err = verifyPin(ctx, path, host, peer, key, true, false)
		return err
	}, nil
}

// verifyPinFast checks known_hosts without taking the cross-process write lock when the file
// is unchanged in memory. Mismatches or transient read errors fall back to the locked path.
func verifyPinFast(path, host string, peer net.Addr, key ssh.PublicKey) (trusted bool, decided bool, err error) {
	info, statErr := os.Stat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return false, true, nil
	}
	if statErr != nil {
		return false, false, nil
	}
	if info.Size() > 8<<20 {
		return false, true, errors.New("known_hosts 文件过大")
	}

	pinCacheMu.RLock()
	var check ssh.HostKeyCallback
	if pinCache.check != nil && pinCache.path == path && pinCache.size == info.Size() && pinCache.modTime.Equal(info.ModTime()) {
		check = pinCache.check
	}
	pinCacheMu.RUnlock()

	if check == nil {
		parsed, parseErr := knownhosts.New(path)
		if parseErr != nil {
			return false, false, nil
		}
		if after, afterErr := os.Stat(path); afterErr == nil && after.Size() == info.Size() && after.ModTime().Equal(info.ModTime()) {
			pinCacheMu.Lock()
			pinCache = pinCacheEntry{
				path:    path,
				modTime: info.ModTime(),
				size:    info.Size(),
				check:   parsed,
			}
			pinCacheMu.Unlock()
		}
		check = parsed
	}

	checkErr := check(host, peer, key)
	if checkErr == nil {
		return true, true, nil
	}
	var missing *knownhosts.KeyError
	if errors.As(checkErr, &missing) && len(missing.Want) == 0 {
		return false, true, nil
	}
	// On host key mismatch/revocation, fall back to a locked fresh read to avoid any stale cache.
	return false, false, nil
}

func stripHostFromKnownHosts(raw []byte, targetHost string, removeLines map[int]bool) []byte {
	normalizedTarget := knownhosts.Normalize(targetHost)
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 2<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if removeLines[lineNo] {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "@") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 3 && fields[0] == normalizedTarget {
				continue
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func verifyPin(ctx context.Context, path, host string, peer net.Addr, key ssh.PublicKey, accept, replaceMismatch bool) (bool, error) {
	if !accept {
		if trusted, decided, err := verifyPinFast(path, host, peer, key); decided {
			return trusted, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	unlock, err := lockPins(ctx, path+".lock")
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0600)
	if err != nil {
		return false, err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil {
		return false, err
	}
	if info.Size() > 8<<20 {
		return false, errors.New("known_hosts 文件过大")
	}
	check, err := knownhosts.New(path)
	if err != nil {
		return false, fmt.Errorf("读取 known_hosts 失败：%w", err)
	}
	err = check(host, peer, key)
	if err == nil {
		pinCacheMu.Lock()
		pinCache = pinCacheEntry{
			path:    path,
			modTime: info.ModTime(),
			size:    info.Size(),
			check:   check,
		}
		pinCacheMu.Unlock()
		return true, nil
	}
	var missing *knownhosts.KeyError
	isKeyErr := errors.As(err, &missing)
	if !isKeyErr {
		return false, fmt.Errorf("主机指纹已变更或已撤销，连接已停止。请核实服务器身份后检查 %s：%w", path, err)
	}
	removeLines := make(map[int]bool)
	if len(missing.Want) != 0 {
		oldFP := ""
		for _, w := range missing.Want {
			if w.Line > 0 {
				removeLines[w.Line] = true
			}
			if oldFP == "" && w.Key != nil {
				oldFP = ssh.FingerprintSHA256(w.Key)
			}
		}
		if !replaceMismatch {
			return false, &hostKeyMismatchError{OldFingerprint: oldFP, Path: path, Cause: err}
		}
	}
	if !accept {
		return false, nil
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if replaceMismatch {
		previous = stripHostFromKnownHosts(previous, host, removeLines)
	}
	if len(previous) > 0 && !bytes.HasSuffix(previous, []byte("\n")) {
		previous = append(previous, '\n')
	}
	content := append(previous, knownhosts.Line([]string{host}, key)+"\n"...)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".known_hosts-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(content); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err = replaceFileAtomic(tmp.Name(), path); err != nil {
		return false, err
	}
	invalidatePinCache(path)
	return true, nil
}
