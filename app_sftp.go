package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/haoch1/Shell/internal/remote"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type transferSpeedTracker struct {
	startAt   time.Time
	lastAt    time.Time
	lastBytes int64
	emaBps    float64
}

func newTransferSpeedTracker() *transferSpeedTracker {
	now := time.Now()
	return &transferSpeedTracker{
		startAt: now,
		lastAt:  now,
	}
}

func (t *transferSpeedTracker) sample(done int64) int64 {
	now := time.Now()
	dt := now.Sub(t.lastAt).Seconds()
	if dt >= 0.15 && done >= t.lastBytes {
		inst := float64(done-t.lastBytes) / dt
		if t.emaBps <= 0 {
			t.emaBps = inst
		} else {
			t.emaBps = 0.35*inst + 0.65*t.emaBps
		}
		t.lastAt = now
		t.lastBytes = done
	} else if t.emaBps <= 0 && done > 0 {
		if elapsed := now.Sub(t.startAt).Seconds(); elapsed >= 0.05 {
			t.emaBps = float64(done) / elapsed
		}
	}
	if t.emaBps < 0 {
		return 0
	}
	return int64(t.emaBps)
}

func (t *transferSpeedTracker) final(total int64) int64 {
	if t.emaBps > 0 {
		return int64(t.emaBps)
	}
	if total <= 0 {
		return 0
	}
	if elapsed := time.Since(t.startAt).Seconds(); elapsed > 0 {
		return int64(float64(total) / elapsed)
	}
	return 0
}

func sanitizeWinName(name string) string {
	trimmed := strings.TrimSpace(name)
	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	cleaned := strings.TrimRight(b.String(), ". ")
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "download"
	}
	base := cleaned
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "_" + cleaned
	}
	return cleaned
}

func (a *App) emitTransferRich(tid, sessionID, name, direction string, transferred, total, speedBps int64, status, errMsg, localPath, remotePath, currentItem string) {
	if a.ctx == nil {
		return
	}
	payload := map[string]any{
		"transferId":  tid,
		"sessionId":   sessionID,
		"name":        name,
		"direction":   direction,
		"transferred": transferred,
		"total":       total,
		"status":      status,
		"error":       errMsg,
	}
	if speedBps > 0 {
		payload["speedBps"] = speedBps
	}
	if localPath != "" {
		payload["localPath"] = localPath
	}
	if remotePath != "" {
		payload["remotePath"] = remotePath
	}
	if currentItem != "" {
		payload["currentItem"] = currentItem
	}
	runtime.EventsEmit(a.ctx, "sftp:progress", payload)
}

func (a *App) PickUploadFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择要上传的文件夹",
	})
}

func (a *App) downloadRemoteDirInto(s *remote.Session, id, remoteDir, destDir, label string) error {
	tid, ctx, cleanup := a.registerTransfer()
	defer cleanup()

	speed := newTransferSpeedTracker()
	a.emitTransferRich(tid, id, label, "download", 0, 0, 0, "running", "", destDir, remoteDir, "")

	var lastDone, lastTotal int64
	var lastItem string
	lastEmit := time.Time{}

	_, err := s.DownloadDir(ctx, remoteDir, destDir, true, func(done, total int64, currentItem string) {
		lastDone = done
		if total > 0 {
			lastTotal = total
		}
		now := time.Now()
		if currentItem == lastItem && !lastEmit.IsZero() && now.Sub(lastEmit) < 80*time.Millisecond && done < total {
			return
		}
		lastItem = currentItem
		lastEmit = now
		a.emitTransferRich(tid, id, label, "download", done, total, speed.sample(done), "running", "", destDir, remoteDir, currentItem)
	})
	if err != nil {
		status := "error"
		if errors.Is(err, context.Canceled) {
			status = "canceled"
		}
		a.emitTransferRich(tid, id, label, "download", lastDone, lastTotal, 0, status, err.Error(), destDir, remoteDir, lastItem)
		return err
	}
	finalTotal := lastTotal
	if finalTotal <= 0 {
		finalTotal = lastDone
	}
	a.emitTransferRich(tid, id, label, "download", finalTotal, finalTotal, speed.final(finalTotal), "done", "", destDir, remoteDir, "")
	return nil
}

func (a *App) downloadRemoteFileInto(s *remote.Session, id, remotePath, localPath, label string) error {
	tid, ctx, cleanup := a.registerTransfer()
	defer cleanup()

	speed := newTransferSpeedTracker()
	a.emitTransferRich(tid, id, label, "download", 0, 0, 0, "running", "", localPath, remotePath, "")

	var lastDone, lastTotal int64
	err := s.DownloadFile(ctx, remotePath, localPath, true, func(done, total int64) {
		lastDone = done
		if total > 0 {
			lastTotal = total
		}
		a.emitTransferRich(tid, id, label, "download", done, total, speed.sample(done), "running", "", localPath, remotePath, "")
	})
	if err != nil {
		status := "error"
		if errors.Is(err, context.Canceled) {
			status = "canceled"
		}
		a.emitTransferRich(tid, id, label, "download", lastDone, lastTotal, 0, status, err.Error(), localPath, remotePath, "")
		return err
	}
	finalTotal := lastTotal
	if finalTotal <= 0 {
		finalTotal = lastDone
	}
	a.emitTransferRich(tid, id, label, "download", finalTotal, finalTotal, speed.final(finalTotal), "done", "", localPath, remotePath, "")
	return nil
}

