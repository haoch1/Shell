package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/remote"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

var (
	modUpdaterOle32   = windows.NewLazySystemDLL("ole32.dll")
	procCoInitEx      = modUpdaterOle32.NewProc("CoInitializeEx")
	procCoUninit      = modUpdaterOle32.NewProc("CoUninitialize")
	procShellExecuteW = modShell32.NewProc("ShellExecuteW")
)

const (
	coinitApartmentThreaded = 0x2
	coinitDisableOLE1DDE    = 0x4
	seErrAccessDenied       = 5
)

const (
	githubRepoOwner   = "haoch1"
	githubRepoName    = "Shell"
	releaseAssetName  = "Shell-Windows-x64.exe"
	checksumAssetName = "SHA256SUMS.txt"
	maxUpdateBytes    = 128 << 20 // 128 MiB safety cap
)

// UpdateInfo describes the latest available GitHub Release relative to the running binary.
type UpdateInfo struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	ReleaseURL     string `json:"releaseUrl"`
	Notes          string `json:"notes"`
	PublishedAt    string `json:"publishedAt"`
	AssetName      string `json:"assetName"`
	AssetSize      int64  `json:"assetSize"`
	DownloadURL    string `json:"downloadUrl"`
	ChecksumsURL   string `json:"checksumsUrl"`
}

// UpdateProgress is streamed via Wails event "update:progress" during in-app updates.
type UpdateProgress struct {
	Phase      string `json:"phase"` // "checking", "downloading", "verifying", "applying", "restarting", "error"
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Percent    int    `json:"percent"`
	Message    string `json:"message"`
}

func resolveSettingsProxy(st local.AppSettings) *url.URL {
	switch strings.ToLower(strings.TrimSpace(st.ProxyMode)) {
	case "socks5", "http":
		if st.ProxyHost != "" && st.ProxyPort > 0 {
			u := &url.URL{
				Scheme: strings.ToLower(strings.TrimSpace(st.ProxyMode)),
				Host:   net.JoinHostPort(st.ProxyHost, strconv.Itoa(st.ProxyPort)),
			}
			if st.ProxyUser != "" {
				u.User = url.User(st.ProxyUser)
			}
			return u
		}
	}
	return nil
}

func resolveSystemProxyURL(sp remote.SystemProxyInfo, host string) *url.URL {
	if !sp.Enabled || sp.ShouldBypass(host) {
		return nil
	}
	scheme := strings.ToLower(strings.TrimSpace(sp.Protocol))
	if scheme == "" {
		scheme = "http"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(sp.Host, strconv.Itoa(sp.Port)),
	}
	if sp.User != "" || sp.Password != "" {
		u.User = url.UserPassword(sp.User, sp.Password)
	}
	return u
}

// outboundHTTPProxy resolves an HTTP/SOCKS5 proxy URL for outbound app requests (AI API & auto-update).
// It honors environment variables first, then configured Shell global proxy, then Windows system proxy.
func outboundHTTPProxy(req *http.Request) (*url.URL, error) {
	if req == nil || req.URL == nil {
		return nil, nil
	}
	host := req.URL.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1" {
		return nil, nil
	}
	if envURL, err := http.ProxyFromEnvironment(req); err == nil && envURL != nil {
		return envURL, nil
	}
	if st, err := local.LoadSettings(); err == nil {
		if u := resolveSettingsProxy(st); u != nil {
			return u, nil
		}
	}
	if u := resolveSystemProxyURL(remote.DetectSystemProxy(), host); u != nil {
		return u, nil
	}
	return nil, nil
}

var updaterTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = outboundHTTPProxy
	return t
}()

func newUpdaterHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: updaterTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 6 {
				return errors.New("更新下载重定向次数过多")
			}
			if !isTrustedReleaseURL(req.URL) {
				return fmt.Errorf("拒绝跳转到非信任更新源：%s", req.URL.Hostname())
			}
			return nil
		},
	}
}

func isTrustedReleaseURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	// Allow loopback HTTP only for local unit tests.
	if u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost") {
		return true
	}
	if u.Scheme != "https" {
		return false
	}
	switch host {
	case "api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com":
		return true
	}
	return strings.HasSuffix(host, ".githubusercontent.com")
}

