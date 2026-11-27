package main

import (
	"fmt"
	goruntime "runtime"
	"sort"
	"sync"
	"syscall"
	"unsafe"

	"github.com/haoch1/Shell/internal/local"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW       = modUser32.NewProc("RegisterClassExW")
	procUnregisterClassW       = modUser32.NewProc("UnregisterClassW")
	procCreateWindowExW        = modUser32.NewProc("CreateWindowExW")
	procDestroyWindow          = modUser32.NewProc("DestroyWindow")
	procDefWindowProcW         = modUser32.NewProc("DefWindowProcW")
	procGetMessageW            = modUser32.NewProc("GetMessageW")
	procTranslateMessage       = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW       = modUser32.NewProc("DispatchMessageW")
	procPostMessageW           = modUser32.NewProc("PostMessageW")
	procPostQuitMessage        = modUser32.NewProc("PostQuitMessage")
	procLoadImageW             = modUser32.NewProc("LoadImageW")
	procLoadIconW              = modUser32.NewProc("LoadIconW")
	procDestroyIcon            = modUser32.NewProc("DestroyIcon")
	procGetSystemMetrics       = modUser32.NewProc("GetSystemMetrics")
	procCreatePopupMenu        = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW            = modUser32.NewProc("AppendMenuW")
	procSetMenuDefaultItem     = modUser32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenuEx       = modUser32.NewProc("TrackPopupMenuEx")
	procDestroyMenu            = modUser32.NewProc("DestroyMenu")
	procGetCursorPos           = modUser32.NewProc("GetCursorPos")
	procSetForegroundWindow    = modUser32.NewProc("SetForegroundWindow")
	procFindWindowW            = modUser32.NewProc("FindWindowW")
	procRegisterWindowMessageW = modUser32.NewProc("RegisterWindowMessageW")
	procShellNotifyIconW       = modShell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW       = modKernel32.NewProc("GetModuleHandleW")
)

const (
	wmNull           = 0x0000
	wmDestroy        = 0x0002
	wmClose          = 0x0010
	wmContextMenu    = 0x007B
	wmLButtonUp      = 0x0202
	wmLButtonDblClk  = 0x0203
	wmRButtonUp      = 0x0205
	wmApp            = 0x8000
	wmTrayIcon       = wmApp + 1

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	imageIcon       = 1
	lrDefaultColor  = 0x00000000
	smCxSmIcon      = 49
	smCySmIcon      = 50
	idiApplication  = 32512

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfGrayed    = 0x00000001
	mfDisabled  = 0x00000002
	mfChecked   = 0x00000008
	mfPopup     = 0x00000010

	tpmLeftAlign   = 0x0000
	tpmBottomAlign = 0x0020
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	trayCmdShow          = 1001
	trayCmdNewConn       = 1002
	trayCmdDisconnectAll = 1003
	trayCmdToggleMCP     = 1004
	trayCmdOpenMCP       = 1005
	trayCmdSettings      = 1006
	trayCmdToggleClose   = 1007
	trayCmdQuit          = 1099
	trayCmdHostBase      = 2000
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type winPoint struct {
	X int32
	Y int32
}

type winMsg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
}

// notifyIconDataW matches the exact Win64 NOTIFYICONDATAW (V4) layout (976 bytes on amd64).
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

type systemTray struct {
	app           *App
	hwnd          uintptr
	hInst         uintptr
	hIcon         uintptr
	ownsIcon      bool
	taskbarMsg    uint32
	classNamePtr  *uint16
	mu            sync.Mutex
	stopOnce      sync.Once
	done          chan struct{}
	quickHostIDs  []string
}

var (
	activeTrayMu sync.RWMutex
	activeTray   *systemTray
)

func startSystemTray(app *App) *systemTray {
	t := &systemTray{
		app:  app,
		done: make(chan struct{}),
	}
	activeTrayMu.Lock()
	activeTray = t
	activeTrayMu.Unlock()

	go t.runLoop()
	return t
}

