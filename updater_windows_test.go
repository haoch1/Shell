package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/remote"
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
	gotNew, err := os.ReadFile(curExe)
	if err != nil || len(gotNew) != len(fakeExe) || gotNew[0] != 'M' || gotNew[1] != 'Z' {
		t.Fatalf("swapped executable invalid: len=%d err=%v", len(gotNew), err)
	}
	gotOld, err := os.ReadFile(oldPath)
	if err != nil || string(gotOld) != "old-binary" {
		t.Fatalf("backup old executable mismatch: %q err=%v", string(gotOld), err)
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