// compareSemver compares two version strings (with or without leading "v").
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareSemver(a, b string) int {
	parse := func(v string) ([3]int, string) {
		v = strings.TrimSpace(v)
		v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
		pre := ""
		if idx := strings.IndexByte(v, '+'); idx >= 0 {
			v = v[:idx]
		}
		if idx := strings.IndexByte(v, '-'); idx >= 0 {
			pre = v[idx+1:]
			v = v[:idx]
		}
		var nums [3]int
		parts := strings.Split(v, ".")
		for i := 0; i < 3 && i < len(parts); i++ {
			n, _ := strconv.Atoi(parts[i])
			nums[i] = n
		}
		return nums, pre
	}
	an, ap := parse(a)
	bn, bp := parse(b)
	for i := 0; i < 3; i++ {
		if an[i] < bn[i] {
			return -1
		}
		if an[i] > bn[i] {
			return 1
		}
	}
	if ap == "" && bp != "" {
		return 1
	}
	if ap != "" && bp == "" {
		return -1
	}
	if ap < bp {
		return -1
	}
	if ap > bp {
		return 1
	}
	return 0
}

type githubReleaseResp struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func checkReleaseFromAPI(ctx context.Context, client *http.Client, apiURL, currentVersion string) (UpdateInfo, error) {
	info := UpdateInfo{
		CurrentVersion: strings.TrimPrefix(strings.TrimSpace(currentVersion), "v"),
		ReleaseURL:     fmt.Sprintf("https://github.com/%s/%s/releases", githubRepoOwner, githubRepoName),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Shell/"+info.CurrentVersion)

	resp, err := client.Do(req)
	if err != nil {
		return info, fmt.Errorf("检查更新失败，请检查网络或代理设置：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		info.LatestVersion = info.CurrentVersion
		return info, nil
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return info, errors.New("GitHub API 请求频率受限，请稍后再试")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return info, fmt.Errorf("检查更新返回异常状态码 HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return info, fmt.Errorf("读取版本信息失败：%w", err)
	}
	var rel githubReleaseResp
	if err := json.Unmarshal(raw, &rel); err != nil {
		return info, errors.New("解析发布版本信息失败")
	}
	latest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	if latest == "" {
		info.LatestVersion = info.CurrentVersion
		return info, nil
	}
	info.LatestVersion = latest
	if rel.HTMLURL != "" {
		info.ReleaseURL = rel.HTMLURL
	}
	info.Notes = strings.TrimSpace(rel.Body)
	info.PublishedAt = rel.PublishedAt
	info.HasUpdate = !rel.Draft && !rel.Prerelease && compareSemver(info.CurrentVersion, latest) < 0

	for _, a := range rel.Assets {
		switch a.Name {
		case releaseAssetName:
			info.AssetName = a.Name
			info.AssetSize = a.Size
			info.DownloadURL = a.BrowserDownloadURL
		case checksumAssetName:
			info.ChecksumsURL = a.BrowserDownloadURL
		}
	}
	return info, nil
}

func parseSHA256Sums(body, targetFile string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sum := strings.ToLower(strings.TrimSpace(fields[0]))
		name := strings.TrimPrefix(strings.TrimSpace(fields[1]), "*")
		if strings.EqualFold(filepath.Base(name), targetFile) {
			if len(sum) != 64 {
				return "", errors.New("SHA256 校验值长度异常")
			}
			if _, err := hex.DecodeString(sum); err != nil {
				return "", errors.New("SHA256 校验值格式无效")
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("校验文件中未找到 %s 的 SHA-256 摘要", targetFile)
}

func downloadVerifiedExecutable(
	ctx context.Context,
	client *http.Client,
	info UpdateInfo,
	destPath string,
	onProgress func(UpdateProgress),
) error {
	if info.DownloadURL == "" {
		return errors.New("当前发布版本缺少 Windows 可执行程序附件")
	}
	if info.ChecksumsURL == "" {
		return errors.New("当前发布版本缺少 SHA256SUMS.txt 校验文件，为确保安全已中止自动更新")
	}
	dlURL, err := url.Parse(info.DownloadURL)
	if err != nil || !isTrustedReleaseURL(dlURL) {
		return errors.New("更新包下载地址不在受信任的 GitHub 域名范围内")
	}
	sumURL, err := url.Parse(info.ChecksumsURL)
	if err != nil || !isTrustedReleaseURL(sumURL) {
		return errors.New("校验文件地址不在受信任的 GitHub 域名范围内")
	}

	emit := func(p UpdateProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	// 1. Fetch expected SHA-256 digest first
	emit(UpdateProgress{Phase: "downloading", Message: "正在获取校验文件…"})
	sumReq, err := http.NewRequestWithContext(ctx, http.MethodGet, info.ChecksumsURL, nil)
	if err != nil {
		return err
	}
	sumReq.Header.Set("User-Agent", "Shell/"+info.CurrentVersion)
	sumResp, err := client.Do(sumReq)
	if err != nil {
		return fmt.Errorf("下载校验文件失败：%w", err)
	}
	sumRaw, sumReadErr := io.ReadAll(io.LimitReader(sumResp.Body, 64<<10))
	sumResp.Body.Close()
	if sumReadErr != nil {
		return fmt.Errorf("读取校验文件失败：%w", sumReadErr)
	}
	if sumResp.StatusCode < 200 || sumResp.StatusCode >= 300 {
		return fmt.Errorf("获取校验文件失败（HTTP %d）", sumResp.StatusCode)
	}
	expectedSum, err := parseSHA256Sums(string(sumRaw), releaseAssetName)
	if err != nil {
		return err
	}

	// 2. Stream executable to temporary file while hashing
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, info.DownloadURL, nil)
	if err != nil {
		return err
	}
	dlReq.Header.Set("User-Agent", "Shell/"+info.CurrentVersion)
	dlResp, err := client.Do(dlReq)
	if err != nil {
		return fmt.Errorf("下载更新包失败：%w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode < 200 || dlResp.StatusCode >= 300 {
		return fmt.Errorf("下载更新包失败（HTTP %d）", dlResp.StatusCode)
	}

	total := dlResp.ContentLength
	if total <= 0 {
		total = info.AssetSize
	}
	if total > maxUpdateBytes {
		return errors.New("更新包体积超出安全上限")
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("无法创建更新暂存文件：%w", err)
	}
	success := false
	defer func() {
		f.Close()
		if !success {
			_ = os.Remove(destPath)
		}
	}()

	hasher := sha256.New()
	var downloaded int64
	buf := make([]byte, 64<<10)
	var firstBytes [2]byte
	lastEmit := time.Time{}

	for {
		n, rErr := dlResp.Body.Read(buf)
		if n > 0 {
			if downloaded < 2 {
				for i := 0; i < n && downloaded+int64(i) < 2; i++ {
					firstBytes[downloaded+int64(i)] = buf[i]
				}
			}
			downloaded += int64(n)
			if downloaded > maxUpdateBytes {
				return errors.New("更新包大小超出安全限制，已中止")
			}
			if _, wErr := f.Write(buf[:n]); wErr != nil {
				return fmt.Errorf("写入更新文件失败：%w", wErr)
			}
			_, _ = hasher.Write(buf[:n])
			if time.Since(lastEmit) >= 120*time.Millisecond || rErr == io.EOF {
				pct := 0
				if total > 0 {
					pct = int((downloaded * 100) / total)
					if pct > 100 {
						pct = 100
					}
				}
				emit(UpdateProgress{
					Phase:      "downloading",
					Downloaded: downloaded,
					Total:      total,
					Percent:    pct,
					Message:    fmt.Sprintf("正在下载 v%s…", info.LatestVersion),
				})
				lastEmit = time.Now()
			}
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			return fmt.Errorf("下载更新包中断：%w", rErr)
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	// 3. Verify PE header & SHA-256
	emit(UpdateProgress{Phase: "verifying", Downloaded: downloaded, Total: total, Percent: 100, Message: "正在校验 SHA-256 完整性…"})
	if downloaded < 64 || firstBytes[0] != 'M' || firstBytes[1] != 'Z' {
		return errors.New("下载的文件不是有效的 Windows 可执行程序")
	}
	actualSum := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualSum, expectedSum) {
		return fmt.Errorf("SHA-256 校验不匹配（期望 %s…，实际 %s…），已中止更新", expectedSum[:12], actualSum[:12])
	}
	success = true
	return nil
}

func isAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, os.ErrPermission) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) ||
		errors.Is(err, windows.ERROR_ELEVATION_REQUIRED)
}

func isRetryableMoveErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func isProcessElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func isDirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".shell-writecheck-*.tmp")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func sanitizeVersionForFilename(v string) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_' {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		return "latest"
	}
	return b.String()
}

func updateStagingDirPath() string {
	if dir, err := local.UpdatesDir(); err == nil && dir != "" {
		return dir
	}
	if tmp := os.TempDir(); tmp != "" {
		return filepath.Join(tmp, "Shell", "updates")
	}
	return ""
}

func ensureUpdateStagingDir() (string, error) {
	if dir, err := local.UpdatesDir(); err == nil && dir != "" {
		if mkErr := os.MkdirAll(dir, 0700); mkErr == nil && isDirWritable(dir) {
			return dir, nil
		}
	}
	fallback := filepath.Join(os.TempDir(), "Shell", "updates")
	if err := os.MkdirAll(fallback, 0700); err != nil {
		return "", fmt.Errorf("无法创建更新暂存目录：%w", err)
	}
	return fallback, nil
}

func sameFilePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	if ra, err := filepath.EvalSymlinks(ca); err == nil && ra != "" {
		ca = filepath.Clean(ra)
	}
	if rb, err := filepath.EvalSymlinks(cb); err == nil && rb != "" {
		cb = filepath.Clean(rb)
	}
	return strings.EqualFold(ca, cb)
}

func validatePEFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	var hdr [2]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return errors.New("更新文件头部不完整")
	}
	if fi.Size() < 64 || hdr[0] != 'M' || hdr[1] != 'Z' {
		return errors.New("更新文件不是有效的 Windows 可执行程序")
	}
	return nil
}

func copyFileStrict(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = out.Close()
		if !success {
			_ = os.Remove(dst)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	success = true
	return nil
}

func setFileHidden(path string, hidden bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return
	}
	if hidden {
		attrs |= windows.FILE_ATTRIBUTE_HIDDEN
	} else {
		attrs &^= windows.FILE_ATTRIBUTE_HIDDEN
		if attrs == 0 {
			attrs = windows.FILE_ATTRIBUTE_NORMAL
		}
	}
	_ = windows.SetFileAttributes(p, attrs)
}

func clearReadOnlyAttr(path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil || attrs&windows.FILE_ATTRIBUTE_READONLY == 0 {
		return
	}
	attrs &^= windows.FILE_ATTRIBUTE_READONLY
	if attrs == 0 {
		attrs = windows.FILE_ATTRIBUTE_NORMAL
	}
	_ = windows.SetFileAttributes(p, attrs)
}

func scheduleDeleteOnReboot(path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	_ = windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

// delayDeleteOnReboot schedules best-effort deletion of a still-locked file on next reboot.
// Overridable in tests so unit tests never write to HKLM PendingFileRenameOperations.
var delayDeleteOnReboot = scheduleDeleteOnReboot

func moveFileExOnce(from, to string) error {
	fromPtr, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPtr, toPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func moveFileWithRetry(from, to string, attempts int, baseDelay, maxDelay time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		lastErr = moveFileExOnce(from, to)
		if lastErr == nil {
			return nil
		}
		if !isRetryableMoveErr(lastErr) || i == attempts-1 {
			return lastErr
		}
		delay := baseDelay * time.Duration(i+1)
		if maxDelay > 0 && delay > maxDelay {
			delay = maxDelay
		}
		time.Sleep(delay)
	}
	return lastErr
}

func uniqueOldBackupPath(exePath string) string {
	candidate := fmt.Sprintf("%s.old.%d", exePath, os.Getpid())
	if _, err := os.Stat(candidate); err == nil {
		// If a same-PID backup already exists and cannot be removed, disambiguate with a timestamp.
		if os.Remove(candidate) != nil {
			return fmt.Sprintf("%s.old.%d.%d", exePath, os.Getpid(), time.Now().UnixNano())
		}
	}
	return candidate
}

// swapExecutableAtomically replaces exePath with newPath on Windows using rename-in-place.
// It uses a unique per-PID backup filename so a stale/locked legacy .old file never blocks updates,
// and uses MoveFileEx with short retry backoff for transient Defender/sharing locks.
func swapExecutableAtomically(exePath, newPath string) (oldPath string, err error) {
	oldPath = uniqueOldBackupPath(exePath)
	if err := moveFileWithRetry(exePath, oldPath, 6, 100*time.Millisecond, 300*time.Millisecond); err != nil {
		return "", fmt.Errorf("无法替换运行中的程序文件（目录可能受系统保护）：%w", err)
	}
	if err := moveFileWithRetry(newPath, exePath, 6, 100*time.Millisecond, 300*time.Millisecond); err != nil {
		// Roll back immediately so the user never ends up with a missing executable.
		_ = moveFileWithRetry(oldPath, exePath, 6, 100*time.Millisecond, 300*time.Millisecond)
		return "", fmt.Errorf("应用新版本文件失败，已自动回滚：%w", err)
	}
	return oldPath, nil
}

func joinWindowsArgs(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = syscall.EscapeArg(arg)
	}
	return strings.Join(parts, " ")
}