func (t *systemTray) Stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		t.mu.Lock()
		hwnd := t.hwnd
		t.mu.Unlock()
		if hwnd != 0 {
			_, _, _ = procPostMessageW.Call(hwnd, wmClose, 0, 0)
		}
		<-t.done
		activeTrayMu.Lock()
		if activeTray == t {
			activeTray = nil
		}
		activeTrayMu.Unlock()
	})
}

func (t *systemTray) runLoop() {
	// Win32 message loops require thread affinity.
	defer close(t.done)
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()

	hInst, _, _ := procGetModuleHandleW.Call(0)
	t.hInst = hInst

	tbName, _ := windows.UTF16PtrFromString("TaskbarCreated")
	msgID, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tbName)))
	t.taskbarMsg = uint32(msgID)

	className, _ := windows.UTF16PtrFromString("ShellNativeTrayWindow")
	t.classNamePtr = className

	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   syscall.NewCallback(trayWndProc),
		hInstance:     hInst,
		lpszClassName: className,
	}
	atom, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		return
	}
	defer procUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), hInst)

	winTitle, _ := windows.UTF16PtrFromString("ShellTray")
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(winTitle)),
		0, 0, 0, 0, 0,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return
	}
	t.mu.Lock()
	t.hwnd = hwnd
	t.mu.Unlock()

	t.hIcon, t.ownsIcon = loadTrayIcon(hInst)
	defer func() {
		if t.ownsIcon && t.hIcon != 0 {
			_, _, _ = procDestroyIcon.Call(t.hIcon)
		}
	}()

	t.addIcon("Shell")
	defer t.deleteIcon()

	var m winMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func loadTrayIcon(hInst uintptr) (uintptr, bool) {
	cx, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCySmIcon)
	if cx == 0 {
		cx = 16
	}
	if cy == 0 {
		cy = 16
	}
	// Resource ID 1 is embedded by cmd/genrsrc (Shell.ico).
	h, _, _ := procLoadImageW.Call(hInst, 1, imageIcon, cx, cy, lrDefaultColor)
	if h != 0 {
		return h, true
	}
	fallback, _, _ := procLoadIconW.Call(0, idiApplication)
	return fallback, false
}

func (t *systemTray) addIcon(tip string) {
	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = t.hwnd
	nid.uID = 1
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = wmTrayIcon
	nid.hIcon = t.hIcon
	copyUTF16(nid.szTip[:], tip)
	_, _, _ = procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
}

