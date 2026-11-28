//go:build windows

package localpty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// ShellProfile describes a locally installed shell or WSL distribution available for ConPTY sessions.
type ShellProfile struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Icon      string   `json:"icon"`     // "powershell" | "cmd" | "git" | "ubuntu" | "debian" | "arch" | "alpine" | "fedora" | "linux" | "terminal"
	Category  string   `json:"category"` // "shell" | "wsl"
	IsDefault bool     `json:"isDefault,omitempty"`
}

// DetectShells discovers installed local Windows shells and WSL distributions using pure
// filesystem and registry lookups (never spawns subprocesses).
func DetectShells() []ShellProfile {
	var profiles []ShellProfile

	if p := detectPwsh(); p != "" {
		profiles = append(profiles, ShellProfile{
			ID:       "pwsh",
			Name:     "PowerShell 7",
			Command:  p,
			Args:     []string{"-NoLogo"},
			Icon:     "powershell",
			Category: "shell",
		})
	}

	if p := detectWindowsPowerShell(); p != "" {
		profiles = append(profiles, ShellProfile{
			ID:       "powershell",
			Name:     "Windows PowerShell",
			Command:  p,
			Args:     []string{"-NoLogo"},
			Icon:     "powershell",
			Category: "shell",
		})
	}

	if p := detectCmd(); p != "" {
		profiles = append(profiles, ShellProfile{
			ID:       "cmd",
			Name:     "命令提示符",
			Command:  p,
			Args:     nil,
			Icon:     "cmd",
			Category: "shell",
		})
	}

	if p := detectGitBash(); p != "" {
		profiles = append(profiles, ShellProfile{
			ID:       "git-bash",
			Name:     "Git Bash",
			Command:  p,
			Args:     []string{"--login", "-i"},
			Icon:     "git",
			Category: "shell",
		})
	}

	profiles = append(profiles, detectWSLDistros()...)

	if len(profiles) > 0 {
		profiles[0].IsDefault = true
	}
	return profiles
}

// ResolveShell returns the ShellProfile matching shellID, or the default shell if shellID is empty.
func ResolveShell(shellID string) (ShellProfile, error) {
	id := strings.TrimSpace(shellID)
	shells := DetectShells()
	if len(shells) == 0 {
		return ShellProfile{}, errors.New("未在系统中检测到可用的本地终端 Shell")
	}
	if id == "" {
		for _, s := range shells {
			if s.IsDefault {
				return s, nil
			}
		}
		return shells[0], nil
	}
	for _, s := range shells {
		if s.ID == id {
			return s, nil
		}
	}
	for _, s := range shells {
		if strings.EqualFold(s.ID, id) {
			return s, nil
		}
	}
	return ShellProfile{}, fmt.Errorf("未找到指定的本地终端：%s", id)
}

// ResolveShellOrDefault resolves preferredID if available, otherwise gracefully falls back
// to the current system default shell.
func ResolveShellOrDefault(preferredID string) (ShellProfile, error) {
	id := strings.TrimSpace(preferredID)
	if id != "" {
		if s, err := ResolveShell(id); err == nil {
			return s, nil
		}
	}
	return ResolveShell("")
}

func systemRoot() string {
	if v := strings.TrimSpace(os.Getenv("SystemRoot")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("WINDIR")); v != "" {
		return v
	}
	return `C:\Windows`
}

func fileExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err == nil {
		return !info.IsDir()
	}
	return false
}

func firstExistingFile(candidates ...string) string {
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		cleaned := filepath.Clean(c)
		key := strings.ToLower(cleaned)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if fileExists(cleaned) {
			if abs, err := filepath.Abs(cleaned); err == nil {
				return abs
			}
			return cleaned
		}
	}
	return ""
}

func lookPathSafe(exe string) string {
	p, err := exec.LookPath(exe)
	if err != nil {
		return ""
	}
	return p
}

func detectPwsh() string {
	var candidates []string
	if p := lookPathSafe("pwsh.exe"); p != "" {
		candidates = append(candidates, p)
	}
	for _, envName := range []string{"ProgramFiles", "ProgramW6432"} {
		if base := strings.TrimSpace(os.Getenv(envName)); base != "" {
			candidates = append(candidates,
				filepath.Join(base, "PowerShell", "7", "pwsh.exe"),
				filepath.Join(base, "PowerShell", "7-preview", "pwsh.exe"),
			)
		}
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		candidates = append(candidates,
			filepath.Join(localAppData, "Microsoft", "WindowsApps", "pwsh.exe"),
		)
	}
	return firstExistingFile(candidates...)
}