func shellExecuteElevated(exePath string, args []string) error {
	errCh := make(chan error, 1)
	go func() {
		goruntime.LockOSThread()
		defer goruntime.UnlockOSThread()

		hr, _, _ := procCoInitEx.Call(0, coinitApartmentThreaded|coinitDisableOLE1DDE)
		if hr == 0 || hr == 1 { // S_OK or S_FALSE
			defer procCoUninit.Call()
		}

		verbPtr, err := windows.UTF16PtrFromString("runas")
		if err != nil {
			errCh <- err
			return
		}
		filePtr, err := windows.UTF16PtrFromString(exePath)
		if err != nil {
			errCh <- err
			return
		}
		argsPtr, err := windows.UTF16PtrFromString(joinWindowsArgs(args))
		if err != nil {
			errCh <- err
			return
		}
		dirPtr, err := windows.UTF16PtrFromString(filepath.Dir(exePath))
		if err != nil {
			errCh <- err
			return
		}

		r1, _, e1 := procShellExecuteW.Call(
			0,
			uintptr(unsafe.Pointer(verbPtr)),
			uintptr(unsafe.Pointer(filePtr)),
			uintptr(unsafe.Pointer(argsPtr)),
			uintptr(unsafe.Pointer(dirPtr)),
			uintptr(windows.SW_HIDE),
		)
		if r1 <= 32 {
			if r1 == seErrAccessDenied || r1 == uintptr(windows.ERROR_CANCELLED) ||
				errors.Is(e1, windows.ERROR_CANCELLED) || errors.Is(e1, windows.ERROR_ACCESS_DENIED) {
				errCh <- errors.New("已取消管理员授权，未能替换受保护目录中的程序文件")
				return
			}
			if errno, ok := e1.(syscall.Errno); ok && errno != 0 {
				errCh <- fmt.Errorf("无法以管理员权限启动更新程序：%w", errno)
				return
			}
			errCh <- fmt.Errorf("无法以管理员权限启动更新程序（错误码 %d）", r1)
			return
		}
		errCh <- nil
	}()
	return <-errCh
}

