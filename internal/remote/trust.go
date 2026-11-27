package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func hostVerifier(life context.Context, verify func(string, string, string) bool) (ssh.HostKeyCallback, error) {
	dir, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows.
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "Shell", "known_hosts")
	return func(host string, peer net.Addr, key ssh.PublicKey) error {
		ctx, cancel := context.WithTimeout(life, 60*time.Second)
		defer cancel()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		trusted, err := verifyPin(ctx, path, host, peer, key, false)
		if err != nil || trusted {
			return err
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
		_, err = verifyPin(ctx, path, host, peer, key, true)
		return err
	}, nil
}

func verifyPin(ctx context.Context, path, host string, peer net.Addr, key ssh.PublicKey, accept bool) (bool, error) {
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
		return true, nil
	}
	var missing *knownhosts.KeyError
	if !errors.As(err, &missing) || len(missing.Want) != 0 {
		return false, fmt.Errorf("主机指纹已变更或已撤销，连接已停止。请核实服务器身份后检查 %s：%w", path, err)
	}
	if !accept {
		return false, nil
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		return false, err
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
	if err = os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}
