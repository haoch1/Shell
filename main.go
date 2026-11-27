package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/haoch1/Shell/internal/agentbridge"
	"github.com/haoch1/Shell/internal/local"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var embedded embed.FS

func hasFlag(flag string) bool {
	for _, arg := range os.Args[1:] {
		if arg == flag {
			return true
		}
	}
	return false
}

func runStdioMCPProxy() {
	err := agentbridge.ProxyStdioToHTTP(os.Stdin, os.Stdout, func() (string, string, error) {
		info, err := local.ReadBridgeRuntime()
		if err != nil || info.Port < 1024 {
			return "", "", fmt.Errorf("未检测到运行中的 Shell MCP 桥接服务")
		}
		token, err := local.EnsureAgentToken()
		if err != nil {
			return "", "", err
		}
		return fmt.Sprintf("http://127.0.0.1:%d/mcp", info.Port), token, nil
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

	assets, err := fs.Sub(embedded, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	app := newApp()
	err = wails.Run(&options.App{
		Title:            "Shell",
		Width:            1280,
		Height:           820,
		MinWidth:         880,
		MinHeight:        560,
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
			ResizeDebounceMS:     16,
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
