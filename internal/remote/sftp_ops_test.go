package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// modeTrackingHandlers wraps sftp.InMemHandler() so Setstat persists POSIX permission bits
// on both files and directories and reports realistic UID/GID/Atime metadata.
type modeTrackingHandlers struct {
	base  sftp.Handlers
	mu    sync.RWMutex
	modes map[string]os.FileMode
}

type trackedFileInfo struct {
	os.FileInfo
	modeOverride os.FileMode
	hasOverride  bool
}

func (fi trackedFileInfo) Mode() os.FileMode {
	if !fi.hasOverride {
		return fi.FileInfo.Mode()
	}
	return (fi.FileInfo.Mode() & os.ModeType) | (fi.modeOverride & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
}

func testPosixMode(mode os.FileMode) uint32 {
	ret := uint32(mode & os.ModePerm)
	switch {
	case mode&os.ModeDir != 0:
		ret |= 0o040000
	case mode&os.ModeSymlink != 0:
		ret |= 0o120000
	default:
		ret |= 0o100000
	}
	if mode&os.ModeSetuid != 0 {
		ret |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		ret |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		ret |= 0o1000
	}
	return ret
}

func (fi trackedFileInfo) Sys() any {
	return &sftp.FileStat{
		Size:  uint64(fi.Size()),
		Mode:  testPosixMode(fi.Mode()),
		Mtime: uint32(fi.ModTime().Unix()),
		Atime: uint32(fi.ModTime().Unix()),
		UID:   1000,
		GID:   1001,
	}
}

type trackedLister struct {
	items []os.FileInfo
}

func (l trackedLister) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l.items)) {
		return 0, io.EOF
	}
	n := copy(dst, l.items[offset:])
	if offset+int64(n) >= int64(len(l.items)) {
		return n, io.EOF
	}
	return n, nil
}

func (h *modeTrackingHandlers) wrapInfo(fullPath string, info os.FileInfo) os.FileInfo {
	h.mu.RLock()
	mode, ok := h.modes[path.Clean(fullPath)]
	h.mu.RUnlock()
	return trackedFileInfo{FileInfo: info, modeOverride: mode, hasOverride: ok}
}

func (h *modeTrackingHandlers) Filecmd(r *sftp.Request) error {
	if r.Method == "Setstat" && r.AttrFlags().Permissions {
		clean := path.Clean(r.Filepath)
		if _, err := h.base.FileList.Filelist(&sftp.Request{Method: "Stat", Filepath: clean}); err != nil {
			return err
		}
		h.mu.Lock()
		h.modes[clean] = r.Attributes().FileMode()
		h.mu.Unlock()
		return nil
	}
	if r.Method == "Rename" {
		if err := h.base.FileCmd.Filecmd(r); err != nil {
			return err
		}
		h.mu.Lock()
		if m, ok := h.modes[path.Clean(r.Filepath)]; ok {
			h.modes[path.Clean(r.Target)] = m
			delete(h.modes, path.Clean(r.Filepath))
		}
		h.mu.Unlock()
		return nil
	}
	return h.base.FileCmd.Filecmd(r)
}

func (h *modeTrackingHandlers) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	lister, err := h.base.FileList.Filelist(r)
	if err != nil {
		return nil, err
	}
	if r.Method == "Readlink" {
		return lister, nil
	}
	var all []os.FileInfo
	buf := make([]os.FileInfo, 64)
	var off int64
	for {
		n, listErr := lister.ListAt(buf, off)
		for i := 0; i < n; i++ {
			itemPath := path.Clean(r.Filepath)
			if r.Method == "List" {
				itemPath = path.Join(itemPath, buf[i].Name())
			}
			all = append(all, h.wrapInfo(itemPath, buf[i]))
		}
		off += int64(n)
		if listErr == io.EOF || n == 0 {
			break
		}
		if listErr != nil {
			return nil, listErr
		}
	}
	return trackedLister{items: all}, nil
}

func (h *modeTrackingHandlers) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	if ls, ok := h.base.FileList.(sftp.LstatFileLister); ok {
		lister, err := ls.Lstat(r)
		if err != nil {
			return nil, err
		}
		buf := make([]os.FileInfo, 1)
		n, listErr := lister.ListAt(buf, 0)
		if n == 0 && listErr != nil {
			return nil, listErr
		}
		return trackedLister{items: []os.FileInfo{h.wrapInfo(r.Filepath, buf[0])}}, nil
	}
	return h.Filelist(r)
}

