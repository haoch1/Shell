package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
)

type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"isDir"`
	IsSymlink bool   `json:"isSymlink"`
	Size      int64  `json:"size"`
	Mode      uint32 `json:"mode"`
	Perm      string `json:"perm"`
	ModTime   int64  `json:"modTime"`
}

type ProgressFunc func(transferred, total int64)

func (s *Session) fileClient() (*sftp.Client, error) {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if s.closed.Load() {
		return nil, net.ErrClosed
	}
	if s.files == nil {
		c, err := sftp.NewClient(s.client)
		if err != nil {
			return nil, fmt.Errorf("启动 SFTP 子系统失败：%w", err)
		}
		s.files = c
	}
	return s.files, nil
}

// RealPath resolves `.` or `~` or relative paths to the canonical remote absolute path.
func (s *Session) RealPath(dir string) (string, error) {
	c, err := s.fileClient()
	if err != nil {
		return "", err
	}
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "~" {
		dir = "."
	}
	resolved, err := c.RealPath(dir)
	if err != nil {
		return dir, err
	}
	return resolved, nil
}

func (s *Session) List(dir string) ([]Entry, error) {
	c, err := s.fileClient()
	if err != nil {
		return nil, err
	}
	if dir == "" || dir == "~" {
		dir = "."
	}
	base := dir
	if resolved, err := c.RealPath(dir); err == nil && resolved != "" {
		base = resolved
	}
	items, err := c.ReadDir(base)
	if err != nil {
		return nil, err
	}
	result := make([]Entry, 0, len(items))
	for _, item := range items {
		name := item.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			continue
		}
		fullPath := path.Join(base, name)
		isSymlink := item.Mode()&os.ModeSymlink != 0
		isDir := item.IsDir()
		size := item.Size()
		if isSymlink {
			if targetInfo, err := c.Stat(fullPath); err == nil {
				isDir = targetInfo.IsDir()
				size = targetInfo.Size()
			}
		}
		result = append(result, Entry{
			Name:      name,
			Path:      fullPath,
			IsDir:     isDir,
			IsSymlink: isSymlink,
			Size:      size,
			Mode:      uint32(item.Mode().Perm()),
			Perm:      item.Mode().String(),
			ModTime:   item.ModTime().Unix(),
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return result, nil
}

func checkDestination(_ os.FileInfo, err error) error {
	if err == nil {
		return errors.New("目标已存在，请选择其他文件名")
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type progressReader struct {
	ctx       context.Context
	r         io.Reader
	total     int64
	done      int64
	lastEmit  time.Time
	onProg    ProgressFunc
}

func (p *progressReader) Read(buf []byte) (int, error) {
	if p.ctx != nil && p.ctx.Err() != nil {
		return 0, p.ctx.Err()
	}
	n, err := p.r.Read(buf)
	if n > 0 {
		p.done += int64(n)
		if p.onProg != nil && (time.Since(p.lastEmit) >= 80*time.Millisecond || p.done >= p.total) {
			p.lastEmit = time.Now()
			p.onProg(p.done, p.total)
		}
	}
	return n, err
}

// Upload uploads a local regular file without overwriting existing remote targets (backward-compatible signature).
func (s *Session) Upload(local, remote string) error {
	return s.UploadFile(context.Background(), local, remote, false, nil)
}

// UploadFile uploads a local regular file to remote path with progress reporting and optional overwrite.
func (s *Session) UploadFile(ctx context.Context, localPath, remotePath string, overwrite bool, onProgress ProgressFunc) (err error) {
	src, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("只能上传普通文件")
	}
	if remotePath == "" || strings.HasSuffix(remotePath, "/") || strings.ContainsRune(remotePath, 0) {
		return errors.New("请输入远端目标文件名")
	}
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	if !overwrite {
		if err := checkDestination(c.Lstat(remotePath)); err != nil {
			return err
		}
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(remotePath), ".shell-upload-"+hex.EncodeToString(id[:])+".part")
	dst, err := c.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = c.Remove(tmp)
		}
	}()
	if onProgress != nil {
		onProgress(0, info.Size())
	}
	reader := &progressReader{ctx: ctx, r: src, total: info.Size(), onProg: onProgress}
	n, copyErr := io.Copy(dst, reader)
	closeErr := dst.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != info.Size() {
		return errors.New("上传期间本地文件的大小发生变化")
	}
	if overwrite {
		if err = c.PosixRename(tmp, remotePath); err != nil {
			_ = c.Remove(remotePath)
			err = c.Rename(tmp, remotePath)
		}
	} else {
		err = c.Rename(tmp, remotePath)
	}
	if err == nil && onProgress != nil {
		onProgress(info.Size(), info.Size())
	}
	return err
}

// Download downloads a remote regular file without overwriting existing local targets (backward-compatible signature).
func (s *Session) Download(remote, local string) error {
	return s.DownloadFile(context.Background(), remote, local, false, nil)
}

// DownloadFile downloads a remote regular file with progress reporting and optional overwrite.
func (s *Session) DownloadFile(ctx context.Context, remotePath, localPath string, overwrite bool, onProgress ProgressFunc) error {
	if !overwrite {
		if err := checkDestination(os.Lstat(localPath)); err != nil {
			return err
		}
	}
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	src, err := c.Open(remotePath)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("只能下载普通文件")
	}
	temp, err := os.CreateTemp(filepath.Dir(localPath), ".shell-download-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if onProgress != nil {
		onProgress(0, info.Size())
	}
	reader := &progressReader{ctx: ctx, r: src, total: info.Size(), onProg: onProgress}
	n, err := io.Copy(temp, reader)
	if err == nil && n != info.Size() {
		err = errors.New("传输未完成，远端文件大小可能已变化")
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := publishDownloadMode(temp.Name(), localPath, overwrite); err != nil {
		return err
	}
	if onProgress != nil {
		onProgress(info.Size(), info.Size())
	}
	return nil
}

func (s *Session) Mkdir(remoteDir string) error {
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	remoteDir = strings.TrimSpace(remoteDir)
	if remoteDir == "" || strings.ContainsRune(remoteDir, 0) {
		return errors.New("请输入有效的目录路径")
	}
	return c.MkdirAll(remoteDir)
}

func (s *Session) CreateEmptyFile(remotePath string) error {
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" || strings.HasSuffix(remotePath, "/") || strings.ContainsRune(remotePath, 0) {
		return errors.New("请输入有效的文件路径")
	}
	f, err := c.OpenFile(remotePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	return f.Close()
}

func (s *Session) Rename(oldPath, newPath string) error {
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	if oldPath == "" || newPath == "" || strings.ContainsRune(oldPath, 0) || strings.ContainsRune(newPath, 0) {
		return errors.New("请输入有效的路径")
	}
	return c.Rename(oldPath, newPath)
}

func (s *Session) Chmod(remotePath string, mode uint32) error {
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	if mode > 0o7777 {
		return errors.New("权限值必须在 0000 到 7777 之间")
	}
	return c.Chmod(remotePath, os.FileMode(mode))
}

func (s *Session) Delete(targetPath string) error {
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	cleaned := path.Clean(strings.TrimSpace(targetPath))
	if cleaned == "" || cleaned == "/" || cleaned == "." || cleaned == ".." {
		return errors.New("禁止删除根目录或相对当前目录")
	}
	info, err := c.Lstat(cleaned)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return c.Remove(cleaned)
	}
	return removeDirRecursive(c, cleaned, 0)
}

func removeDirRecursive(c *sftp.Client, dir string, depth int) error {
	if depth > 64 {
		return errors.New("目录嵌套过深，已停止递归删除")
	}
	entries, err := c.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "." || name == ".." {
			continue
		}
		child := path.Join(dir, name)
		if entry.IsDir() && entry.Mode()&os.ModeSymlink == 0 {
			if err := removeDirRecursive(c, child, depth+1); err != nil {
				return err
			}
		} else {
			if err := c.Remove(child); err != nil {
				return err
			}
		}
	}
	return c.RemoveDirectory(dir)
}

const maxEditableFileSize = 2 << 20

// ReadText reads a UTF-8 text file up to 2 MiB for the built-in quick config editor.
func (s *Session) ReadText(remotePath string) (string, error) {
	c, err := s.fileClient()
	if err != nil {
		return "", err
	}
	f, err := c.Open(remotePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("仅支持编辑普通文本文件")
	}
	if info.Size() > maxEditableFileSize {
		return "", errors.New("文件超过 2 MiB，请下载后在本地编辑")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEditableFileSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxEditableFileSize {
		return "", errors.New("文件超过 2 MiB，请下载后在本地编辑")
	}
	if !utf8.Valid(data) {
		return "", errors.New("该文件为二进制或非 UTF-8 编码，暂不支持内置编辑")
	}
	return string(data), nil
}

// WriteText saves text back to a remote file atomically while preserving its existing permissions where possible.
func (s *Session) WriteText(remotePath, content string) (err error) {
	if len(content) > maxEditableFileSize {
		return errors.New("文本内容超过 2 MiB 限制")
	}
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	var perm os.FileMode = 0644
	if oldInfo, statErr := c.Stat(remotePath); statErr == nil {
		perm = oldInfo.Mode().Perm()
	}
	var id [12]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(remotePath), ".shell-edit-"+hex.EncodeToString(id[:])+".tmp")
	f, err := c.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = c.Remove(tmp)
		}
	}()
	if _, err = io.WriteString(f, content); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	_ = c.Chmod(tmp, perm)
	if err = c.PosixRename(tmp, remotePath); err != nil {
		// Fallback when POSIX rename extension is unavailable: write directly in place.
		_ = c.Remove(tmp)
		direct, openErr := c.OpenFile(remotePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
		if openErr != nil {
			return openErr
		}
		_, writeErr := io.WriteString(direct, content)
		closeErr := direct.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	return nil
}