func (a *App) DownloadDirectory(id, remoteDir string) (string, error) {
	s, err := a.get(id)
	if err != nil {
		return "", err
	}
	remoteDir = strings.TrimSpace(remoteDir)
	if remoteDir == "" {
		return "", errors.New("请输入有效的远端目录路径")
	}
	destParent, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择下载保存目录",
	})
	if err != nil || destParent == "" {
		return "", err
	}
	folderName := sanitizeWinName(path.Base(path.Clean(remoteDir)))
	destDir := filepath.Join(destParent, folderName)
	if err := a.downloadRemoteDirInto(s, id, remoteDir, destDir, folderName); err != nil {
		return "", err
	}
	return destDir, nil
}

func (a *App) BatchDownloadRemote(id string, remotePaths []string) (string, error) {
	s, err := a.get(id)
	if err != nil {
		return "", err
	}
	cleaned := make([]string, 0, len(remotePaths))
	for _, rp := range remotePaths {
		if trimmed := strings.TrimSpace(rp); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return "", nil
	}
	destParent, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择批量下载保存目录",
	})
	if err != nil || destParent == "" {
		return "", err
	}

	var firstErr error
	for _, rp := range cleaned {
		detail, statErr := s.StatDetail(rp)
		if statErr != nil {
			if firstErr == nil {
				firstErr = statErr
			}
			continue
		}
		isDir := detail.IsDir
		if detail.IsSymlink {
			if resolved, linkErr := s.ResolveFileLink(rp); linkErr == nil {
				isDir = resolved.IsDir
			}
		}
		leaf := sanitizeWinName(path.Base(path.Clean(rp)))
		localDest := filepath.Join(destParent, leaf)
		var dlErr error
		if isDir {
			dlErr = a.downloadRemoteDirInto(s, id, rp, localDest, leaf)
		} else {
			dlErr = a.downloadRemoteFileInto(s, id, rp, localDest, leaf)
		}
		if dlErr != nil {
			if firstErr == nil {
				firstErr = dlErr
			}
			if errors.Is(dlErr, context.Canceled) {
				break
			}
		}
	}
	return destParent, firstErr
}

func (a *App) DeleteRemoteBatch(id string, paths []string) (int, error) {
	s, err := a.get(id)
	if err != nil {
		return 0, err
	}
	deleted := 0
	var firstErr error
	for _, target := range paths {
		trimmed := strings.TrimSpace(target)
		if trimmed == "" {
			continue
		}
		if delErr := s.Delete(trimmed); delErr != nil {
			if firstErr == nil {
				firstErr = delErr
			}
			continue
		}
		deleted++
	}
	return deleted, firstErr
}

func (a *App) ChmodRemoteRecursive(id, remotePath string, mode uint32, recursive bool) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.ChmodRecursive(a.baseContext(), remotePath, mode, recursive)
}

func (a *App) CopyRemote(id, srcPath, dstPath string) error {
	s, err := a.get(id)
	if err != nil {
		return err
	}
	return s.CopyRemote(a.baseContext(), srcPath, dstPath)
}

func (a *App) StatRemote(id, remotePath string) (remote.EntryDetail, error) {
	s, err := a.get(id)
	if err != nil {
		return remote.EntryDetail{}, err
	}
	return s.StatDetail(remotePath)
}

func validateRevealLocalPath(localPath string) (string, os.FileInfo, error) {
	trimmed := strings.TrimSpace(localPath)
	if trimmed == "" || strings.ContainsRune(trimmed, 0) || strings.ContainsRune(trimmed, '"') {
		return "", nil, errors.New("无效的本地路径")
	}
	for _, seg := range strings.FieldsFunc(trimmed, func(r rune) bool { return r == '\\' || r == '/' }) {
		if seg == ".." {
			return "", nil, errors.New("路径不允许包含上级目录跳转")
		}
	}
	if !filepath.IsAbs(trimmed) {
		return "", nil, errors.New("仅支持打开本地绝对路径")
	}
	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", nil, errors.New("本地文件或目录不存在")
	}
	return absPath, info, nil
}

func (a *App) RevealLocalPath(localPath string) error {
	absPath, info, err := validateRevealLocalPath(localPath)
	if err != nil {
		return err
	}
	explorer := "explorer.exe"
	if sysRoot := os.Getenv("SystemRoot"); sysRoot != "" {
		candidate := filepath.Join(sysRoot, "explorer.exe")
		if _, statErr := os.Stat(candidate); statErr == nil {
			explorer = candidate
		}
	}
	var cmd *exec.Cmd
	if info.IsDir() {
		cmd = exec.Command(explorer, absPath)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	} else {
		cmd = exec.Command(explorer)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow: true,
			CmdLine:    fmt.Sprintf(`"%s" /select,"%s"`, explorer, absPath),
		}
	}
	return cmd.Start()
}
