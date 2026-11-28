//go:build windows

package localpty

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetectShells(t *testing.T) {
	shells := DetectShells()
	if len(shells) == 0 {
		t.Fatal("DetectShells() returned empty list; expected at least cmd or powershell")
	}

	defaultCount := 0
	hasCoreShell := false
	for _, s := range shells {
		if s.ID == "" || s.Name == "" || s.Command == "" {
			t.Errorf("invalid shell profile: %+v", s)
		}
		if s.Category != "shell" && s.Category != "wsl" {
			t.Errorf("unexpected category %q in %+v", s.Category, s)
		}
		if s.IsDefault {
			defaultCount++
		}
		if s.ID == "cmd" || s.ID == "powershell" || s.ID == "pwsh" {
			hasCoreShell = true
		}
		lowerID := strings.ToLower(s.ID)
		if strings.Contains(lowerID, "docker-desktop") {
			t.Errorf("docker-desktop distro must be filtered out, got %+v", s)
		}
		t.Logf("detected shell: id=%s name=%q category=%s icon=%s default=%v cmd=%s args=%v",
			s.ID, s.Name, s.Category, s.Icon, s.IsDefault, s.Command, s.Args)
	}

	if !hasCoreShell {
		t.Fatalf("expected at least one of pwsh/powershell/cmd, got %+v", shells)
	}
	if defaultCount != 1 {
		t.Fatalf("expected exactly 1 default shell, got %d", defaultCount)
	}

	def, err := ResolveShell("")
	if err != nil {
		t.Fatalf("ResolveShell(\"\") failed: %v", err)
	}
	if !def.IsDefault {
		t.Fatalf("ResolveShell(\"\") returned non-default shell: %+v", def)
	}

	if _, err := ResolveShell("non-existent-shell-id"); err == nil {
		t.Fatal("expected error for unknown shell ID, got nil")
	}

	fallback, err := ResolveShellOrDefault("non-existent-shell-id")
	if err != nil {
		t.Fatalf("ResolveShellOrDefault fallback failed: %v", err)
	}
	if fallback.ID != def.ID {
		t.Fatalf("expected fallback %q, got %q", def.ID, fallback.ID)
	}
}

