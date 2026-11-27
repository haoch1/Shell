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

type DirectoryListing struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

type ProgressFunc func(transferred, total int64)

func (s *Session) fileClient() (*sftp.Client, error) {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if s.closed.Load() {
		return nil, net.ErrClosed
	}
	if s.files == nil {
		c, err := sftp.NewClient(s.client, sftp.UseFstat(true))
		if err != nil {
			return nil, fmt.Errorf("启动 SFTP 子系统失败：%w", err)
		}
		s.files = c
	}
	return s.files, nil
}

// maybeResetFileClient clears a dead SFTP subsystem client so subsequent operations reopen it cleanly.
func (s *Session) maybeResetFileClient(c *sftp.Client, err error) {
	if c == nil || err == nil || s.closed.Load() {
		return
	}
	if !errors.Is(err, sftp.ErrSSHFxConnectionLost) {
		return
	}
	s.fileMu.Lock()
	if s.files == c {
		s.files = nil
	}
	s.fileMu.Unlock()
	_ = c.Close()
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
		s.maybeResetFileClient(c, err)
		return dir, err
	}
	return resolved, nil
}

func (s *Session) List(dir string) ([]Entry, error) {
	listing, err := s.readDirectory(context.Background(), dir, true)
	if err != nil {
		return nil, err
	}
	result := listing.Entries
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return result, nil
}

// BrowseDirectory returns READDIR attributes. The GUI sorts them and resolves a link only when opened.
func (s *Session) BrowseDirectory(ctx context.Context, dir string) (DirectoryListing, error) {
	return s.readDirectory(ctx, dir, false)
}

func (s *Session) readDirectory(ctx context.Context, dir string, followLinks bool) (DirectoryListing, error) {
	if err := ctx.Err(); err != nil {
		return DirectoryListing{}, err
	}
	c, err := s.fileClient()
	if err != nil {
		return DirectoryListing{}, err
	}
	if dir == "" || dir == "~" {
		dir = "."
	}
	base := dir
	if resolved, err := c.RealPath(dir); err == nil && resolved != "" {
		base = resolved
	}
	if err := ctx.Err(); err != nil {
		return DirectoryListing{}, err
	}
	items, err := c.ReadDirContext(ctx, base)
	if err != nil {
		s.maybeResetFileClient(c, err)
		return DirectoryListing{}, err
	}
	result := make([]Entry, 0, len(items))
	for _, item := range items {
		name := item.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			continue
		}
		fullPath := path.Join(base, name)
		entry := directoryEntry(fullPath, item)
		if followLinks && entry.IsSymlink {
			if targetInfo, err := c.Stat(fullPath); err == nil {
				entry.IsDir = targetInfo.IsDir()
				entry.Size = targetInfo.Size()
			}
		}
		result = append(result, entry)
	}
	return DirectoryListing{Path: base, Entries: result}, nil
}

func directoryEntry(fullPath string, item os.FileInfo) Entry {
	return Entry{
		Name:      path.Base(fullPath),
		Path:      fullPath,
		IsDir:     item.IsDir(),
		IsSymlink: item.Mode()&os.ModeSymlink != 0,
		Size:      item.Size(),
		Mode:      uint32(item.Mode().Perm()),
		Perm:      item.Mode().String(),
		ModTime:   item.ModTime().Unix(),
	}
}

