package local

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func isolatedAutoStart(t *testing.T, exePath string) (runKey, approvedKey string) {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	baseKey := `Software\ShellTestAutoStart_` + hex.EncodeToString(b[:])
	runKey = baseKey + `\Run`
	approvedKey = baseKey + `\StartupApproved\Run`

	orig := autoStart
	autoStart = autoStartConfig{
		runKeyPath:      runKey,
		approvedKeyPath: approvedKey,
		valueName:       "Shell",
		resolveExe:      func() (string, error) { return exePath, nil },
	}
	t.Cleanup(func() {
		autoStart = orig
		_ = registry.DeleteKey(registry.CURRENT_USER, approvedKey)
		_ = registry.DeleteKey(registry.CURRENT_USER, baseKey+`\StartupApproved`)
		_ = registry.DeleteKey(registry.CURRENT_USER, runKey)
		_ = registry.DeleteKey(registry.CURRENT_USER, baseKey)
	})
	return runKey, approvedKey
}

func TestAutoStartLifecycleApprovedSyncAndZeroResidue(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	exe1 := filepath.Join(dir1, "Shell.exe")
	exe2 := filepath.Join(dir2, "Shell.exe")
	for _, p := range []string{exe1, exe2} {
		if err := os.WriteFile(p, []byte("MZ"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if resolved, err := filepath.EvalSymlinks(exe1); err == nil && resolved != "" {
		exe1 = filepath.Clean(resolved)
	}
	if resolved, err := filepath.EvalSymlinks(exe2); err == nil && resolved != "" {
		exe2 = filepath.Clean(resolved)
	}

	runKey, approvedKey := isolatedAutoStart(t, exe1)

	// 1. Fresh state is disabled.
	enabled, err := IsAutoStartEnabled()
	if err != nil || enabled {
		t.Fatalf("expected fresh auto-start disabled, got (%v, %v)", enabled, err)
	}

	// 2. Reject temporary go-build executable inside os.TempDir().
	goBuildDir := filepath.Join(os.TempDir(), "go-build123456789", "b001")
	if err := os.MkdirAll(goBuildDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(os.TempDir(), "go-build123456789")) })
	tempExe := filepath.Join(goBuildDir, "Shell.exe")
	if err := os.WriteFile(tempExe, []byte("MZ"), 0755); err != nil {
		t.Fatal(err)
	}
	autoStart.resolveExe = func() (string, error) { return tempExe, nil }
	if err := SetAutoStart(true); err == nil || !strings.Contains(err.Error(), "开发构建临时程序") {
		t.Fatalf("expected temp go-build exe rejection, got %v", err)
	}
	autoStart.resolveExe = func() (string, error) { return exe1, nil }

	// 3. Enable auto-start and verify quoted `"exe" --autostart` value.
	if err := SetAutoStart(true); err != nil {
		t.Fatalf("SetAutoStart(true) failed: %v", err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("Run key not created: %v", err)
	}
	gotVal, _, err := k.GetStringValue("Shell")
	k.Close()
	wantVal := `"` + exe1 + `" --autostart`
	if err != nil || gotVal != wantVal {
		t.Fatalf("Run\\Shell = (%q, %v), want %q", gotVal, err, wantVal)
	}
	if enabled, err := IsAutoStartEnabled(); err != nil || !enabled {
		t.Fatalf("expected IsAutoStartEnabled=true, got (%v, %v)", enabled, err)
	}

	// 4. Simulate Windows Task Manager disabling the startup entry via StartupApproved (0x03).
	ak, _, err := registry.CreateKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	disabledBytes := []byte{0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if err := ak.SetBinaryValue("Shell", disabledBytes); err != nil {
		ak.Close()
		t.Fatal(err)
	}
	ak.Close()
	if enabled, err := IsAutoStartEnabled(); err != nil || enabled {
		t.Fatalf("expected Task Manager StartupApproved disable to report false, got (%v, %v)", enabled, err)
	}

	// 5. Re-enabling clears StartupApproved and restores enabled=true.
	if err := SetAutoStart(true); err != nil {
		t.Fatalf("re-enable SetAutoStart(true) failed: %v", err)
	}
	if ak, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE); err == nil {
		_, _, valErr := ak.GetBinaryValue("Shell")
		ak.Close()
		if !errors.Is(valErr, registry.ErrNotExist) {
			t.Fatalf("expected StartupApproved\\Shell to be cleared on re-enable, err=%v", valErr)
		}
	}
	if enabled, err := IsAutoStartEnabled(); err != nil || !enabled {
		t.Fatalf("expected IsAutoStartEnabled=true after re-enable, got (%v, %v)", enabled, err)
	}

	// 6. SyncAutoStartPath updates moved portable Shell.exe path automatically.
	autoStart.resolveExe = func() (string, error) { return exe2, nil }
	if err := SyncAutoStartPath(); err != nil {
		t.Fatalf("SyncAutoStartPath failed: %v", err)
	}
	k, err = registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	gotVal, _, err = k.GetStringValue("Shell")
	k.Close()
	wantVal2 := `"` + exe2 + `" --autostart`
	if err != nil || gotVal != wantVal2 {
		t.Fatalf("Run\\Shell after SyncAutoStartPath = (%q, %v), want %q", gotVal, err, wantVal2)
	}

	// 7. Re-create StartupApproved then disable -> BOTH Run\Shell and StartupApproved\Shell deleted cleanly!
	ak, _, err = registry.CreateKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	if err := ak.SetBinaryValue("Shell", []byte{0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		ak.Close()
		t.Fatal(err)
	}
	ak.Close()

	if err := SetAutoStart(false); err != nil {
		t.Fatalf("SetAutoStart(false) failed: %v", err)
	}
	if enabled, err := IsAutoStartEnabled(); err != nil || enabled {
		t.Fatalf("expected disabled after SetAutoStart(false), got (%v, %v)", enabled, err)
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE); err == nil {
		_, _, valErr := k.GetStringValue("Shell")
		k.Close()
		if !errors.Is(valErr, registry.ErrNotExist) {
			t.Fatalf("Run\\Shell residue remained after disable: %v", valErr)
		}
	}
	if ak, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE); err == nil {
		_, _, valErr := ak.GetBinaryValue("Shell")
		ak.Close()
		if !errors.Is(valErr, registry.ErrNotExist) {
			t.Fatalf("StartupApproved\\Shell residue remained after disable: %v", valErr)
		}
	}
}