func TestLocalPTY_EchoResizeAndExit(t *testing.T) {
	cmdShell, err := ResolveShell("cmd")
	if err != nil {
		t.Fatalf("ResolveShell(\"cmd\") failed: %v", err)
	}

	var outMu sync.Mutex
	var output bytes.Buffer
	markerFound := make(chan struct{}, 1)

	var exitCalls atomic.Int32
	exitCh := make(chan int, 1)

	sess, err := Start(Options{
		Command: cmdShell.Command,
		Args:    cmdShell.Args,
		Cols:    80,
		Rows:    24,
		OnOutput: func(chunk []byte) {
			outMu.Lock()
			output.Write(chunk)
			hasMarker := bytes.Contains(output.Bytes(), []byte("SHELL_PTY_OK_12345"))
			outMu.Unlock()
			if hasMarker {
				select {
				case markerFound <- struct{}{}:
				default:
				}
			}
		},
		OnExit: func(exitCode int, exitErr error) {
			if exitErr != nil {
				t.Errorf("OnExit received error: %v", exitErr)
			}
			exitCalls.Add(1)
			select {
			case exitCh <- exitCode:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("Start(cmd.exe) failed: %v", err)
	}
	defer sess.Close()

	if err := sess.Write([]byte("echo SHELL_PTY_OK_12345\r\n")); err != nil {
		t.Fatalf("Write(echo) failed: %v", err)
	}

	select {
	case <-markerFound:
	case <-time.After(8 * time.Second):
		outMu.Lock()
		got := output.String()
		outMu.Unlock()
		t.Fatalf("timed out waiting for SHELL_PTY_OK_12345; captured output: %q", got)
	}

	if err := sess.Resize(100, 32); err != nil {
		t.Fatalf("Resize(100, 32) failed: %v", err)
	}

	if err := sess.Write([]byte("exit 0\r\n")); err != nil {
		t.Fatalf("Write(exit 0) failed: %v", err)
	}

	select {
	case code := <-exitCh:
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d", code)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for OnExit after 'exit 0'")
	}

	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session Done channel not closed after OnExit")
	}

	if calls := exitCalls.Load(); calls != 1 {
		t.Fatalf("expected OnExit to be called exactly once, got %d", calls)
	}

	// Idempotent Close after natural exit must succeed cleanly.
	if err := sess.Close(); err != nil {
		t.Fatalf("Close after exit failed: %v", err)
	}
}

func TestLocalPTY_CloseKillsProcessTree(t *testing.T) {
	cmdShell, err := ResolveShell("cmd")
	if err != nil {
		t.Fatalf("ResolveShell(\"cmd\") failed: %v", err)
	}

	exitCh := make(chan int, 1)
	var exitCalls atomic.Int32

	sess, err := Start(Options{
		Command: cmdShell.Command,
		Args:    []string{"/c", "ping", "-n", "30", "127.0.0.1"},
		Cols:    80,
		Rows:    24,
		OnOutput: func([]byte) {
			// Drain output continuously.
		},
		OnExit: func(exitCode int, _ error) {
			exitCalls.Add(1)
			select {
			case exitCh <- exitCode:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("Start(cmd /c ping) failed: %v", err)
	}

	// Allow cmd.exe to spawn child ping.exe before closing.
	time.Sleep(250 * time.Millisecond)

	start := time.Now()
	if err := sess.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	// Second Close() must be idempotent and non-blocking.
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close() failed: %v", err)
	}

	select {
	case <-exitCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for process tree exit after Close() (%v elapsed)", time.Since(start))
	}

	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session Done channel not closed after Close()")
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close() teardown took too long: %v", elapsed)
	}
	if calls := exitCalls.Load(); calls != 1 {
		t.Fatalf("expected OnExit once, got %d", calls)
	}
}

func TestLocalPTY_EnvInjectionAndWSLMeta(t *testing.T) {
	// Verify WSL filtering & icon mapping helpers.
	if !isIgnoredWSLDistro("docker-desktop") || !isIgnoredWSLDistro("Docker-Desktop-Data") {
		t.Fatal("expected docker-desktop distros to be ignored")
	}
	if isIgnoredWSLDistro("Ubuntu-24.04") {
		t.Fatal("Ubuntu-24.04 must not be ignored")
	}
	iconCases := map[string]string{
		"Ubuntu-24.04": "ubuntu",
		"Debian":       "debian",
		"kali-linux":   "debian",
		"Arch":         "arch",
		"Alpine":       "alpine",
		"Fedora-40":    "fedora",
		"openSUSE":     "linux",
		"CustomDistro": "linux",
	}
	for distro, want := range iconCases {
		if got := wslIconForDistro(distro); got != want {
			t.Errorf("wslIconForDistro(%q) = %q, want %q", distro, got, want)
		}
	}

	// Verify real cmd.exe receives injected TERM / COLORTERM / TERM_PROGRAM / WT_SESSION.
	cmdShell, err := ResolveShell("cmd")
	if err != nil {
		t.Fatalf("ResolveShell(\"cmd\") failed: %v", err)
	}

	var outMu sync.Mutex
	var output bytes.Buffer
	foundCh := make(chan struct{}, 1)
	exitCh := make(chan int, 1)

	sess, err := Start(Options{
		Command: cmdShell.Command,
		Args:    cmdShell.Args,
		Cols:    120,
		Rows:    30,
		OnOutput: func(chunk []byte) {
			outMu.Lock()
			output.Write(chunk)
			ok := bytes.Contains(output.Bytes(), []byte("ENVCHECK:xterm-256color|truecolor|Shell|"))
			outMu.Unlock()
			if ok {
				select {
				case foundCh <- struct{}{}:
				default:
				}
			}
		},
		OnExit: func(code int, _ error) {
			select {
			case exitCh <- code:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sess.Close()

	if err := sess.Write([]byte("echo ENVCHECK:%TERM%^|%COLORTERM%^|%TERM_PROGRAM%^|%WT_SESSION%\r\nexit 0\r\n")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	select {
	case <-foundCh:
	case <-time.After(8 * time.Second):
		outMu.Lock()
		got := output.String()
		outMu.Unlock()
		t.Fatalf("timed out waiting for injected env vars; captured: %q", got)
	}

	select {
	case code := <-exitCh:
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for cmd exit")
	}
}

