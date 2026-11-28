package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

type EntryDetail struct {
	Entry
	LinkTarget string `json:"linkTarget,omitempty"`
	Octal      string `json:"octal"`
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
	AccessTime int64  `json:"accessTime,omitempty"`
	ItemCount  int    `json:"itemCount,omitempty"`
}

const maxDirWalkDepth = 64

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
	entry, err := s.StatEntry(fullPath)
	if err == nil {
		entry.IsSymlink = true
	}
	return entry, err
}

// StatEntry follows a selected link without reading its target or listing a directory.
func (s *Session) StatEntry(fullPath string) (Entry, error) {
	c, err := s.fileClient()
	if err != nil {
		return Entry{}, err
	}
	target, err := c.Stat(fullPath)
	if err != nil {
		s.maybeResetFileClient(c, err)
		return Entry{}, err
	}
	return directoryEntry(fullPath, target), nil
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

func posixToFileMode(mode uint32) os.FileMode {
	fm := os.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		fm |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		fm |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		fm |= os.ModeSticky
	}
	return fm
}

func (s *Session) Chmod(remotePath string, mode uint32) error {
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	if mode > 0o7777 {
		return errors.New("权限值必须在 0000 到 7777 之间")
	}
	return c.Chmod(remotePath, posixToFileMode(mode))
}