func launchDetachedUpdater(stagedPath string, args []string) error {
	cmd := exec.Command(stagedPath, args...)
	cmd.Dir = filepath.Dir(stagedPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	return cmd.Start()
}

func relaunchExecutable(targetExe string, elevated bool) error {
	if elevated {
		// Launch through explorer.exe so the restarted Shell runs at standard desktop
		// integrity rather than remaining elevated as Administrator.
		explorer := "explorer.exe"
		if sysRoot := os.Getenv("SystemRoot"); sysRoot != "" {
			candidate := filepath.Join(sysRoot, "explorer.exe")
			if _, err := os.Stat(candidate); err == nil {
				explorer = candidate
			}
		}
		cmd := exec.Command(explorer, targetExe)
		cmd.Dir = filepath.Dir(targetExe)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		}
		if err := cmd.Start(); err == nil {
			return nil
		}
	}
	cmd := exec.Command(targetExe, "--updated-cleanup")
	cmd.Dir = filepath.Dir(targetExe)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd.Start()
}

func waitForProcessExit(pid int, timeout time.Duration) {
	if pid <= 0 || pid == os.Getpid() {
		return
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// If the process already exited before we started, OpenProcess fails with
		// ERROR_INVALID_PARAMETER. If access is denied, pause briefly and let the
		// replacement retry loop handle any transient file locks.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			time.Sleep(500 * time.Millisecond)
		}
		return
	}
	defer windows.CloseHandle(h)

	ms := uint32(timeout / time.Millisecond)
	if ms == 0 {
		ms = 1
	}
	ev, _ := windows.WaitForSingleObject(h, ms)
	if ev == windows.WAIT_OBJECT_0 {
		// Give the OS kernel a brief moment to release the mapped image handle.
		time.Sleep(100 * time.Millisecond)
	}
}

