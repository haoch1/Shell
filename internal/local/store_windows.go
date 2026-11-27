// Package local stores the current Windows user's connection profiles and AI preferences.
package local

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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
	return readBounded(f, limit)
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
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
	var moveErr error
	for attempt := 0; attempt < 6; attempt++ {
		moveErr = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if moveErr == nil {
			return nil
		}
		if !errors.Is(moveErr, windows.ERROR_ACCESS_DENIED) && !errors.Is(moveErr, windows.ERROR_SHARING_VIOLATION) {
			return moveErr
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return moveErr
}

// SetProfilesGroup updates Group atomically for all matching profile IDs under a single store lock.
func SetProfilesGroup(ids []string, group string) error {
	group = strings.TrimSpace(group)
	if utf8.RuneCountInString(group) > 128 {
		return errors.New("分组名称最多 128 字符")
	}
	if strings.IndexFunc(group, unicode.IsControl) >= 0 {
		return errors.New("连接资料中不能包含控制字符")
	}
	if len(ids) == 0 {
		return nil
	}
	target := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			target[id] = true
		}
	}
	if len(target) == 0 {
		return nil
	}
	return withStore(func(dir string) error {
		list, err := readProfiles(dir)
		if err != nil {
			return err
		}
		changed := false
		for i := range list {
			if target[list[i].ID] && list[i].Group != group {
				list[i].Group = group
				changed = true
			}
		}
		if !changed {
			return nil
		}
		return writeProfiles(dir, list)
	})
}
