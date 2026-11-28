package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haoch1/Shell/internal/local"
)

func TestCleanupDataCLISecurityAndRemoval(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)

	// Populate a full 5-folder Shell data tree.
	shellDir := filepath.Join(root, "Shell")
	for _, rel := range []string{
		filepath.Join("config", "settings.json"),
		filepath.Join("security", "credentials.dpapi"),
		filepath.Join("logs", "audit.jsonl"),
		filepath.Join("runtime", "bridge.json"),
		filepath.Join("cache", "WebView2", "dummy.dat"),
	} {
		p := filepath.Join(shellDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// Unrelated directory MUST NEVER be deleted.
	unrelated := t.TempDir()
	keepFile := filepath.Join(unrelated, "important.txt")
	if err := os.WriteFile(keepFile, []byte("precious"), 0600); err != nil {
		t.Fatal(err)
	}

	// 1. Missing required flags fails with code 2.
	if code := runCleanupDataCLI([]string{"--cleanup-data"}); code != 2 {
		t.Fatalf("expected exit code 2 for missing --dir, got %d", code)
	}

	// 2. Arbitrary directory or parent dir MUST be rejected with code 3 and remain untouched.
	for _, badTarget := range []string{unrelated, root, filepath.Join(root, "OtherApp")} {
		if code := runCleanupDataCLI([]string{"--cleanup-data", "--pid", "0", "--dir", badTarget}); code != 3 {
			t.Fatalf("expected security rejection (3) for %q, got %d", badTarget, code)
		}
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("unrelated file was disturbed: %v", err)
	}

	// 3. Exact local.DataDir() succeeds and removes Shell/ completely.
	if code := runCleanupDataCLI([]string{"--cleanup-data", "--pid", "0", "--dir", shellDir}); code != 0 {
		t.Fatalf("expected exit code 0 for valid dataDir, got %d", code)
	}
	if _, err := os.Stat(shellDir); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be completely removed, stat err=%v", shellDir, err)
	}
}

func TestCleanupRuntimeLeftoversAndUninstallFlow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	shellDir := filepath.Join(root, "Shell")

	// Calling cleanupRuntimeLeftovers on a missing directory must NOT create anything.
	cleanupRuntimeLeftovers()
	if _, err := os.Stat(shellDir); !os.IsNotExist(err) {
		t.Fatalf("cleanupRuntimeLeftovers created directory when absent: %v", err)
	}

	// Create real settings + stale tmp/lock/bridge files.
	if _, err := local.SaveSettings(local.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	if err := local.WriteBridgeRuntime(17822, ""); err != nil {
		t.Fatal(err)
	}
	staleFiles := []string{
		filepath.Join(shellDir, "config", ".shell-123.tmp"),
		filepath.Join(shellDir, "security", ".shell-456.tmp"),
		filepath.Join(shellDir, "security", ".known_hosts-789"),
		filepath.Join(shellDir, "runtime", ".shell-000.tmp"),
		filepath.Join(shellDir, "runtime", "known_hosts.lock"),
	}
	for _, p := range staleFiles {
		if err := os.WriteFile(p, []byte("tmp"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	cleanupRuntimeLeftovers()

	for _, p := range append(staleFiles,
		filepath.Join(shellDir, "runtime", "bridge.json"),
		filepath.Join(shellDir, "runtime", "store.lock"),
	) {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("expected leftover %s to be removed, stat err=%v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(shellDir, "config", "settings.json")); err != nil {
		t.Fatalf("cleanupRuntimeLeftovers removed real settings.json: %v", err)
	}

	// Verify runUninstallCLI and App.UninstallAndQuit using safe test seams (zero real HKCU writes).
	origCleanReg := cleanAutoStartRegistry
	origLaunch := launchDataCleanup
	regCleaned := 0
	var launchedDir string
	cleanAutoStartRegistry = func() error {
		regCleaned++
		return nil
	}
	launchDataCleanup = func(_ int, dataDir string) error {
		launchedDir = dataDir
		return nil
	}
	t.Cleanup(func() {
		cleanAutoStartRegistry = origCleanReg
		launchDataCleanup = origLaunch
	})

	app := newApp()
	if err := app.UninstallAndQuit(true); err != nil {
		t.Fatalf("UninstallAndQuit(true) failed: %v", err)
	}
	if regCleaned != 1 || launchedDir != shellDir {
		t.Fatalf("unexpected uninstall actions: regCleaned=%d launchedDir=%q", regCleaned, launchedDir)
	}
	// Shutdown while uninstalling must NOT recreate config/settings.json or runtime/store.lock.
	app.shutdown(t.Context())
	for _, sub := range []string{"config", "security", "logs", "runtime"} {
		if _, err := os.Stat(filepath.Join(shellDir, sub)); !os.IsNotExist(err) {
			t.Fatalf("subdirectory %s should remain deleted after UninstallAndQuit + shutdown", sub)
		}
	}

	// Verify --uninstall CLI removes dataDir and invokes registry cleanup cleanly.
	if err := os.MkdirAll(filepath.Join(shellDir, "cache", "WebView2"), 0700); err != nil {
		t.Fatal(err)
	}
	if code := runUninstallCLI(); code != 0 {
		t.Fatalf("runUninstallCLI() = %d, want 0", code)
	}
	if regCleaned != 2 {
		t.Fatalf("expected runUninstallCLI to clean registry, count=%d", regCleaned)
	}
	if _, err := os.Stat(shellDir); !os.IsNotExist(err) {
		t.Fatalf("expected runUninstallCLI to remove %s", shellDir)
	}
}