func applyStagedUpdate(parentPID int, stagedExe, targetExe string, relaunch bool) error {
	targetExe = strings.TrimSpace(targetExe)
	stagedExe = strings.TrimSpace(stagedExe)
	if targetExe == "" {
		return errors.New("未指定目标程序路径")
	}
	if stagedExe == "" {
		return errors.New("未指定暂存更新文件路径")
	}
	targetExe = filepath.Clean(targetExe)
	stagedExe = filepath.Clean(stagedExe)
	if resolved, err := filepath.EvalSymlinks(targetExe); err == nil && resolved != "" {
		targetExe = resolved
	}
	if err := validatePEFile(stagedExe); err != nil {
		return err
	}

	waitForProcessExit(parentPID, 20*time.Second)

	if !sameFilePath(stagedExe, targetExe) {
		targetDir := filepath.Dir(targetExe)
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return fmt.Errorf("无法访问目标程序目录：%w", err)
		}
		sameDirTemp := fmt.Sprintf("%s.new.%d", targetExe, os.Getpid())
		if err := copyFileStrict(stagedExe, sameDirTemp); err != nil {
			return fmt.Errorf("复制更新文件至目标目录失败：%w", err)
		}
		// Keep temporary staging file hidden while swapping so Explorer stays clean.
		setFileHidden(sameDirTemp, true)
		replaced := false
		defer func() {
			if !replaced {
				_ = os.Remove(sameDirTemp)
			}
		}()

		clearReadOnlyAttr(targetExe)

		// Phase 1: Direct atomic replacement (ZERO .old file created in normal updates!).
		// Retry with short backoff for transient OS teardown or AV scanning locks.
		var lastErr error
		const directAttempts = 8
		for attempt := 0; attempt < directAttempts; attempt++ {
			lastErr = moveFileExOnce(sameDirTemp, targetExe)
			if lastErr == nil {
				setFileHidden(targetExe, false)
				replaced = true
				break
			}
			if !isRetryableMoveErr(lastErr) {
				break
			}
			if attempt < directAttempts-1 {
				delay := time.Duration(100+50*attempt) * time.Millisecond
				if delay > 250*time.Millisecond {
					delay = 250 * time.Millisecond
				}
				time.Sleep(delay)
			}
		}

		// Phase 2: Rare fallback only when another process (e.g. Shell.exe --mcp) still holds
		// targetExe open after direct retries. Rename target aside into a hidden file, place the
		// new executable, immediately clear HIDDEN on target, and delete (or delay-delete) the aside file.
		if !replaced && isRetryableMoveErr(lastErr) {
			const fallbackAttempts = 17
			for attempt := 0; attempt < fallbackAttempts; attempt++ {
				if err := moveFileExOnce(sameDirTemp, targetExe); err == nil {
					setFileHidden(targetExe, false)
					replaced = true
					break
				} else if !isRetryableMoveErr(err) {
					lastErr = err
					break
				} else {
					lastErr = err
				}

				asidePath := uniqueOldBackupPath(targetExe)
				if moveFileExOnce(targetExe, asidePath) == nil {
					setFileHidden(asidePath, true)
					if err := moveFileExOnce(sameDirTemp, targetExe); err == nil {
						setFileHidden(targetExe, false)
						if os.Remove(asidePath) != nil {
							delayDeleteOnReboot(asidePath)
						}
						replaced = true
						break
					} else {
						lastErr = err
					}
					_ = moveFileWithRetry(asidePath, targetExe, 6, 100*time.Millisecond, 300*time.Millisecond)
					setFileHidden(targetExe, false)
				}
				if attempt < fallbackAttempts-1 {
					time.Sleep(250 * time.Millisecond)
				}
			}
		}
		if !replaced {
			return fmt.Errorf("替换目标程序失败：%w", lastErr)
		}

		// Clean up any pre-existing legacy *.old* / *.new* files in the install directory
		// right away (including while elevated for protected directories like Program Files).
		_ = cleanupStaleUpdateFiles(targetDir, filepath.Base(targetExe))
	}

	if relaunch {
		if err := relaunchExecutable(targetExe, isProcessElevated()); err != nil {
			return fmt.Errorf("重启更新后的程序失败：%w", err)
		}
	}
	return nil
}

