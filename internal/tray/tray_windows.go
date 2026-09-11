//go:build windows

package tray

import (
	"syscall"
	"unsafe"
)

// Minimal Win32 tray backend using only stdlib syscalls (no CGO).
// Clicking the icon opens the web UI. Compile-checked only outside
// Windows; runtime behavior needs a real Windows desktop to verify.

const (
	wmUser       = 0x0400
	wmDestroy    = 0x0002
	wmQuit       = 0x0012
	wmLButtonUp  = 0x0202
	wmRButtonUp  = 0x0205
	wmTrayIcon   = wmUser + 1
	nimAdd       = 0x0
	nimDelete    = 0x2
	nifMessage   = 0x1
	nifIcon      = 0x2
	nifTip       = 0x4
	idiApp       = 32512
	idcArrow     = 32512
	hwndMessage  = -3
	wsOverlapped = 0x00000000
	classStyle   = 0
)

// messageOnlyWindow is HWND_MESSAGE ((HWND)-3): a window with no visible
// representation, sufficient for receiving tray callbacks.
var messageOnlyWindow = syscall.Handle(^uintptr(0) - 2)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pRegisterClassExW = user32.NewProc("RegisterClassExW")
	pCreateWindowExW  = user32.NewProc("CreateWindowExW")
	pDefWindowProcW   = user32.NewProc("DefWindowProcW")
	pGetMessageW      = user32.NewProc("GetMessageW")
	pTranslateMessage = user32.NewProc("TranslateMessage")
	pDispatchMessageW = user32.NewProc("DispatchMessageW")
	pPostMessageW     = user32.NewProc("PostMessageW")
	pLoadIconW        = user32.NewProc("LoadIconW")
	pLoadCursorW      = user32.NewProc("LoadCursorW")
	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	pShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")

	trayHWND  uintptr
	trayURL   string
	trayAdded bool
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

type msg struct {
	hwnd     syscall.Handle
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	ptX      int32
	ptY      int32
	lPrivate uint32
}

type notifyIconData struct {
	cbSize            uint32
	hWnd              syscall.Handle
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             syscall.Handle
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          [16]byte
	hBalloonIcon      syscall.Handle
}

// Available is always true on Windows; Run fails only on API errors.
func Available() bool { return true }

func wndProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTrayIcon:
		if lParam == wmLButtonUp || lParam == wmRButtonUp {
			_ = openBrowser(trayURL)
		}
		return 0
	case wmDestroy:
		ret, _, _ := pPostMessageW.Call(uintptr(hwnd), wmQuit, 0, 0)
		_ = ret
		return 0
	}
	ret, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

// Run creates a message-only window, adds the icon, and pumps messages
// until Quit. Left/right click opens the UI.
func Run(cfg Config) error {
	if stopped() {
		return nil
	}
	trayURL = cfg.UIURL

	className, _ := syscall.UTF16PtrFromString("LanternTray")
	hInstance, _, _ := pGetModuleHandleW.Call(0)
	hIcon, _, _ := pLoadIconW.Call(0, idiApp)
	hCursor, _, _ := pLoadCursorW.Call(0, idcArrow)
	proc := syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
		return wndProc(syscall.Handle(hwnd), uint32(msg), wParam, lParam)
	})
	wcx := wndClassEx{
		lpfnWndProc:   proc,
		hInstance:     syscall.Handle(hInstance),
		hIcon:         syscall.Handle(hIcon),
		hCursor:       syscall.Handle(hCursor),
		lpszClassName: className,
	}
	wcx.cbSize = uint32(unsafe.Sizeof(wcx))
	if ret, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wcx))); ret == 0 {
		return syscall.GetLastError()
	}

	hwnd, _, _ := pCreateWindowExW.Call(
		wsOverlapped, uintptr(unsafe.Pointer(className)),
		0, 0, 0, 0, 0, 0,
		uintptr(messageOnlyWindow), 0, hInstance, 0,
	)
	if hwnd == 0 {
		return syscall.GetLastError()
	}
	trayHWND = hwnd

	var tip [128]uint16
	copy(tip[:], syscall.StringToUTF16(cfg.Title))
	nid := notifyIconData{
		hWnd:             syscall.Handle(hwnd),
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmTrayIcon,
		hIcon:            syscall.Handle(hIcon),
		szTip:            tip,
	}
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	if ret, _, _ := pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); ret == 0 {
		return syscall.GetLastError()
	}
	trayAdded = true
	defer func() {
		nid.uFlags = 0
		_, _, _ = pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		trayAdded = false
	}()

	// Wake the loop when Quit is called from another goroutine.
	go func() {
		<-stopCh
		_, _, _ = pPostMessageW.Call(hwnd, wmQuit, 0, 0)
	}()

	var m msg
	for {
		ret, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			return nil
		}
		_, _, _ = pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