func (t *systemTray) deleteIcon() {
	if t.hwnd == 0 {
		return
	}
	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = t.hwnd
	nid.uID = 1
	_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func copyUTF16(dst []uint16, s string) {
	u, err := windows.UTF16FromString(s)
	if err != nil || len(dst) == 0 {
		return
	}
	n := len(u)
	if n > len(dst) {
		n = len(dst)
		u[n-1] = 0
	}
	copy(dst[:n], u[:n])
}

func trayWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	activeTrayMu.RLock()
	t := activeTray
	activeTrayMu.RUnlock()

	if t != nil && t.taskbarMsg != 0 && msg == t.taskbarMsg {
		t.addIcon("Shell")
		return 0
	}

	switch msg {
	case wmTrayIcon:
		ev := uint32(lParam & 0xFFFF)
		switch ev {
		case wmLButtonUp, wmLButtonDblClk:
			if t != nil {
				go t.app.ShowMainWindow()
			}
		case wmRButtonUp, wmContextMenu:
			if t != nil {
				t.showContextMenu()
			}
		}
		return 0
	case wmClose:
		_, _, _ = procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func appendMenuText(hMenu uintptr, flags uintptr, id uintptr, text string) {
	ptr, _ := windows.UTF16PtrFromString(text)
	_, _, _ = procAppendMenuW.Call(hMenu, flags, id, uintptr(unsafe.Pointer(ptr)))
}

func appendMenuSep(hMenu uintptr) {
	_, _, _ = procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
}

func trayHostChoices(profiles []local.Profile, limit int) []local.Profile {
	cp := append([]local.Profile(nil), profiles...)
	sort.SliceStable(cp, func(i, j int) bool {
		if cp[i].Pinned != cp[j].Pinned {
			return cp[i].Pinned
		}
		if cp[i].LastUsedAt != cp[j].LastUsedAt {
			return cp[i].LastUsedAt > cp[j].LastUsedAt
		}
		return cp[i].Name < cp[j].Name
	})
	if limit > 0 && len(cp) > limit {
		cp = cp[:limit]
	}
	return cp
}

func (t *systemTray) showContextMenu() {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	appendMenuText(hMenu, mfString, trayCmdShow, "显示主窗口")
	_, _, _ = procSetMenuDefaultItem.Call(hMenu, trayCmdShow, 0)

	// Quick-connect submenu populated with saved hosts (pinned first, then recent).
	hSub, _, _ := procCreatePopupMenu.Call()
	t.quickHostIDs = nil
	if hSub != 0 {
		profiles, _ := local.Profiles()
		choices := trayHostChoices(profiles, 12)
		if len(choices) == 0 {
			appendMenuText(hSub, mfString|mfGrayed|mfDisabled, 0, "暂无已保存主机")
		} else {
			for i, p := range choices {
				t.quickHostIDs = append(t.quickHostIDs, p.ID)
				label := fmt.Sprintf("%s  (%s)", p.Name, p.Host)
				appendMenuText(hSub, mfString, uintptr(trayCmdHostBase+i), label)
			}
		}
		appendMenuText(hMenu, mfPopup, hSub, "快速连接")
	}

	if liveCount := t.app.ActiveSessionCount(); liveCount > 0 {
		appendMenuSep(hMenu)
		appendMenuText(hMenu, mfString, trayCmdDisconnectAll, fmt.Sprintf("断开全部会话 (%d)", liveCount))
	}

	appendMenuSep(hMenu)
	appendMenuText(hMenu, mfString, trayCmdSettings, "偏好设置")
	appendMenuText(hMenu, mfString, trayCmdQuit, "退出 Shell")

	var pt winPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	_, _, _ = procSetForegroundWindow.Call(t.hwnd)

	cmd, _, _ := procTrackPopupMenuEx.Call(
		hMenu,
		tpmLeftAlign|tpmBottomAlign|tpmRightButton|tpmReturnCmd,
		uintptr(pt.X),
		uintptr(pt.Y),
		t.hwnd,
		0,
	)
	_, _, _ = procPostMessageW.Call(t.hwnd, wmNull, 0, 0)

	if cmd != 0 {
		go t.handleCommand(int(cmd))
	}
}

func (t *systemTray) handleCommand(cmd int) {
	switch {
	case cmd == trayCmdShow:
		t.app.ShowMainWindow()
	case cmd == trayCmdNewConn:
		t.app.ShowMainWindow()
		runtime.EventsEmit(t.app.ctx, "shell:tray_action", "new_host")
	case cmd == trayCmdDisconnectAll:
		t.app.DisconnectAllSessions()
	case cmd == trayCmdToggleMCP:
		t.app.ToggleMCPFromTray()
	case cmd == trayCmdOpenMCP:
		t.app.ShowMainWindow()
		runtime.EventsEmit(t.app.ctx, "shell:tray_action", "open_agent_settings")
	case cmd == trayCmdSettings:
		t.app.ShowMainWindow()
		runtime.EventsEmit(t.app.ctx, "shell:tray_action", "open_settings")
	case cmd == trayCmdToggleClose:
		t.app.ToggleCloseToTray()
	case cmd == trayCmdQuit:
		t.app.QuitApp()
	case cmd >= trayCmdHostBase && cmd < trayCmdHostBase+len(t.quickHostIDs):
		hostID := t.quickHostIDs[cmd-trayCmdHostBase]
		t.app.ShowMainWindow()
		runtime.EventsEmit(t.app.ctx, "agent:connect_profile", hostID)
	}
}

func focusShellWindow() {
	title, _ := windows.UTF16PtrFromString("Shell")
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd != 0 {
		_, _, _ = procSetForegroundWindow.Call(hwnd)
	}
}