func (s *Session) Delete(targetPath string) error {
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	cleaned := path.Clean(targetPath)
	if cleaned == "/" || cleaned == "." || cleaned == ".." || strings.ContainsRune(targetPath, 0) {
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
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
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

const maxEditableFileSize = 4 << 20

// ReadText reads a UTF-8 text file up to 4 MiB for the built-in quick config editor.
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
		return "", errors.New("文件超过 4 MiB，请下载后在本地编辑")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEditableFileSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxEditableFileSize {
		return "", errors.New("文件超过 4 MiB，请下载后在本地编辑")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("该文件包含二进制空字节（NUL），暂不支持内置编辑")
	}
	if !utf8.Valid(data) {
		return "", errors.New("该文件不是有效的 UTF-8 文本编码，暂不支持内置编辑")
	}
	return string(data), nil
}

// A staged save must publish beside the final target to preserve links opened in the editor.
func editorTarget(c *sftp.Client, remotePath string, followLinks bool) (string, os.FileMode, error) {
	if remotePath == "" || strings.HasSuffix(remotePath, "/") || strings.ContainsRune(remotePath, 0) {
		return "", 0, errors.New("请输入有效的远端文件路径")
	}
	var seen map[string]bool
	for hops := 0; hops <= 40; hops++ {
		info, err := c.Lstat(remotePath)
		if hops == 0 && errors.Is(err, os.ErrNotExist) {
			return remotePath, 0644, nil
		}
		if err != nil {
			return "", 0, err
		}
		if info.Mode().IsRegular() {
			return remotePath, info.Mode().Perm(), nil
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return "", 0, errors.New("仅支持编辑普通文本文件")
		}
		if !followLinks {
			return "", 0, errors.New("所选文件是符号链接，请直接指定目标文件")
		}
		if seen[remotePath] {
			return "", 0, errors.New("符号链接循环，请检查远端文件")
		}
		if seen == nil {
			seen = make(map[string]bool)
		}
		seen[remotePath] = true
		target, err := c.ReadLink(remotePath)
		if err != nil {
			return "", 0, err
		}
		if !path.IsAbs(target) {
			dir, _ := path.Split(remotePath)
			target = dir + target // Keep ".." for the server to resolve across directory symlinks.
		}
		remotePath = target
	}
	return "", 0, errors.New("符号链接层级过深，请检查远端文件")
}

// WriteText publishes at the specified path and rejects a final symbolic link.
func (s *Session) WriteText(remotePath, content string) error {
	return s.writeText(remotePath, content, false)
}

// EditText saves to the same file the interactive editor opened through a link.
func (s *Session) EditText(remotePath, content string) error {
	return s.writeText(remotePath, content, true)
}

func (s *Session) writeText(remotePath, content string, followLinks bool) (err error) {
	if len(content) > maxEditableFileSize {
		return errors.New("文本内容超过 4 MiB 限制")
	}
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	remotePath, perm, err := editorTarget(c, remotePath, followLinks)
	if err != nil {
		return err
	}
	var id [12]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	dir, _ := path.Split(remotePath)
	tmp := dir + ".shell-edit-" + hex.EncodeToString(id[:]) + ".tmp"
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

func posixPermBits(info os.FileInfo) uint32 {
	if st, ok := info.Sys().(*sftp.FileStat); ok && st != nil && st.Mode != 0 {
		return st.Mode & 0o7777
	}
	m := uint32(info.Mode().Perm())
	if info.Mode()&os.ModeSetuid != 0 {
		m |= 0o4000
	}
	if info.Mode()&os.ModeSetgid != 0 {
		m |= 0o2000
	}
	if info.Mode()&os.ModeSticky != 0 {
		m |= 0o1000
	}
	return m
}

// StatDetail inspects a remote path via Lstat and collects symlink target, octal mode, UID/GID, access time, and direct child count.
func (s *Session) StatDetail(remotePath string) (EntryDetail, error) {
	if remotePath == "" || strings.ContainsRune(remotePath, 0) {
		return EntryDetail{}, errors.New("请输入有效的远端路径")
	}
	c, err := s.fileClient()
	if err != nil {
		return EntryDetail{}, err
	}
	cleanPath := path.Clean(remotePath)
	if remotePath == "~" || remotePath == "." {
		if resolved, rpErr := c.RealPath("."); rpErr == nil && resolved != "" {
			cleanPath = resolved
		}
	}
	info, err := c.Lstat(cleanPath)
	if err != nil {
		s.maybeResetFileClient(c, err)
		return EntryDetail{}, err
	}
	permBits := posixPermBits(info)
	detail := EntryDetail{
		Entry: directoryEntry(cleanPath, info),
		Octal: fmt.Sprintf("%04o", permBits),
	}
	if st, ok := info.Sys().(*sftp.FileStat); ok && st != nil {
		detail.UID = st.UID
		detail.GID = st.GID
		if st.Atime > 0 {
			detail.AccessTime = int64(st.Atime)
		}
	}
	if detail.IsSymlink {
		if target, linkErr := c.ReadLink(cleanPath); linkErr == nil {
			detail.LinkTarget = target
		}
	}
	if detail.IsDir && !detail.IsSymlink {
		if items, dirErr := c.ReadDir(cleanPath); dirErr == nil {
			count := 0
			for _, it := range items {
				name := it.Name()
				if name == "" || name == "." || name == ".." {
					continue
				}
				count++
			}
			detail.ItemCount = count
		}
	}
	return detail, nil
}

func isWindowsReservedName(name string) bool {
	base := name
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	switch strings.ToUpper(strings.TrimSpace(base)) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

func validateLocalEntryName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("远端文件名无效：%q", name)
	}
	if strings.ContainsAny(name, `<>:"/\|?*`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("远端文件名不适用于 Windows：%s", name)
	}
	for _, r := range name {
		if r < 0x20 {
			return fmt.Errorf("远端文件名包含非法控制字符：%s", name)
		}
	}
	if strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("远端文件名以空格或点号结尾，不适用于 Windows：%s", name)
	}
	if isWindowsReservedName(name) {
		return fmt.Errorf("远端文件名为 Windows 保留设备名：%s", name)
	}
	return nil
}