func (s *Session) ResolveFileLink(fullPath string) (Entry, error) {
	c, err := s.fileClient()
	if err != nil {
		return Entry{}, err
	}
	target, err := c.Stat(fullPath)
	if err != nil {
		return Entry{}, err
	}
	entry := directoryEntry(fullPath, target)
	entry.IsSymlink = true
	return entry, nil
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

// replaceRemote publishes a completed sibling file. A server without POSIX
// rename can only replace a file in steps, so retain the old contents until
// the final name is in place and report the recovery name on rollback failure.
func replaceRemote(c *sftp.Client, tmp, target string) error {
	if _, ok := c.HasExtension("posix-rename@openssh.com"); ok {
		err := c.PosixRename(tmp, target)
		var status *sftp.StatusError
		if !errors.As(err, &status) || status.FxCode() != sftp.ErrSSHFxOpUnsupported {
			return err
		}
	}
	info, err := c.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return c.Rename(tmp, target)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return errors.New("只允许覆盖普通文件或符号链接")
	}
	backup := tmp + ".old"
	if err := c.Rename(target, backup); err != nil {
		return err
	}
	if err := c.Rename(tmp, target); err != nil {
		if restoreErr := c.Rename(backup, target); restoreErr != nil {
			return fmt.Errorf("写入失败：%w；恢复失败：%v。原文件备份路径：%s", err, restoreErr, backup)
		}
		return err
	}
	if err := c.Remove(backup); err != nil {
		return fmt.Errorf("新文件已保存，未能清理原文件备份 %s：%w", backup, err)
	}
	return nil
}

// pkg/sftp's default packet payload is 32 KiB. One packet never needs a pipeline.
const sftpPacketSize = 32 << 10

type transferProgress struct {
	ctx      context.Context
	total    int64
	done     int64
	lastEmit time.Time
	onProg   ProgressFunc
}

func (p *transferProgress) advance(n int) {
	p.done += int64(n)
	// Only the caller can report completion after closing and publishing the temporary file.
	if n > 0 && p.onProg != nil && p.done < p.total && time.Since(p.lastEmit) >= 80*time.Millisecond {
		p.lastEmit = time.Now()
		p.onProg(p.done, p.total)
	}
}

type progressReader struct {
	*transferProgress
	r io.Reader
}

func (p *progressReader) Read(buf []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(buf)
	p.advance(n)
	return n, err
}

type progressWriter struct {
	*transferProgress
	w io.Writer
}

func (p *progressWriter) Write(buf []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.w.Write(buf)
	p.advance(n)
	return n, err
}

func (p *transferProgress) upload(dst *sftp.File, src io.Reader) (int64, error) {
	reader := &progressReader{p, src}
	if p.total <= sftpPacketSize {
		return dst.ReadFrom(reader)
	}
	// Limit in-flight writes on this exclusive temporary file. Other SFTP writes stay sequential.
	workers := min(int64(64), 1+(p.total-1)/sftpPacketSize)
	return dst.ReadFromWithConcurrency(reader, int(workers))
}

func (p *transferProgress) download(dst io.Writer, src *sftp.File) (int64, error) {
	writer := &progressWriter{p, dst}
	if p.total <= sftpPacketSize {
		// Avoid an extra FSTAT to estimate worker count. Also detect growth of the source.
		return io.Copy(writer, io.LimitReader(src, p.total+1))
	}
	return src.WriteTo(writer)
}

// Upload uploads a local regular file without overwriting existing remote targets (backward-compatible signature).
func (s *Session) Upload(local, remote string) error {
	return s.UploadFile(context.Background(), local, remote, false, nil)
}

// UploadFile uploads a local regular file to remote path with progress reporting and optional overwrite.
func (s *Session) UploadFile(ctx context.Context, localPath, remotePath string, overwrite bool, onProgress ProgressFunc) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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
	progress := &transferProgress{ctx: ctx, total: info.Size(), onProg: onProgress}
	n, copyErr := progress.upload(dst, src)
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
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if overwrite {
		err = replaceRemote(c, tmp, remotePath)
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
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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
	progress := &transferProgress{ctx: ctx, total: info.Size(), onProg: onProgress}
	n, err := progress.download(temp, src)
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
	if ctx.Err() != nil {
		return ctx.Err()
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

// WriteText stages the new contents before replacing the remote file.
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
	if err = c.Chmod(tmp, perm); err != nil {
		f.Close()
		return fmt.Errorf("无法保留文件权限：%w", err)
	}
	if _, err = io.WriteString(f, content); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return replaceRemote(c, tmp, remotePath)
}
