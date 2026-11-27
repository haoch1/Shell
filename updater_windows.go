package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/remote"
	"github.com/wailsapp/wails/v2/pkg/runtime"
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
		return fmt.Errorf("无法写入更新文件（请确认程序所在目录有写入权限）：%w", err)
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

// swapExecutableAtomically replaces exePath with newPath on Windows using rename-in-place.
// Windows allows renaming a currently executing .exe while blocking direct overwrite/delete.
func swapExecutableAtomically(exePath, newPath string) (oldPath string, err error) {
	oldPath = exePath + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(exePath, oldPath); err != nil {
		return "", fmt.Errorf("无法替换运行中的程序文件（目录可能受系统保护）：%w", err)
	}
	if err := os.Rename(newPath, exePath); err != nil {
		// Roll back immediately so the user never ends up with a missing executable.
		_ = os.Rename(oldPath, exePath)
		return "", fmt.Errorf("应用新版本文件失败，已自动回滚：%w", err)
	}
	return oldPath, nil
}

// cleanupPreviousExecutable removes any leftover Shell.exe.old created during an in-app self-update.
func cleanupPreviousExecutable() {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && resolved != "" {
		exePath = resolved
	}
	oldPath := exePath + ".old"
	if _, err := os.Stat(oldPath); err != nil {
		return
	}
	go func() {
		for i := 0; i < 15; i++ {
			if os.Remove(oldPath) == nil {
				return
			}
			time.Sleep(350 * time.Millisecond)
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

// DownloadAndApplyUpdate downloads the latest Shell-Windows-x64.exe, verifies SHA-256, swaps the binary, and restarts Shell.
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

	newPath := exePath + ".new"
	ctx, cancel := context.WithTimeout(a.baseContext(), 5*time.Minute)
	defer cancel()

	if err := downloadVerifiedExecutable(ctx, newUpdaterHTTPClient(5*time.Minute), info, newPath, emit); err != nil {
		emit(UpdateProgress{Phase: "error", Message: err.Error()})
		return err
	}

	emit(UpdateProgress{Phase: "applying", Percent: 100, Message: "正在替换程序文件…"})
	if _, err := swapExecutableAtomically(exePath, newPath); err != nil {
		_ = os.Remove(newPath)
		emit(UpdateProgress{Phase: "error", Message: err.Error()})
		return err
	}

	emit(UpdateProgress{Phase: "restarting", Percent: 100, Message: fmt.Sprintf("已更新至 v%s，正在重启…", info.LatestVersion)})

	// Launch updated executable detached, then gracefully close current process.
	cmd := exec.Command(exePath, "--updated-cleanup")
	cmd.Dir = filepath.Dir(exePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("新版本已就绪但自动重启失败，请手动重新打开 Shell：%w", err)
	}

	go func() {
		time.Sleep(350 * time.Millisecond)
		a.QuitApp()
	}()
	return nil
}
