package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/haoch1/Shell/internal/local"
	"golang.org/x/sys/windows"
)

// Test seams so unit tests can exercise uninstall flows without modifying real HKCU keys
// or spawning background cleanup helpers.
var (
	cleanAutoStartRegistry = local.CleanAllRegistryResidue
	launchDataCleanup      = launchDetachedDataCleanup
)

// runUninstallCLI handles `Shell.exe --uninstall`: clears all Windows auto-start registry
// entries and removes the entire `%LOCALAPPDATA%\Shell` data directory cleanly.
func runUninstallCLI() int {
	regErr := cleanAutoStartRegistry()
	dataDir, dirErr := local.DataDir()
	var rmErr error
	if dirErr == nil && dataDir != "" {
		rmErr = os.RemoveAll(dataDir)
	}
	if err := errors.Join(regErr, dirErr, rmErr); err != nil {
		fmt.Fprintf(os.Stderr, "清理失败：%v\n", err)
		return 1
	}
	fmt.Printf("已清除开机自启注册表项并删除本地数据目录：%s\n现在可直接删除 Shell.exe 完成无残留卸载。\n", dataDir)
	return 0
}

// runCleanupDataCLI handles the hidden post-exit helper `Shell.exe --cleanup-data --pid <pid> --dir <dataDir>`.
// SECURITY INVARIANT: strictly refuses to delete any directory other than `local.DataDir()`.
func runCleanupDataCLI(args []string) int {
	fs := flag.NewFlagSet("cleanup-data", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cleanupFlag := fs.Bool("cleanup-data", false, "")
	pid := fs.Int("pid", 0, "")
	dir := fs.String("dir", "", "")

	if err := fs.Parse(args); err != nil || !*cleanupFlag {
		return 2
	}
	rawDir := strings.TrimSpace(*dir)
	if rawDir == "" {
		return 2
	}
	expectedDir, err := local.DataDir()
	if err != nil || expectedDir == "" {
		return 1
	}
	targetDir, err := filepath.Abs(rawDir)
	if err != nil {
		return 3
	}
	targetDir = filepath.Clean(targetDir)
	if fi, lErr := os.Lstat(targetDir); lErr == nil && fi.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintln(os.Stderr, "拒绝清理符号链接目录")
		return 3
	}
	if !sameFilePath(targetDir, expectedDir) {
		fmt.Fprintln(os.Stderr, "拒绝清理非 Shell 本地数据目录")
		return 3
	}

	waitForProcessExit(*pid, 12*time.Second)

	const maxAttempts = 24
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := os.RemoveAll(targetDir)
		if err == nil {
			if _, statErr := os.Stat(targetDir); os.IsNotExist(statErr) {
				return 0
			}
		}
		if attempt < maxAttempts-1 {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if _, statErr := os.Stat(targetDir); os.IsNotExist(statErr) {
		return 0
	}
	return 1
}

func launchDetachedDataCleanup(parentPID int, dataDir string) error {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && resolved != "" {
		exePath = resolved
	}
	cmd := exec.Command(exePath,
		"--cleanup-data",
		"--pid", strconv.Itoa(parentPID),
		"--dir", dataDir,
	)
	cmd.Dir = os.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_NO_WINDOW,
	}
	return cmd.Start()
}

// cleanupRuntimeLeftovers removes stale temporary atomic-write files and best-effort deletes
// runtime lock/bridge files on clean app shutdown without creating any directories.
func cleanupRuntimeLeftovers() {
	dataDir, err := local.DataDir()
	if err != nil || dataDir == "" {
		return
	}
	for _, sub := range []string{"config", "security", "runtime"} {
		subDir := filepath.Join(dataDir, sub)
		entries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if (strings.HasPrefix(name, ".shell-") && strings.HasSuffix(name, ".tmp")) ||
				(sub == "security" && strings.HasPrefix(name, ".known_hosts-")) {
				_ = os.Remove(filepath.Join(subDir, name))
			}
		}
	}
	runtimeDir := filepath.Join(dataDir, "runtime")
	for _, name := range []string{"bridge.json", "store.lock", "known_hosts.lock"} {
		_ = os.Remove(filepath.Join(runtimeDir, name))
	}
}