func ensureWithinDir(baseDir, targetPath string) error {
	rel, err := filepath.Rel(baseDir, targetPath)
	if err != nil {
		return fmt.Errorf("无效的本地目标路径：%w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("检测到非法路径穿越：%s", targetPath)
	}
	return nil
}

type localUploadItem struct {
	localPath string
	relPath   string
	size      int64
}

// UploadDir recursively uploads a local directory to remoteDir, skipping symlinks and reporting aggregate progress.
func (s *Session) UploadDir(ctx context.Context, localDir, remoteDir string, overwrite bool, onProgress func(done, total int64, currentItem string)) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	localDir = filepath.Clean(strings.TrimSpace(localDir))
	if localDir == "" || localDir == "." {
		return 0, errors.New("请输入有效的本地目录路径")
	}
	if remoteDir == "" || strings.ContainsRune(remoteDir, 0) {
		return 0, errors.New("请输入有效的远端目录路径")
	}
	rootInfo, err := os.Stat(localDir)
	if err != nil {
		return 0, err
	}
	if !rootInfo.IsDir() {
		return 0, errors.New("只能上传本地目录")
	}
	c, err := s.fileClient()
	if err != nil {
		return 0, err
	}

	var dirs []string
	var files []localUploadItem
	var totalBytes int64
	walkErr := filepath.WalkDir(localDir, func(curPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		rel, relErr := filepath.Rel(localDir, curPath)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		// Skip symlinks (both files and symlinked directories) to avoid cycles.
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if strings.ContainsRune(relSlash, 0) {
			return fmt.Errorf("文件名包含非法空字符：%s", d.Name())
		}
		depth := strings.Count(relSlash, "/") + 1
		if depth > maxDirWalkDepth {
			return errors.New("目录嵌套过深，已停止递归上传")
		}
		if d.IsDir() {
			dirs = append(dirs, relSlash)
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, localUploadItem{
			localPath: curPath,
			relPath:   relSlash,
			size:      info.Size(),
		})
		totalBytes += info.Size()
		return nil
	})
	if walkErr != nil {
		return 0, walkErr
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := c.MkdirAll(remoteDir); err != nil {
		s.maybeResetFileClient(c, err)
		return 0, err
	}
	if onProgress != nil {
		onProgress(0, totalBytes, "")
	}
	for _, relDir := range dirs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := c.MkdirAll(path.Join(remoteDir, relDir)); err != nil {
			s.maybeResetFileClient(c, err)
			return 0, err
		}
	}

	var completedBytes int64
	uploaded := 0
	for _, item := range files {
		if err := ctx.Err(); err != nil {
			return uploaded, err
		}
		target := path.Join(remoteDir, item.relPath)
		if onProgress != nil {
			onProgress(completedBytes, totalBytes, item.relPath)
		}
		baseBytes := completedBytes
		upErr := s.UploadFile(ctx, item.localPath, target, overwrite, func(fileDone, _ int64) {
			if onProgress == nil {
				return
			}
			if fileDone <= 0 || fileDone >= item.size {
				return
			}
			agg := baseBytes + fileDone
			if agg >= totalBytes && totalBytes > 0 {
				agg = totalBytes - 1
			}
			onProgress(agg, totalBytes, item.relPath)
		})
		if upErr != nil {
			return uploaded, upErr
		}
		completedBytes += item.size
		uploaded++
		if onProgress != nil && completedBytes < totalBytes {
			onProgress(completedBytes, totalBytes, item.relPath)
		}
	}
	if onProgress != nil {
		onProgress(totalBytes, totalBytes, "")
	}
	return uploaded, nil
}

type remoteDownloadItem struct {
	remotePath string
	relPath    string
	size       int64
}

func walkRemoteDir(ctx context.Context, c *sftp.Client, curRemote, relPrefix string, depth int, dirs *[]string, files *[]remoteDownloadItem, totalBytes *int64) error {
	if depth > maxDirWalkDepth {
		return errors.New("目录嵌套过深，已停止递归下载")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	items, err := c.ReadDirContext(ctx, curRemote)
	if err != nil {
		return err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Name() < items[j].Name()
	})
	localNames := make(map[string]bool, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := item.Name()
		if name == "." || name == ".." {
			continue
		}
		// Skip symlinks to avoid cycles or unexpected escapes.
		if !item.IsDir() && !item.Mode().IsRegular() {
			continue
		}
		if err := validateLocalEntryName(name); err != nil {
			return err
		}
		key := strings.ToUpper(name)
		if localNames[key] {
			return fmt.Errorf("目录 %q 包含在 Windows 下同名的条目 %q，请分别下载", curRemote, name)
		}
		localNames[key] = true
		childRemote := path.Join(curRemote, name)
		childRel := name
		if relPrefix != "" {
			childRel = relPrefix + "/" + name
		}
		if item.IsDir() {
			*dirs = append(*dirs, childRel)
			if err := walkRemoteDir(ctx, c, childRemote, childRel, depth+1, dirs, files, totalBytes); err != nil {
				return err
			}
		} else {
			*files = append(*files, remoteDownloadItem{
				remotePath: childRemote,
				relPath:    childRel,
				size:       item.Size(),
			})
			*totalBytes += item.Size()
		}
	}
	return nil
}

