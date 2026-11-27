// Package local stores the current Windows user's connection profiles and AI preferences.
package local

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

var settingsLock sync.Mutex

func withStore(op func(string) error) error {
	settingsLock.Lock()
	defer settingsLock.Unlock()

	root, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows.
	if err != nil {
		return fmt.Errorf("无法定位本地设置目录：%w", err)
	}
	dir := filepath.Join(root, "Shell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "settings.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	handle := windows.Handle(f.Fd())
	var offset windows.Overlapped
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &offset); err != nil {
		return err
	}
	defer windows.UnlockFileEx(handle, 0, 1, 0, &offset)
	return op(dir)
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("设置文件过大")
	}
	return data, nil
}

func writeAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".shell-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name()) // After a successful replacement the temporary name no longer exists.
	_, writeErr := temp.Write(data)
	if writeErr == nil {
		writeErr = temp.Sync()
	}
	closeErr := temp.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	from, err := windows.UTF16PtrFromString(temp.Name())
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