func (h *modeTrackingHandlers) Readlink(p string) (string, error) {
	if rl, ok := h.base.FileList.(sftp.ReadlinkFileLister); ok {
		return rl.Readlink(p)
	}
	return "", sftp.ErrSSHFxOpUnsupported
}

func newInMemSFTPSession(t *testing.T, delay time.Duration) *Session {
	t.Helper()
	serverSide, clientSide := net.Pipe()
	base := sftp.InMemHandler()
	tracked := &modeTrackingHandlers{
		base:  base,
		modes: make(map[string]os.FileMode),
	}
	handlers := sftp.Handlers{
		FileGet:  base.FileGet,
		FilePut:  base.FilePut,
		FileCmd:  tracked,
		FileList: tracked,
	}
	if delay > 0 {
		probe := &transferProbe{FileReader: handlers.FileGet, FileWriter: handlers.FilePut, delay: delay}
		handlers.FileGet, handlers.FilePut = probe, probe
	}
	server := sftp.NewRequestServer(serverSide, handlers)
	go server.Serve()
	t.Cleanup(func() {
		_ = clientSide.Close()
		_ = server.Close()
	})
	c, err := sftp.NewClientPipe(clientSide, clientSide, sftp.UseFstat(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &Session{files: c}
}

func TestUploadDirNestedProgressAndCancel(t *testing.T) {
	s := newInMemSFTPSession(t, 0)
	localRoot := t.TempDir()
	srcDir := filepath.Join(localRoot, "project")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub", "deep"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	fileA := []byte("hello root file\n")
	fileB := bytes.Repeat([]byte("nested-payload-"), 1200)
	fileC := []byte("deep config = true\n")
	if err := os.WriteFile(filepath.Join(srcDir, "readme.txt"), fileA, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "data.bin"), fileB, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "deep", "app.conf"), fileC, 0644); err != nil {
		t.Fatal(err)
	}

	wantTotal := int64(len(fileA) + len(fileB) + len(fileC))
	type progEvent struct {
		done  int64
		total int64
		item  string
	}
	var events []progEvent
	count, err := s.UploadDir(context.Background(), srcDir, "/uploads/project", true, func(done, total int64, currentItem string) {
		events = append(events, progEvent{done: done, total: total, item: currentItem})
	})
	if err != nil {
		t.Fatalf("UploadDir failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("uploaded count = %d, want 3", count)
	}
	if len(events) < 2 {
		t.Fatalf("expected progress events, got %v", events)
	}
	if events[0].done != 0 || events[0].total != wantTotal {
		t.Fatalf("initial event = %+v, want done=0 total=%d", events[0], wantTotal)
	}
	last := events[len(events)-1]
	if last.done != wantTotal || last.total != wantTotal {
		t.Fatalf("final event = %+v, want done=%d total=%d", last, wantTotal, wantTotal)
	}
	seenItems := map[string]bool{}
	for i, ev := range events {
		if ev.total != wantTotal {
			t.Fatalf("event %d total = %d, want %d", i, ev.total, wantTotal)
		}
		if i > 0 && ev.done < events[i-1].done {
			t.Fatalf("progress regressed: %+v -> %+v", events[i-1], ev)
		}
		if ev.item != "" {
			seenItems[ev.item] = true
		}
	}
	for _, wantItem := range []string{"readme.txt", "sub/data.bin", "sub/deep/app.conf"} {
		if !seenItems[wantItem] {
			t.Fatalf("missing currentItem %q in %v", wantItem, seenItems)
		}
	}

	// Verify uploaded content and empty directory exist remotely.
	gotA, err := s.ReadText("/uploads/project/readme.txt")
	if err != nil || gotA != string(fileA) {
		t.Fatalf("readme mismatch: %q, %v", gotA, err)
	}
	gotC, err := s.ReadText("/uploads/project/sub/deep/app.conf")
	if err != nil || gotC != string(fileC) {
		t.Fatalf("deep file mismatch: %q, %v", gotC, err)
	}
	if info, err := s.files.Stat("/uploads/project/empty"); err != nil || !info.IsDir() {
		t.Fatalf("empty dir missing remotely: %v, %v", info, err)
	}

	// Verify cancellation stops upload promptly.
	slow := newInMemSFTPSession(t, 2*time.Millisecond)
	bigDir := filepath.Join(localRoot, "big")
	if err := os.MkdirAll(bigDir, 0755); err != nil {
		t.Fatal(err)
	}
	bigPayload := bytes.Repeat([]byte("A"), 256<<10)
	if err := os.WriteFile(filepath.Join(bigDir, "01.bin"), bigPayload, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bigDir, "02.bin"), bigPayload, 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = slow.UploadDir(ctx, bigDir, "/cancel-upload", true, func(done, _ int64, item string) {
		if item != "" && done > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on upload cancel, got %v", err)
	}
}

func TestDownloadDirNestedIllegalNameAndCancel(t *testing.T) {
	s := newInMemSFTPSession(t, 0)
	if err := s.Mkdir("/srv/app/nested"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mkdir("/srv/app/empty"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/srv/app/root.txt", "root-content"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/srv/app/nested/cfg.yaml", "port: 8080\n"); err != nil {
		t.Fatal(err)
	}
	// Symlink inside remote directory should be skipped without failing or looping.
	if err := s.files.Symlink("/srv/app", "/srv/app/self-loop"); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(t.TempDir(), "downloaded-app")
	wantTotal := int64(len("root-content") + len("port: 8080\n"))
	var lastDone, lastTotal int64
	seenItems := map[string]bool{}
	count, err := s.DownloadDir(context.Background(), "/srv/app", destDir, true, func(done, total int64, currentItem string) {
		if done < lastDone {
			t.Errorf("download progress regressed: %d -> %d", lastDone, done)
		}
		lastDone = done
		lastTotal = total
		if currentItem != "" {
			seenItems[currentItem] = true
		}
	})
	if err != nil {
		t.Fatalf("DownloadDir failed: %v", err)
	}
	if count != 2 || lastDone != wantTotal || lastTotal != wantTotal {
		t.Fatalf("count=%d done=%d total=%d (want 2, %d, %d)", count, lastDone, lastTotal, wantTotal, wantTotal)
	}
	if !seenItems["root.txt"] || !seenItems["nested/cfg.yaml"] {
		t.Fatalf("expected downloaded items in progress, got %v", seenItems)
	}
	gotRoot, err := os.ReadFile(filepath.Join(destDir, "root.txt"))
	if err != nil || string(gotRoot) != "root-content" {
		t.Fatalf("root.txt mismatch: %q, %v", string(gotRoot), err)
	}
	gotCfg, err := os.ReadFile(filepath.Join(destDir, "nested", "cfg.yaml"))
	if err != nil || string(gotCfg) != "port: 8080\n" {
		t.Fatalf("cfg.yaml mismatch: %q, %v", string(gotCfg), err)
	}
	if info, err := os.Stat(filepath.Join(destDir, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty subdir missing locally: %v, %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(destDir, "self-loop")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink should have been skipped, lstat err=%v", err)
	}

	// Illegal Windows filename guard (colon, wildcard, reserved device name).
	for _, badName := range []string{"bad:name.txt", "wild*card.log", "CON.txt", "trailing."} {
		t.Run("illegal/"+badName, func(t *testing.T) {
			badSession := newInMemSFTPSession(t, 0)
			if err := badSession.Mkdir("/bad"); err != nil {
				t.Fatal(err)
			}
			f, err := badSession.files.Create("/bad/" + badName)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write([]byte("x"))
			_ = f.Close()

			outDir := filepath.Join(t.TempDir(), "out")
			if _, err := badSession.DownloadDir(context.Background(), "/bad", outDir, true, nil); err == nil {
				t.Fatalf("expected error for illegal Windows name %q", badName)
			}
		})
	}

	// Cancel mid-download.
	slow := newInMemSFTPSession(t, 2*time.Millisecond)
	if err := slow.Mkdir("/bigdl"); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("D"), 256<<10)
	for _, name := range []string{"a.bin", "b.bin"} {
		f, err := slow.files.Create("/bigdl/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(payload); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = slow.DownloadDir(ctx, "/bigdl", filepath.Join(t.TempDir(), "dl-cancel"), true, func(done, _ int64, item string) {
		if item != "" && done > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on download cancel, got %v", err)
	}
}

func TestCopyRemoteFileDirAndSelfGuard(t *testing.T) {
	s := newInMemSFTPSession(t, 0)
	if err := s.Mkdir("/src/sub"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/src/file.txt", "copy-me-123"); err != nil {
		t.Fatal(err)
	}
	if err := s.Chmod("/src/file.txt", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/src/sub/child.txt", "child-data"); err != nil {
		t.Fatal(err)
	}
	// Symlink loop inside directory must be skipped safely during CopyRemote.
	if err := s.files.Symlink("/src", "/src/sub/loop"); err != nil {
		t.Fatal(err)
	}

	// Regular file copy.
	if err := s.CopyRemote(context.Background(), "/src/file.txt", "/src/file.copy.txt"); err != nil {
		t.Fatalf("CopyRemote file failed: %v", err)
	}
	got, err := s.ReadText("/src/file.copy.txt")
	if err != nil || got != "copy-me-123" {
		t.Fatalf("copied file content = %q, err = %v", got, err)
	}
	if copyInfo, err := s.files.Lstat("/src/file.copy.txt"); err != nil || copyInfo.Mode().Perm() != 0o750 {
		t.Fatalf("copied file perm = %04o, err = %v; want 0750", copyInfo.Mode().Perm(), err)
	}

	// Recursive directory copy.
	if err := s.CopyRemote(context.Background(), "/src", "/dst-copy"); err != nil {
		t.Fatalf("CopyRemote dir failed: %v", err)
	}
	gotChild, err := s.ReadText("/dst-copy/sub/child.txt")
	if err != nil || gotChild != "child-data" {
		t.Fatalf("copied child content = %q, err = %v", gotChild, err)
	}
	if _, err := s.files.Lstat("/dst-copy/sub/loop"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink loop inside copied dir should be skipped, err=%v", err)
	}

	// Self-copy & subdirectory copy guards.
	if err := s.CopyRemote(context.Background(), "/src", "/src"); err == nil {
		t.Fatal("expected error copying path onto itself")
	}
	if err := s.CopyRemote(context.Background(), "/src", "/src/inside"); err == nil {
		t.Fatal("expected error copying directory into its own subdirectory")
	}
	if err := s.CopyRemote(context.Background(), "/src/file.txt", "/dst-copy"); err == nil {
		t.Fatal("expected error copying regular file over existing directory")
	}
}

func TestChmodRecursive(t *testing.T) {
	s := newInMemSFTPSession(t, 0)
	if err := s.Mkdir("/perm/sub"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/perm/a.sh", "#!/bin/sh\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/perm/sub/b.sh", "#!/bin/sh\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.files.Symlink("/perm", "/perm/link-loop"); err != nil {
		t.Fatal(err)
	}

	// Single file chmod (non-recursive).
	if err := s.ChmodRecursive(context.Background(), "/perm/a.sh", 0o600, false); err != nil {
		t.Fatalf("single ChmodRecursive failed: %v", err)
	}

	// Recursive directory chmod.
	if err := s.ChmodRecursive(context.Background(), "/perm", 0o755, true); err != nil {
		t.Fatalf("recursive ChmodRecursive failed: %v", err)
	}
	for _, p := range []string{"/perm", "/perm/sub", "/perm/a.sh", "/perm/sub/b.sh"} {
		info, err := s.files.Lstat(p)
		if err != nil {
			t.Fatalf("Lstat(%s): %v", p, err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("%s perm = %04o, want 0755", p, info.Mode().Perm())
		}
	}

	// Restrictive mode (post-order) should also succeed on nested tree.
	if err := s.ChmodRecursive(context.Background(), "/perm", 0o644, true); err != nil {
		t.Fatalf("restrictive recursive chmod failed: %v", err)
	}
	if info, err := s.files.Lstat("/perm/sub/b.sh"); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("b.sh perm = %04o, err=%v", info.Mode().Perm(), err)
	}

	// Special POSIX bits (sticky bit 01755) map properly through posixToFileMode.
	if err := s.ChmodRecursive(context.Background(), "/perm/a.sh", 0o1755, false); err != nil {
		t.Fatalf("sticky bit chmod failed: %v", err)
	}
	stickyDetail, err := s.StatDetail("/perm/a.sh")
	if err != nil || stickyDetail.Octal != "1755" {
		t.Fatalf("sticky StatDetail = %+v, err=%v; want octal 1755", stickyDetail, err)
	}

	// Invalid mode rejected.
	if err := s.ChmodRecursive(context.Background(), "/perm", 0o10000, true); err == nil {
		t.Fatal("expected error for mode > 07777")
	}

	// Canceled context rejected immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.ChmodRecursive(ctx, "/perm", 0o755, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestEditorPreservesLinksAndTargetPermissions(t *testing.T) {
	s := newInMemSFTPSession(t, 0)
	if err := s.Mkdir("/etc/sub"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/etc/sub/config", "before"); err != nil {
		t.Fatal(err)
	}
	if err := s.Chmod("/etc/sub/config", 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.files.Symlink("sub/config", "/etc/current"); err != nil {
		t.Fatal(err)
	}
	if err := s.files.Symlink("/etc/current", "/entry"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/entry", "unexpected"); err == nil {
		t.Fatal("path-scoped WriteText accepted a symbolic link")
	}
	if data, err := s.ReadText("/etc/sub/config"); err != nil || data != "before" {
		t.Fatalf("rejected write changed the target: %q, %v", data, err)
	}
	if target, err := s.files.ReadLink("/entry"); err != nil || target != "/etc/current" {
		t.Fatalf("rejected write replaced the selected link: %q, %v", target, err)
	}
	if err := s.EditText("/entry", "after"); err != nil {
		t.Fatal(err)
	}
	if data, err := s.ReadText("/etc/sub/config"); err != nil || data != "after" {
		t.Fatalf("target contents: %q, %v", data, err)
	}
	if info, err := s.files.Stat("/etc/sub/config"); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("target permissions: %v, %v", info, err)
	}
	for link, want := range map[string]string{"/entry": "/etc/current", "/etc/current": "sub/config"} {
		if target, err := s.files.ReadLink(link); err != nil || target != want {
			t.Fatalf("%q points to %q, want %q: %v", link, target, want, err)
		}
	}
	if err := s.files.Symlink("/missing", "/dangling"); err != nil {
		t.Fatal(err)
	}
	if err := s.EditText("/dangling", "oops"); err == nil {
		t.Fatal("saved through a dangling link")
	}
	if target, err := s.files.ReadLink("/dangling"); err != nil || target != "/missing" {
		t.Fatalf("dangling link changed: %q, %v", target, err)
	}
	if err := s.files.Symlink("/loop", "/loop"); err != nil {
		t.Fatal(err)
	}
	if err := s.EditText("/loop", "oops"); err == nil {
		t.Fatal("saved through a link cycle")
	}
}

func TestSFTPUsesExactRemoteNames(t *testing.T) {
	s := newInMemSFTPSession(t, 0)
	for _, suffix := range []string{"", " "} {
		if err := s.Mkdir("/dir" + suffix); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateEmptyFile("/dir/name" + suffix); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := s.StatDetail("/dir/name "); err != nil || info.Name != "name " {
		t.Fatalf("wrong entry: %v, %v", info, err)
	}
	if err := s.Delete("/dir/name "); err != nil {
		t.Fatal(err)
	}
	if _, err := s.files.Lstat("/dir/name"); err != nil {
		t.Fatalf("other file was deleted: %v", err)
	}
	if _, err := s.files.Lstat("/dir/name "); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected file remained: %v", err)
	}
	count, err := s.DownloadDir(t.Context(), "/dir ", filepath.Join(t.TempDir(), "empty"), true, nil)
	if err != nil || count != 0 {
		t.Fatalf("downloaded a different directory: %d, %v", count, err)
	}
}

func TestDownloadDirRejectsCollidingWindowsNamesBeforeWriting(t *testing.T) {
	s := newInMemSFTPSession(t, 0)
	if err := s.Mkdir("/case"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README", "Readme"} {
		if err := s.WriteText("/case/"+name, name); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "download")
	if _, err := s.DownloadDir(t.Context(), "/case", dest, true, nil); err == nil {
		t.Fatal("download allowed two files with the same Windows destination")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination was created before detecting name collision: %v", err)
	}
}

func TestStatDetailFileDirAndSymlink(t *testing.T) {
	s := newInMemSFTPSession(t, 0)
	if err := s.Mkdir("/inspect/nested"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/inspect/one.txt", "12345"); err != nil {
		t.Fatal(err)
	}
	if err := s.Chmod("/inspect/one.txt", 0o640); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/inspect/nested/deep.txt", "deep"); err != nil {
		t.Fatal(err)
	}
	if err := s.files.Symlink("/inspect/one.txt", "/inspect/one.link"); err != nil {
		t.Fatal(err)
	}

	fileDetail, err := s.StatDetail("/inspect/one.txt")
	if err != nil {
		t.Fatalf("StatDetail file: %v", err)
	}
	if fileDetail.Name != "one.txt" || fileDetail.IsDir || fileDetail.IsSymlink || fileDetail.Size != 5 {
		t.Fatalf("unexpected file detail: %+v", fileDetail)
	}
	if fileDetail.Octal != "0640" {
		t.Fatalf("file octal = %q, want 0640", fileDetail.Octal)
	}
	if fileDetail.AccessTime <= 0 {
		t.Fatalf("expected AccessTime > 0, got %+v", fileDetail)
	}

	dirDetail, err := s.StatDetail("/inspect")
	if err != nil {
		t.Fatalf("StatDetail dir: %v", err)
	}
	if !dirDetail.IsDir || dirDetail.IsSymlink || dirDetail.ItemCount != 3 {
		t.Fatalf("unexpected dir detail (want ItemCount=3 direct children): %+v", dirDetail)
	}

	linkDetail, err := s.StatDetail("/inspect/one.link")
	if err != nil {
		t.Fatalf("StatDetail symlink: %v", err)
	}
	if !linkDetail.IsSymlink {
		t.Fatalf("expected IsSymlink=true, got %+v", linkDetail)
	}
	if linkDetail.LinkTarget != "/inspect/one.txt" {
		t.Fatalf("LinkTarget = %q, want /inspect/one.txt", linkDetail.LinkTarget)
	}

	if _, err := s.StatDetail("/does-not-exist"); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestReadTextNulByteAnd4MiBLimit(t *testing.T) {
	s := newInMemSFTPSession(t, 0)

	// 1. NUL bytes rejected with clear Chinese message.
	f, err := s.files.Create("/binary.dat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("ELF\x00binary")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := s.ReadText("/binary.dat"); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("expected NUL byte rejection error, got %v", err)
	}

	// 2. Invalid UTF-8 rejected.
	f2, err := s.files.Create("/badutf8.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f2.Write([]byte{0xff, 0xfe, 0xfd}); err != nil {
		t.Fatal(err)
	}
	_ = f2.Close()
	if _, err := s.ReadText("/badutf8.txt"); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("expected UTF-8 error, got %v", err)
	}

	// 3. 3 MiB UTF-8 text (above old 2 MiB, within new 4 MiB) succeeds for both WriteText and ReadText.
	threeMiB := strings.Repeat("a", 3<<20)
	if err := s.WriteText("/three.txt", threeMiB); err != nil {
		t.Fatalf("WriteText 3 MiB failed: %v", err)
	}
	got3, err := s.ReadText("/three.txt")
	if err != nil || len(got3) != len(threeMiB) {
		t.Fatalf("ReadText 3 MiB failed: len=%d err=%v", len(got3), err)
	}

	// 4. Over 4 MiB rejected in both ReadText and WriteText.
	over4MiB := strings.Repeat("b", (4<<20)+1)
	if err := s.WriteText("/over4.txt", over4MiB); err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("expected WriteText 4 MiB error, got %v", err)
	}
	fBig, err := s.files.Create("/big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fBig.Write([]byte(over4MiB)); err != nil {
		t.Fatal(err)
	}
	_ = fBig.Close()
	if _, err := s.ReadText("/big.txt"); err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("expected ReadText 4 MiB error, got %v", err)
	}
}