// DownloadDir recursively downloads a remote directory to localDir, validating Windows filenames and reporting aggregate progress.
func (s *Session) DownloadDir(ctx context.Context, remoteDir, localDir string, overwrite bool, onProgress func(done, total int64, currentItem string)) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	localDir = filepath.Clean(strings.TrimSpace(localDir))
	if remoteDir == "" || strings.ContainsRune(remoteDir, 0) {
		return 0, errors.New("请输入有效的远端目录路径")
	}
	if localDir == "" || localDir == "." {
		return 0, errors.New("请输入有效的本地保存目录")
	}
	c, err := s.fileClient()
	if err != nil {
		return 0, err
	}
	var dirs []string
	var files []remoteDownloadItem
	var totalBytes int64
	if err := walkRemoteDir(ctx, c, path.Clean(remoteDir), "", 0, &dirs, &files, &totalBytes); err != nil {
		s.maybeResetFileClient(c, err)
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return 0, err
	}
	for _, relDir := range dirs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		targetDir := filepath.Join(localDir, filepath.FromSlash(relDir))
		if err := ensureWithinDir(localDir, targetDir); err != nil {
			return 0, err
		}
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return 0, err
		}
	}
	if onProgress != nil {
		onProgress(0, totalBytes, "")
	}

	var completedBytes int64
	downloaded := 0
	for _, item := range files {
		if err := ctx.Err(); err != nil {
			return downloaded, err
		}
		destPath := filepath.Join(localDir, filepath.FromSlash(item.relPath))
		if err := ensureWithinDir(localDir, destPath); err != nil {
			return downloaded, err
		}
		if onProgress != nil {
			onProgress(completedBytes, totalBytes, item.relPath)
		}
		baseBytes := completedBytes
		dlErr := s.DownloadFile(ctx, item.remotePath, destPath, overwrite, func(fileDone, _ int64) {
			if onProgress == nil {
				return
			}
			if fileDone <= 0 || fileDone >= item.size {
				return
			}
			agg := baseBytes + fileDone
			if agg >= totalBytes && totalBytes > 0 {
				agg = totalBytes - 1
			}
			onProgress(agg, totalBytes, item.relPath)
		})
		if dlErr != nil {
			return downloaded, dlErr
		}
		completedBytes += item.size
		downloaded++
		if onProgress != nil && completedBytes < totalBytes {
			onProgress(completedBytes, totalBytes, item.relPath)
		}
	}
	if onProgress != nil {
		onProgress(totalBytes, totalBytes, "")
	}
	return downloaded, nil
}

// ChmodRecursive changes permissions on remotePath and optionally recursively across subdirectories (skipping symlinks).
func (s *Session) ChmodRecursive(ctx context.Context, remotePath string, mode uint32, recursive bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if remotePath == "" || strings.ContainsRune(remotePath, 0) {
		return errors.New("请输入有效的远端路径")
	}
	if mode > 0o7777 {
		return errors.New("权限值必须在 0000 到 7777 之间")
	}
	if !recursive {
		return s.Chmod(remotePath, mode)
	}
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	err = chmodRecursiveWalk(ctx, c, path.Clean(remotePath), posixToFileMode(mode), 0)
	if err != nil {
		s.maybeResetFileClient(c, err)
	}
	return err
}

func chmodRecursiveWalk(ctx context.Context, c *sftp.Client, target string, perm os.FileMode, depth int) error {
	if depth > maxDirWalkDepth {
		return errors.New("目录嵌套过深，已停止递归修改权限")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := c.Lstat(target)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if depth > 0 {
			return nil
		}
		return c.Chmod(target, perm)
	}
	if !info.IsDir() {
		return c.Chmod(target, perm)
	}
	// Apply directory chmod before descending when read+exec are present, or after descending when restricting access.
	if perm&0o500 == 0o500 {
		if err := c.Chmod(target, perm); err != nil {
			return err
		}
	}
	entries, err := c.ReadDirContext(ctx, target)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			continue
		}
		child := path.Join(target, name)
		if entry.IsDir() {
			if err := chmodRecursiveWalk(ctx, c, child, perm, depth+1); err != nil {
				return err
			}
		} else {
			if err := c.Chmod(child, perm); err != nil {
				return err
			}
		}
	}
	if perm&0o500 != 0o500 {
		if err := c.Chmod(target, perm); err != nil {
			return err
		}
	}
	return nil
}