func runApplyUpdateCLI(args []string) int {
	fs := flag.NewFlagSet("apply-update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	applyFlag := fs.Bool("apply-update", false, "")
	pid := fs.Int("pid", 0, "")
	target := fs.String("target", "", "")
	relaunch := fs.Bool("relaunch", false, "")

	if err := fs.Parse(args); err != nil || !*applyFlag || strings.TrimSpace(*target) == "" {
		return 2
	}
	selfExe, err := os.Executable()
	if err != nil || selfExe == "" {
		fmt.Fprintln(os.Stderr, "无法定位当前更新程序路径")
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(selfExe); err == nil && resolved != "" {
		selfExe = resolved
	}
	if err := applyStagedUpdate(*pid, selfExe, *target, *relaunch); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func isStaleExeSibling(name, exeBase string) bool {
	lowerName := strings.ToLower(name)
	lowerBase := strings.ToLower(exeBase)
	if lowerName == "" || lowerBase == "" || lowerName == lowerBase {
		return false
	}
	if lowerName == lowerBase+".old" || strings.HasPrefix(lowerName, lowerBase+".old.") {
		return true
	}
	if lowerName == lowerBase+".new" || strings.HasPrefix(lowerName, lowerBase+".new.") {
		return true
	}
	if strings.HasPrefix(lowerName, ".shell-writecheck-") && strings.HasSuffix(lowerName, ".tmp") {
		return true
	}
	return false
}

// cleanupStaleUpdateFiles removes leftover .old*, .new*, and write-check probe files
// next to exeBase inside dir without touching exeBase itself.
// It returns true if no matching stale files remain.
func cleanupStaleUpdateFiles(dir, exeBase string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	allRemoved := true
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !isStaleExeSibling(entry.Name(), exeBase) {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			clearReadOnlyAttr(fullPath)
			if retryErr := os.Remove(fullPath); retryErr != nil && !os.IsNotExist(retryErr) {
				allRemoved = false
			}
		}
	}
	return allRemoved
}

func isStagedUpdateFilename(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "shell-update-") && strings.HasSuffix(lower, ".exe")
}

// cleanupUpdateStagingDir removes stale staged update executables older than minAge
// (never removing currentExe). It returns true if no staged update files remain.
func cleanupUpdateStagingDir(stageDir, currentExe string, minAge time.Duration) bool {
	if stageDir == "" {
		return true
	}
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return true
	}
	remaining := false
	for _, entry := range entries {
		if entry.IsDir() || !isStagedUpdateFilename(entry.Name()) {
			continue
		}
		fullPath := filepath.Join(stageDir, entry.Name())
		if sameFilePath(fullPath, currentExe) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if minAge > 0 && time.Since(info.ModTime()) < minAge {
			remaining = true
			continue
		}
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			remaining = true
		}
	}
	return !remaining
}

// cleanupPreviousExecutable removes any leftover .old*/.new.* files next to the running binary
// as well as stale staged update binaries in %LOCALAPPDATA%\Shell\runtime\updates.
// It retries for up to 60s so files briefly held by exiting background processes (e.g. --mcp)
// are cleaned up automatically without user intervention.
func cleanupPreviousExecutable() {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && resolved != "" {
		exePath = resolved
	}
	dir := filepath.Dir(exePath)
	base := filepath.Base(exePath)
	stageDir := updateStagingDirPath()

	go func() {
		deadline := time.Now().Add(60 * time.Second)
		for attempt := 0; ; attempt++ {
			exeClean := cleanupStaleUpdateFiles(dir, base)
			stageClean := cleanupUpdateStagingDir(stageDir, exePath, 3*time.Second)
			if exeClean && stageClean {
				return
			}
			if time.Now().After(deadline) {
				return
			}
			if attempt < 12 {
				time.Sleep(400 * time.Millisecond)
			} else {
				time.Sleep(2 * time.Second)
			}
		}
	}()
}

