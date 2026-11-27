package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/sys/windows"
)

func dialAgentPipe(ctx context.Context) (io.ReadWriteCloser, error) {
	dial, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	endpoint := os.Getenv("SSH_AUTH_SOCK")
	if !strings.HasPrefix(strings.ToLower(endpoint), "\\\\.\\pipe\\") {
		endpoint = "\\\\.\\pipe\\openssh-ssh-agent"
	}
	pipe, err := winio.DialPipeContext(dial, endpoint)
	if err != nil {
		return nil, fmt.Errorf("无法访问 Windows ssh-agent，请检查代理服务或改用私钥：%w", err)
	}
	return pipe, nil
}

func agentMethod(ctx context.Context) (ssh.AuthMethod, func(), error) {
	pipe, err := dialAgentPipe(ctx)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { pipe.Close() })
	return ssh.PublicKeysCallback(agent.NewClient(pipe).Signers), func() {
		stop()
		pipe.Close()
	}, nil
}

func setupAgentForwarding(ctx context.Context, client *ssh.Client, pty *ssh.Session) (func(), error) {
	pipe, err := dialAgentPipe(ctx)
	if err != nil {
		return nil, err
	}
	ag := agent.NewClient(pipe)
	if err := agent.ForwardToAgent(client, ag); err != nil {
		pipe.Close()
		return nil, err
	}
	if err := agent.RequestAgentForwarding(pty); err != nil {
		pipe.Close()
		return nil, err
	}
	return func() { pipe.Close() }, nil
}

func lockPins(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	handle := windows.Handle(f.Fd())
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	var state windows.Overlapped
	var retryTimer *time.Timer
	defer func() {
		if retryTimer != nil {
			retryTimer.Stop()
		}
	}()
	for {
		err = windows.LockFileEx(handle, flags, 0, 1, 0, &state)
		if err == nil {
			return func() {
				windows.UnlockFileEx(handle, 0, 1, 0, &state)
				f.Close()
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			f.Close()
			return nil, err
		}
		if retryTimer == nil {
			retryTimer = time.NewTimer(40 * time.Millisecond)
		} else {
			retryTimer.Reset(40 * time.Millisecond)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-retryTimer.C:
		}
	}
}

// publishDownload moves temp to dest without overwriting an existing file.
func publishDownload(temp, dest string) error {
	return publishDownloadMode(temp, dest, false)
}

func publishDownloadMode(temp, dest string, overwrite bool) error {
	oldName, err := windows.UTF16PtrFromString(temp)
	if err != nil {
		return err
	}
	newName, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if overwrite {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(oldName, newName, flags)
}

func replaceFileAtomic(temp, dest string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = publishDownloadMode(temp, dest, true)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return err
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return err
}