func detectWindowsPowerShell() string {
	return firstExistingFile(
		filepath.Join(systemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		lookPathSafe("powershell.exe"),
	)
}

func detectCmd() string {
	return firstExistingFile(
		os.Getenv("COMSPEC"),
		filepath.Join(systemRoot(), "System32", "cmd.exe"),
		lookPathSafe("cmd.exe"),
	)
}

func readRegistryString(root registry.Key, subkey, valueName string) string {
	for _, access := range []uint32{
		registry.QUERY_VALUE | registry.WOW64_64KEY,
		registry.QUERY_VALUE,
	} {
		k, err := registry.OpenKey(root, subkey, access)
		if err != nil {
			continue
		}
		val, _, err := k.GetStringValue(valueName)
		_ = k.Close()
		if err == nil && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func detectGitBash() string {
	var roots []string
	if p := readRegistryString(registry.LOCAL_MACHINE, `SOFTWARE\GitForWindows`, "InstallPath"); p != "" {
		roots = append(roots, p)
	}
	if p := readRegistryString(registry.CURRENT_USER, `SOFTWARE\GitForWindows`, "InstallPath"); p != "" {
		roots = append(roots, p)
	}
	for _, envName := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		if base := strings.TrimSpace(os.Getenv(envName)); base != "" {
			roots = append(roots, filepath.Join(base, "Git"))
		}
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		roots = append(roots, filepath.Join(localAppData, "Programs", "Git"))
	}
	if gitExe := lookPathSafe("git.exe"); gitExe != "" {
		dir := filepath.Dir(gitExe)
		parent := filepath.Dir(dir)
		grandParent := filepath.Dir(parent)
		roots = append(roots, parent, grandParent, dir)
	}

	var candidates []string
	seenRoots := make(map[string]struct{}, len(roots))
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		cleaned := filepath.Clean(r)
		key := strings.ToLower(cleaned)
		if _, dup := seenRoots[key]; dup {
			continue
		}
		seenRoots[key] = struct{}{}
		candidates = append(candidates,
			filepath.Join(cleaned, "bin", "bash.exe"),
			filepath.Join(cleaned, "usr", "bin", "bash.exe"),
		)
	}
	return firstExistingFile(candidates...)
}

type wslDistroEntry struct {
	name      string
	isDefault bool
}

func detectWSLDistros() []ShellProfile {
	wslExe := firstExistingFile(filepath.Join(systemRoot(), "System32", "wsl.exe"))
	if wslExe == "" {
		return nil
	}

	k, err := registry.OpenKey(
		registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Lxss`,
		registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE,
	)
	if err != nil {
		return nil
	}
	defer k.Close()

	defaultGUID, _, _ := k.GetStringValue("DefaultDistribution")
	defaultGUID = strings.TrimSpace(defaultGUID)

	subkeys, err := k.ReadSubKeyNames(-1)
	if err != nil || len(subkeys) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(subkeys))
	entries := make([]wslDistroEntry, 0, len(subkeys))
	for _, sub := range subkeys {
		sk, err := registry.OpenKey(k, sub, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		distro, _, err := sk.GetStringValue("DistributionName")
		_ = sk.Close()
		if err != nil {
			continue
		}
		distro = strings.TrimSpace(distro)
		if distro == "" || isIgnoredWSLDistro(distro) {
			continue
		}
		lower := strings.ToLower(distro)
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		entries = append(entries, wslDistroEntry{
			name:      distro,
			isDefault: defaultGUID != "" && strings.EqualFold(strings.TrimSpace(sub), defaultGUID),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isDefault != entries[j].isDefault {
			return entries[i].isDefault
		}
		li, lj := strings.ToLower(entries[i].name), strings.ToLower(entries[j].name)
		if li != lj {
			return li < lj
		}
		return entries[i].name < entries[j].name
	})

	profiles := make([]ShellProfile, 0, len(entries))
	for _, e := range entries {
		profiles = append(profiles, ShellProfile{
			ID:       "wsl:" + e.name,
			Name:     "WSL · " + e.name,
			Command:  wslExe,
			Args:     []string{"-d", e.name, "--cd", "~"},
			Icon:     wslIconForDistro(e.name),
			Category: "wsl",
		})
	}
	return profiles
}

func isIgnoredWSLDistro(distro string) bool {
	lower := strings.ToLower(strings.TrimSpace(distro))
	return lower == "docker-desktop" || lower == "docker-desktop-data"
}

func wslIconForDistro(distro string) string {
	lower := strings.ToLower(strings.TrimSpace(distro))
	switch {
	case strings.Contains(lower, "ubuntu"):
		return "ubuntu"
	case strings.Contains(lower, "debian"), strings.Contains(lower, "kali"):
		return "debian"
	case strings.Contains(lower, "arch"), strings.Contains(lower, "manjaro"):
		return "arch"
	case strings.Contains(lower, "alpine"):
		return "alpine"
	case strings.Contains(lower, "fedora"):
		return "fedora"
	case strings.Contains(lower, "opensuse"), strings.Contains(lower, "suse"):
		return "linux"
	default:
		return "linux"
	}
}
