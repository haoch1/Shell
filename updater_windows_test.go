package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/remote"
	"golang.org/x/sys/windows"
)

func TestCompareSemverAndTrustedURL(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.1.0", "1.2.0", -1},
		{"v1.2.0", "1.2.0", 0},
		{"1.2.1", "1.2.0", 1},
		{"1.9.0", "1.10.0", -1},
		{"1.2.0-beta.1", "1.2.0", -1},
		{"2.0.0", "1.99.9", 1},
	}
	for _, tc := range cases {
		if got := compareSemver(tc.a, tc.b); got != tc.want {
			t.Fatalf("compareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}

	shaBody := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  Shell-Windows-x64.exe\n" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  Shell-Windows-x64.zip\n"
	sum, err := parseSHA256Sums(shaBody, releaseAssetName)
	if err != nil || sum != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("parseSHA256Sums failed: sum=%q err=%v", sum, err)
	}
}

func TestCheckDownloadVerifyAndAtomicSwap(t *testing.T) {
	fakeExe := append([]byte{'M', 'Z'}, make([]byte, 256)...)
	sumArr := sha256.Sum256(fakeExe)
	goodHex := hex.EncodeToString(sumArr[:])

	var srvURL string
	badChecksum := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{
				"tag_name": "v1.3.0",
				"html_url": "https://github.com/haoch1/Shell/releases/tag/v1.3.0",
				"body": "### 更新内容\n- 测试自动更新",
				"published_at": "2026-11-28T00:00:00Z",
				"assets": [
					{"name": "Shell-Windows-x64.exe", "size": %d, "browser_download_url": "%s/Shell-Windows-x64.exe"},
					{"name": "SHA256SUMS.txt", "size": 90, "browser_download_url": "%s/SHA256SUMS.txt"}
				]
			}`, len(fakeExe), srvURL, srvURL)
		case "/SHA256SUMS.txt":
			if badChecksum {
				fmt.Fprintf(w, "%s  Shell-Windows-x64.exe\n", strings.Repeat("0", 64))
			} else {
				fmt.Fprintf(w, "%s  Shell-Windows-x64.exe\n", goodHex)
			}
		case "/Shell-Windows-x64.exe":
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fakeExe)))
			_, _ = w.Write(fakeExe)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	client := &http.Client{Timeout: 5 * time.Second}
	info, err := checkReleaseFromAPI(context.Background(), client, srv.URL+"/latest", "1.2.0")
	if err != nil {
		t.Fatalf("checkReleaseFromAPI failed: %v", err)
	}
	if !info.HasUpdate || info.LatestVersion != "1.3.0" || info.DownloadURL == "" || info.ChecksumsURL == "" {
		t.Fatalf("unexpected UpdateInfo: %+v", info)
	}

	tmp := t.TempDir()
	destNew := filepath.Join(tmp, "Shell.exe.new")

	// 1. Checksum mismatch must fail and remove the temp file.
	badChecksum = true
	if err := downloadVerifiedExecutable(context.Background(), client, info, destNew, nil); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, err := os.Stat(destNew); !os.IsNotExist(err) {
		t.Fatal("expected unverified temp executable to be cleaned up")
	}

	// 2. Valid checksum succeeds and atomic swap replaces target executable cleanly.
	badChecksum = false
	var phases []string
	if err := downloadVerifiedExecutable(context.Background(), client, info, destNew, func(p UpdateProgress) {
		phases = append(phases, p.Phase)
	}); err != nil {
		t.Fatalf("downloadVerifiedExecutable failed: %v", err)
	}
	if len(phases) == 0 {
		t.Fatal("expected progress events")
	}

	curExe := filepath.Join(tmp, "Shell.exe")
	if err := os.WriteFile(curExe, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	oldPath, err := swapExecutableAtomically(curExe, destNew)
	if err != nil {
		t.Fatalf("swapExecutableAtomically failed: %v", err)
	}
	wantOld := fmt.Sprintf("%s.old.%d", curExe, os.Getpid())
	if oldPath != wantOld {
		t.Fatalf("oldPath = %q, want %q", oldPath, wantOld)
	}
	gotNew, err := os.ReadFile(curExe)
	if err != nil || len(gotNew) != len(fakeExe) || gotNew[0] != 'M' || gotNew[1] != 'Z' {
		t.Fatalf("swapped executable invalid: len=%d err=%v", len(gotNew), err)
	}
	gotOld, err := os.ReadFile(oldPath)
	if err != nil || string(gotOld) != "old-binary" {
		t.Fatalf("backup old executable mismatch: %q err=%v", string(gotOld), err)
	}
}

func TestSwapExecutableAtomicallyWithExistingLockedOldFile(t *testing.T) {
	tmp := t.TempDir()
	curExe := filepath.Join(tmp, "Shell.exe")
	staleOld := curExe + ".old"
	newExe := fmt.Sprintf("%s.new.%d", curExe, os.Getpid())

	if err := os.WriteFile(curExe, []byte("current-v1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleOld, []byte("locked-stale-old"), 0755); err != nil {
		t.Fatal(err)
	}
	newPayload := append([]byte{'M', 'Z'}, []byte(strings.Repeat("v2", 64))...)
	if err := os.WriteFile(newExe, newPayload, 0755); err != nil {
		t.Fatal(err)
	}

	// Lock legacy Shell.exe.old without FILE_SHARE_DELETE to simulate Defender / another process.
	stalePtr, err := windows.UTF16PtrFromString(staleOld)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(
		stalePtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("failed to lock stale .old file: %v", err)
	}
	defer windows.CloseHandle(h)

	oldPath, err := swapExecutableAtomically(curExe, newExe)
	if err != nil {
		t.Fatalf("swapExecutableAtomically failed despite locked .old: %v", err)
	}
	wantOld := fmt.Sprintf("%s.old.%d", curExe, os.Getpid())
	if oldPath != wantOld {
		t.Fatalf("oldPath = %q, want unique %q", oldPath, wantOld)
	}
	if got, err := os.ReadFile(curExe); err != nil || !bytes.Equal(got, newPayload) {
		t.Fatalf("target exe content mismatch after swap: err=%v len=%d", err, len(got))
	}
	if got, err := os.ReadFile(oldPath); err != nil || string(got) != "current-v1" {
		t.Fatalf("unique backup content mismatch: %q err=%v", string(got), err)
	}

	// Verify rollback if the replacement source file does not exist.
	if _, err := swapExecutableAtomically(curExe, filepath.Join(tmp, "missing.new")); err == nil {
		t.Fatal("expected swapExecutableAtomically to fail for missing newPath")
	}
	if got, err := os.ReadFile(curExe); err != nil || !bytes.Equal(got, newPayload) {
		t.Fatalf("expected rollback to preserve curExe: err=%v len=%d", err, len(got))
	}
}

func TestHelperSleepProcess(t *testing.T) {
	if os.Getenv("SHELL_TEST_WAIT_STDIN") == "1" {
		_, _ = fmt.Fprintln(os.Stdout, "READY")
		done := make(chan struct{})
		go func() {
			var buf [16]byte
			_, _ = os.Stdin.Read(buf[:])
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
		os.Exit(0)
	}
	if os.Getenv("SHELL_TEST_SLEEP_HELPER") != "1" {
		return
	}
	time.Sleep(400 * time.Millisecond)
	os.Exit(0)
}

func TestApplyStagedUpdateWaitsForParentAndReplacesAcrossDirs(t *testing.T) {
	stageDir := t.TempDir()
	targetDir := t.TempDir()

	stagedExe := filepath.Join(stageDir, "Shell-update-1.5.1-999.exe")
	targetExe := filepath.Join(targetDir, "Shell-Windows-x64.exe")
	legacyOld := targetExe + ".old"
	staleOld := targetExe + ".old.42"
	staleNew := targetExe + ".new.99"

	v1 := append([]byte{'M', 'Z'}, bytes.Repeat([]byte("1"), 128)...)
	v2 := append([]byte{'M', 'Z'}, bytes.Repeat([]byte("2"), 256)...)
	if err := os.WriteFile(stagedExe, v2, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetExe, v1, 0755); err != nil {
		t.Fatal(err)
	}
	for _, leftover := range []string{legacyOld, staleOld, staleNew} {
		if err := os.WriteFile(leftover, []byte("leftover"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	selfTestExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(selfTestExe, "-test.run=^TestHelperSleepProcess$")
	child.Env = append(os.Environ(), "SHELL_TEST_SLEEP_HELPER=1")
	if err := child.Start(); err != nil {
		t.Fatalf("failed to start child process: %v", err)
	}
	defer func() { _ = child.Wait() }()

	start := time.Now()
	if err := applyStagedUpdate(child.Process.Pid, stagedExe, targetExe, false); err != nil {
		t.Fatalf("applyStagedUpdate failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 250*time.Millisecond {
		t.Fatalf("applyStagedUpdate returned too quickly (%v); expected wait for parent PID %d", elapsed, child.Process.Pid)
	}

	got, err := os.ReadFile(targetExe)
	if err != nil {
		t.Fatalf("failed reading replaced targetExe: %v", err)
	}
	if !bytes.Equal(got, v2) {
		t.Fatalf("targetExe content was not replaced with staged binary (len=%d, want=%d)", len(got), len(v2))
	}

	// Ensure targetExe itself is visible (FILE_ATTRIBUTE_HIDDEN must be cleared after swap).
	targetPtr, err := windows.UTF16PtrFromString(targetExe)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(targetPtr)
	if err != nil {
		t.Fatalf("GetFileAttributes(targetExe) failed: %v", err)
	}
	if attrs&windows.FILE_ATTRIBUTE_HIDDEN != 0 {
		t.Fatalf("targetExe should not remain hidden after swap (attrs=0x%x)", attrs)
	}

	// Verify ZERO .old* or .new* files remain in targetDir (and pre-existing legacy .old is removed).
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(targetExe) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected targetDir to contain ONLY %s with zero .old/.new leftovers, got %v", filepath.Base(targetExe), names)
	}
}

func TestApplyStagedUpdateFallbackRenameAsideWhenTargetOpen(t *testing.T) {
	stageDir := t.TempDir()
	targetDir := t.TempDir()

	stagedExe := filepath.Join(stageDir, "Shell-update-1.5.1-777.exe")
	targetExe := filepath.Join(targetDir, "Shell-Windows-x64.exe")

	v2 := append([]byte{'M', 'Z'}, bytes.Repeat([]byte("B"), 192)...)
	if err := os.WriteFile(stagedExe, v2, 0755); err != nil {
		t.Fatal(err)
	}
	// Copy the test binary itself to targetExe and execute it to reproduce a genuine
	// Windows SEC_IMAGE executable lock (identical to a running Shell.exe --mcp process).
	selfTestExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := copyFileStrict(selfTestExe, targetExe); err != nil {
		t.Fatalf("failed copying test executable: %v", err)
	}

	// Capture reboot-delete scheduling so unit tests never touch HKLM PendingFileRenameOperations.
	var scheduledPath string
	origDelayDelete := delayDeleteOnReboot
	delayDeleteOnReboot = func(p string) {
		scheduledPath = p
	}
	defer func() { delayDeleteOnReboot = origDelayDelete }()

	holder := exec.Command(targetExe, "-test.run=^TestHelperSleepProcess$")
	holder.Env = append(os.Environ(), "SHELL_TEST_WAIT_STDIN=1")
	stdinPipe, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatalf("failed to start running targetExe process: %v", err)
	}
	holderExited := false
	defer func() {
		_ = stdinPipe.Close()
		if !holderExited {
			_ = holder.Process.Kill()
			_ = holder.Wait()
		}
		_ = cleanupStaleUpdateFiles(targetDir, filepath.Base(targetExe))
	}()

	// Wait until the child process prints READY so its executable image is definitely mapped.
	readyBuf := make([]byte, 32)
	if _, err := stdoutPipe.Read(readyBuf); err != nil {
		t.Fatalf("failed waiting for child READY signal: %v", err)
	}

	if err := applyStagedUpdate(0, stagedExe, targetExe, false); err != nil {
		t.Fatalf("applyStagedUpdate failed when targetExe was actively running: %v", err)
	}
	got, err := os.ReadFile(targetExe)
	if err != nil || !bytes.Equal(got, v2) {
		t.Fatalf("expected targetExe to be replaced via rename-aside fallback: err=%v len=%d", err, len(got))
	}

	// Target exe must be visible, while the locked aside file must be hidden and scheduled for reboot deletion.
	targetPtr, err := windows.UTF16PtrFromString(targetExe)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(targetPtr)
	if err != nil || attrs&windows.FILE_ATTRIBUTE_HIDDEN != 0 {
		t.Fatalf("targetExe must be visible after fallback swap (attrs=0x%x err=%v)", attrs, err)
	}
	if scheduledPath == "" {
		t.Fatal("expected locked aside file to be scheduled for reboot deletion")
	}
	asidePtr, err := windows.UTF16PtrFromString(scheduledPath)
	if err != nil {
		t.Fatal(err)
	}
	asideAttrs, err := windows.GetFileAttributes(asidePtr)
	if err != nil || asideAttrs&windows.FILE_ATTRIBUTE_HIDDEN == 0 {
		t.Fatalf("expected locked aside file %q to have FILE_ATTRIBUTE_HIDDEN (attrs=0x%x err=%v)", scheduledPath, asideAttrs, err)
	}

	// Once the external process exits, startup cleanup must remove the hidden aside file completely.
	_ = stdinPipe.Close()
	_ = holder.Wait()
	holderExited = true
	if ok := cleanupStaleUpdateFiles(targetDir, filepath.Base(targetExe)); !ok {
		t.Fatal("expected cleanupStaleUpdateFiles to remove hidden aside file once process exited")
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(targetExe) {
		t.Fatalf("expected only %s left after cleanup, got %d entries", filepath.Base(targetExe), len(entries))
	}
}

func TestCleanupStaleUpdateFiles(t *testing.T) {
	tmp := t.TempDir()
	keepFiles := []string{"Shell.exe", "other.exe.old", "config.json"}
	staleFiles := []string{
		"Shell.exe.old",
		"Shell.exe.old.1234",
		"Shell.exe.new",
		"Shell.exe.new.5678",
		".shell-writecheck-999.tmp",
	}
	for _, name := range append(append([]string{}, keepFiles...), staleFiles...) {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if ok := cleanupStaleUpdateFiles(tmp, "Shell.exe"); !ok {
		t.Fatal("expected cleanupStaleUpdateFiles to remove all stale files cleanly")
	}
	for _, name := range keepFiles {
		if _, err := os.Stat(filepath.Join(tmp, name)); err != nil {
			t.Fatalf("expected %s to remain untouched: %v", name, err)
		}
	}
	for _, name := range staleFiles {
		if _, err := os.Stat(filepath.Join(tmp, name)); !os.IsNotExist(err) {
			t.Fatalf("expected stale file %s to be removed", name)
		}
	}

	// Verify staging directory cleanup respects minAge and never deletes currentExe.
	stageDir := t.TempDir()
	oldStaged := filepath.Join(stageDir, "Shell-update-1.5.0-111.exe")
	freshStaged := filepath.Join(stageDir, "Shell-update-1.5.1-222.exe")
	curStaged := filepath.Join(stageDir, "Shell-update-1.5.0-cur.exe")
	unrelated := filepath.Join(stageDir, "notes.txt")
	for _, p := range []string{oldStaged, freshStaged, curStaged, unrelated} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Minute)
	for _, p := range []string{oldStaged, curStaged, unrelated} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}

	if allClean := cleanupUpdateStagingDir(stageDir, curStaged, 10*time.Second); allClean {
		t.Fatal("expected freshStaged to remain pending until minAge elapses")
	}
	if _, err := os.Stat(oldStaged); !os.IsNotExist(err) {
		t.Fatal("expected oldStaged to be removed")
	}
	for _, p := range []string{freshStaged, curStaged, unrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to be preserved: %v", filepath.Base(p), err)
		}
	}
}

func TestUpdaterHelpersAndCLIValidation(t *testing.T) {
	if !isDirWritable(t.TempDir()) {
		t.Fatal("expected temp directory to be writable")
	}
	if isDirWritable(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Fatal("expected non-existent directory not to be writable")
	}
	if !isAccessDenied(fmt.Errorf("wrapped: %w", windows.ERROR_ACCESS_DENIED)) {
		t.Fatal("expected wrapped ERROR_ACCESS_DENIED to match isAccessDenied")
	}
	if !isAccessDenied(os.ErrPermission) {
		t.Fatal("expected os.ErrPermission to match isAccessDenied")
	}
	if isAccessDenied(windows.ERROR_SHARING_VIOLATION) {
		t.Fatal("expected ERROR_SHARING_VIOLATION not to match isAccessDenied")
	}

	joined := joinWindowsArgs([]string{
		"--apply-update",
		"--pid", "4321",
		"--target", `D:\Program Files\Shell\Shell-Windows-x64.exe`,
		"--relaunch",
	})
	if !strings.Contains(joined, `"D:\Program Files\Shell\Shell-Windows-x64.exe"`) {
		t.Fatalf("expected spaces in target path to be quoted, got %q", joined)
	}
	if code := runApplyUpdateCLI([]string{"--apply-update"}); code == 0 {
		t.Fatal("expected runApplyUpdateCLI without --target to return non-zero")
	}
}

func TestOutboundHTTPProxyFormatsIPv6Host(t *testing.T) {
	st := local.DefaultSettings()
	st.ProxyMode = "socks5"
	st.ProxyHost = "::1"
	st.ProxyPort = 1080
	st.ProxyUser = "ops"
	if got := resolveSettingsProxy(st); got == nil || got.String() != "socks5://ops@[::1]:1080" {
		t.Fatalf("unexpected settings IPv6 proxy URL: %v", got)
	}

	sp := remote.SystemProxyInfo{
		Enabled:  true,
		Protocol: "http",
		Host:     "fd00::1",
		Port:     7897,
		User:     "alice",
		Password: "pw",
		Bypass:   "*.internal",
	}
	if got := resolveSystemProxyURL(sp, "api.github.com"); got == nil || got.String() != "http://alice:pw@[fd00::1]:7897" {
		t.Fatalf("unexpected system IPv6 proxy URL: %v", got)
	}
	if got := resolveSystemProxyURL(sp, "svc.internal"); got != nil {
		t.Fatalf("expected bypassed host to return nil proxy, got %v", got)
	}
}