// CopyRemote copies a remote file or directory (recursively, skipping symlink loops) to dstPath.
func (s *Session) CopyRemote(ctx context.Context, srcPath, dstPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(srcPath) == "" || strings.TrimSpace(dstPath) == "" ||
		strings.ContainsRune(srcPath, 0) || strings.ContainsRune(dstPath, 0) {
		return errors.New("请输入有效的源路径和目标路径")
	}
	srcClean := path.Clean(srcPath)
	dstClean := path.Clean(dstPath)
	if srcClean == dstClean {
		return errors.New("目标路径不能与源路径相同")
	}
	srcPrefix := srcClean
	if !strings.HasSuffix(srcPrefix, "/") {
		srcPrefix += "/"
	}
	if strings.HasPrefix(dstClean+"/", srcPrefix) {
		return errors.New("不能将目录复制到其自身或子目录中")
	}
	c, err := s.fileClient()
	if err != nil {
		return err
	}
	srcInfo, err := c.Lstat(srcClean)
	if err != nil {
		s.maybeResetFileClient(c, err)
		return err
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		linkTarget, linkErr := c.ReadLink(srcClean)
		if linkErr != nil {
			return linkErr
		}
		return c.Symlink(linkTarget, dstClean)
	}
	if srcInfo.IsDir() {
		err = copyRemoteDir(ctx, c, srcClean, dstClean, srcInfo, 0)
	} else if srcInfo.Mode().IsRegular() {
		err = copyRemoteFile(ctx, c, srcClean, dstClean, srcInfo)
	} else {
		return errors.New("仅支持复制普通文件、目录或符号链接")
	}
	if err != nil {
		s.maybeResetFileClient(c, err)
	}
	return err
}

func copyRemoteFile(ctx context.Context, c *sftp.Client, srcPath, dstPath string, srcInfo os.FileInfo) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if dstInfo, statErr := c.Lstat(dstPath); statErr == nil && dstInfo.IsDir() {
		return errors.New("目标路径已存在且为目录，无法覆盖")
	}
	src, err := c.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	var id [12]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(dstPath), ".shell-copy-"+hex.EncodeToString(id[:])+".tmp")
	dst, err := c.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = c.Remove(tmp)
		}
	}()

	buf := make([]byte, 64<<10)
	for {
		if err = ctx.Err(); err != nil {
			_ = dst.Close()
			return err
		}
		nr, readErr := src.Read(buf)
		if nr > 0 {
			nw, writeErr := dst.Write(buf[:nr])
			if writeErr != nil {
				_ = dst.Close()
				return writeErr
			}
			if nw != nr {
				_ = dst.Close()
				return io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = dst.Close()
			return readErr
		}
	}
	if err = dst.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	perm := srcInfo.Mode().Perm()
	if perm == 0 {
		perm = 0644
	}
	_ = c.Chmod(tmp, perm)
	return replaceRemote(c, tmp, dstPath)
}

func copyRemoteDir(ctx context.Context, c *sftp.Client, srcDir, dstDir string, srcInfo os.FileInfo, depth int) error {
	if depth > maxDirWalkDepth {
		return errors.New("目录嵌套过深，已停止递归复制")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if dstInfo, statErr := c.Lstat(dstDir); statErr == nil && !dstInfo.IsDir() {
		return errors.New("目标路径已存在且不是目录")
	}
	if err := c.MkdirAll(dstDir); err != nil {
		return err
	}
	entries, err := c.ReadDirContext(ctx, srcDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			continue
		}
		// Skip symlinks inside copied directories to prevent symlink loops.
		if entry.Mode()&os.ModeSymlink != 0 {
			continue
		}
		childSrc := path.Join(srcDir, name)
		childDst := path.Join(dstDir, name)
		if entry.IsDir() {
			if err := copyRemoteDir(ctx, c, childSrc, childDst, entry, depth+1); err != nil {
				return err
			}
		} else if entry.Mode().IsRegular() {
			if err := copyRemoteFile(ctx, c, childSrc, childDst, entry); err != nil {
				return err
			}
		}
	}
	if perm := srcInfo.Mode().Perm(); perm != 0 {
		_ = c.Chmod(dstDir, perm)
	}
	return nil
}
