package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/haoch1/Shell/frontend"
	"github.com/haoch1/Shell/internal/agentbridge"
	"github.com/haoch1/Shell/internal/local"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var embedded embed.FS

// A custom build can override this value with -ldflags '-X main.appVersion=...'.
var appVersion string

func init() {
	if appVersion == "" {
		appVersion = frontend.Version()
	}
}

func hasFlag(flag string) bool {
	for _, arg := range os.Args[1:] {
		if arg == flag {
			return true
		}
	}
	return false
}

func runStdioMCPProxy() {
	err := agentbridge.ProxyStdioToHTTP(os.Stdin, os.Stdout, func() (agentbridge.Endpoint, error) {
		info, token, err := local.ReadBridgeCredentials()
		if err != nil {
			return agentbridge.Endpoint{}, err
		}
		return agentbridge.Endpoint{
			URL:   fmt.Sprintf("http://127.0.0.1:%d/mcp", info.Port),
			Token: token, PipeID: info.PipeID, PID: info.PID,
		}, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func main() {
	if hasFlag("--mcp") {
		runStdioMCPProxy()
		return
	}
	if hasFlag("--version") {
		fmt.Println("Shell", appVersion)
		return
	}
	cleanupPreviousExecutable()

	assets, err := fs.Sub(embedded, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	st, err := local.LoadSettings()
	if err != nil {
		st = local.DefaultSettings()
	}
	startState := options.Normal
	if st.WindowMaximised {
		startState = options.Maximised
	}

	wvDataDir := ""
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		wvDataDir = filepath.Join(cacheDir, "Shell", "WebView2")
	}

	app := newApp()
	err = wails.Run(&options.App{
		Title:            "Shell",
		Width:            st.WindowWidth,
		Height:           st.WindowHeight,
		MinWidth:         880,
		MinHeight:        560,
		WindowStartState: startState,
		Frameless:        true,
		BackgroundColour: &options.RGBA{R: 255, G: 255, B: 255, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: false,
			CSSDropProperty:    "--wails-drop-target",
			CSSDropValue:       "drop",
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			ResizeDebounceMS:     4,
			WebviewUserDataPath:  wvDataDir,
		},
		OnStartup:     app.startup,
		OnBeforeClose: app.beforeClose,
		OnShutdown:    app.shutdown,
		Bind:          []interface{}{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