// CheckForUpdate queries GitHub Releases for a newer version of Shell.
func (a *App) CheckForUpdate() (UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(a.baseContext(), 15*time.Second)
	defer cancel()
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubRepoOwner, githubRepoName)
	return checkReleaseFromAPI(ctx, newUpdaterHTTPClient(15*time.Second), apiURL, appVersion)
}

// DownloadAndApplyUpdate downloads the latest Shell-Windows-x64.exe into a user-writable
// staging directory, verifies PE header & SHA-256, and hands off replacement to the staged
// binary (--apply-update) before quitting so the install directory never accumulates .old files.
func (a *App) DownloadAndApplyUpdate() error {
	a.updateMu.Lock()
	if a.updating {
		a.updateMu.Unlock()
		return errors.New("正在下载或安装更新，请稍候")
	}
	a.updating = true
	a.updateMu.Unlock()
	defer func() {
		a.updateMu.Lock()
		a.updating = false
		a.updateMu.Unlock()
	}()

	emit := func(p UpdateProgress) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "update:progress", p)
		}
	}

	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return errors.New("无法定位当前程序路径")
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && resolved != "" {
		exePath = resolved
	}

	emit(UpdateProgress{Phase: "checking", Message: "正在获取最新版本信息…"})
	info, err := a.CheckForUpdate()
	if err != nil {
		emit(UpdateProgress{Phase: "error", Message: err.Error()})
		return err
	}
	if !info.HasUpdate {
		return errors.New("当前已是最新版本，无需更新")
	}

	// 1. Always stage & verify in user-writable %LOCALAPPDATA%\Shell\runtime\updates\ first.
	stageDir, err := ensureUpdateStagingDir()
	if err != nil {
		emit(UpdateProgress{Phase: "error", Message: err.Error()})
		return err
	}
	stagedPath := filepath.Join(
		stageDir,
		fmt.Sprintf("Shell-update-%s-%d.exe", sanitizeVersionForFilename(info.LatestVersion), os.Getpid()),
	)

	ctx, cancel := context.WithTimeout(a.baseContext(), 5*time.Minute)
	defer cancel()

	if err := downloadVerifiedExecutable(ctx, newUpdaterHTTPClient(5*time.Minute), info, stagedPath, emit); err != nil {
		emit(UpdateProgress{Phase: "error", Message: err.Error()})
		return err
	}

	// 2. Always hand off to the verified staged binary via --apply-update so this process exits
	// cleanly first and targetExe is replaced directly in place without leaving any .old file.
	applyArgs := []string{
		"--apply-update",
		"--pid", strconv.Itoa(os.Getpid()),
		"--target", exePath,
		"--relaunch",
	}
	dirWritable := isDirWritable(filepath.Dir(exePath))
	var launchErr error
	if !dirWritable && !isProcessElevated() {
		emit(UpdateProgress{Phase: "applying", Percent: 100, Message: "正在请求管理员权限以替换程序文件…"})
		launchErr = shellExecuteElevated(stagedPath, applyArgs)
	} else {
		emit(UpdateProgress{Phase: "applying", Percent: 100, Message: "正在准备替换程序文件…"})
		launchErr = launchDetachedUpdater(stagedPath, applyArgs)
	}
	if launchErr != nil {
		_ = os.Remove(stagedPath)
		emit(UpdateProgress{Phase: "error", Message: launchErr.Error()})
		return launchErr
	}

	emit(UpdateProgress{Phase: "restarting", Percent: 100, Message: fmt.Sprintf("已更新至 v%s，正在重启…", info.LatestVersion)})
	go func() {
		time.Sleep(120 * time.Millisecond)
		a.QuitApp()
	}()
	return nil
}
