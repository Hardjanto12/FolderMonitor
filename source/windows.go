//go:build windows

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	winmm    = syscall.NewLazyDLL("winmm.dll")

	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procMessageBeep      = user32.NewProc("MessageBeep")
	procBeep             = kernel32.NewProc("Beep")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procShowWindow       = user32.NewProc("ShowWindow")
	procUpdateWindow     = user32.NewProc("UpdateWindow")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procSetWindowTextW   = user32.NewProc("SetWindowTextW")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procSetTimer         = user32.NewProc("SetTimer")
	procKillTimer        = user32.NewProc("KillTimer")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = shell32.NewProc("ShellExecuteW")
	procGetStockObject   = gdi32.NewProc("GetStockObject")
	procPlaySoundW       = winmm.NewProc("PlaySoundW")
	procCreateMutexW     = kernel32.NewProc("CreateMutexW")
)

const (
	mbOK            = 0x00000000
	mbIconWarning   = 0x00000030
	mbIconInfo      = 0x00000040
	mbSetForeground = 0x00010000
	mbTopMost       = 0x00040000

	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	wsTabStop          = 0x00010000
	wsBorder           = 0x00800000
	wsHScroll          = 0x00100000
	bsPushButton       = 0x00000000
	ssLeft             = 0x00000000
	esReadOnly         = 0x00000800
	esAutoHScroll      = 0x00000080

	cwUseDefault = ^uintptr(0x7fffffff)

	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmSize          = 0x0005
	wmTimer         = 0x0113
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmUser          = 0x0400
	wmSetFont       = 0x0030
	wmTrayIcon      = wmUser + 1

	sizeMinimized = 1

	swHide       = 0
	swShow       = 5
	swRestore    = 9
	swShowNormal = 1

	idiApplication = 32512
	idcArrow       = 32512
	imageIcon      = 1
	lrLoadFromFile = 0x0010
	lrDefaultSize  = 0x0040
	lrShared       = 0x8000
	defaultGuiFont = 17
	sndAsync       = 0x0001
	sndFilename    = 0x00020000
	sndLoop        = 0x0008

	nimAdd     = 0x00000000
	nimModify  = 0x00000001
	nimDelete  = 0x00000002
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	ctrlBtn15      = 101
	ctrlBtn20      = 102
	ctrlBtn30      = 103
	ctrlBtnOpenCfg = 104
	ctrlBtnOpenFld = 105
	ctrlBtnMinTray = 106
	ctrlBtnTest    = 107
	ctrlBtnExit    = 108
	guiTimerID     = 2001
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

//go:embed appicon.ico
var embeddedAppIconICO []byte

//go:embed alarm.wav
var embeddedAlarmWAV []byte

var appIconPathOnce sync.Once
var appIconPathCached string
var alarmSoundPathOnce sync.Once
var alarmSoundPathCached string

// alertMu prevents rapid duplicate alert launches. Real alert UI/beep is run
// in a separate helper process so the main monitor GUI stays responsive even if
// Windows audio or MessageBox blocks.
var alertMu sync.Mutex
var lastAlertSpawn time.Time

type alertPayload struct {
	Config   Config `json:"config"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Mode     string `json:"mode"`
	LockPath string `json:"lockPath"`
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

type guiApp struct {
	store  *ConfigStore
	exeDir string
	logger *log.Logger
	stop   chan struct{}

	hwnd         uintptr
	lblStatus    uintptr
	lblMaxAge    uintptr
	lblFolder    uintptr
	lblLatest    uintptr
	lblLastCheck uintptr
	lblCount     uintptr
	lblHelp      uintptr

	icon      uintptr
	trayAdded bool

	snapMu sync.RWMutex
	snap   MonitorSnapshot
}

var currentGUI *guiApp

func runDesktopApp(store *ConfigStore, exeDir string, logger *log.Logger) error {
	// Windows UI windows must be created and messaged on the same OS thread.
	// Without this, the Go scheduler may move the goroutine to another thread,
	// causing the message loop to read the wrong thread queue and Windows marks
	// the app as Not Responding.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	app := &guiApp{store: store, exeDir: exeDir, logger: logger, stop: make(chan struct{})}
	currentGUI = app

	if err := app.createWindow(); err != nil {
		close(app.stop)
		return err
	}
	app.updateLabels()

	cfg := store.Get()
	if cfg.StartMinimizedToTray && cfg.MinimizeToTray {
		app.hideToTray()
	} else {
		procShowWindow.Call(app.hwnd, swShow)
		procUpdateWindow.Call(app.hwnd)
	}

	// Start monitoring after the window is already created and shown. This avoids
	// slow network/disk scans making startup look frozen.
	go runMonitor(store, exeDir, logger, app.updateSnapshot, app.stop)

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	app.removeTrayIcon()
	close(app.stop)
	return nil
}

func (a *guiApp) createWindow() error {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className := utf16Ptr("FolderMonitorWindowClass")
	windowTitle := utf16Ptr(fmt.Sprintf("FolderMonitor v%s", version))

	hIcon := loadAppIcon(32, 32)
	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, idiApplication)
	}
	hIconSm := loadAppIcon(16, 16)
	if hIconSm == 0 {
		hIconSm = hIcon
	}
	hCursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	a.icon = hIconSm

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   syscall.NewCallback(windowProc),
		HInstance:     hInstance,
		HIcon:         hIcon,
		HCursor:       hCursor,
		HbrBackground: 6, // COLOR_WINDOW + 1
		LpszClassName: className,
		HIconSm:       hIconSm,
	}

	r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if r == 0 && err != syscall.Errno(1410) { // class already exists
		return fmt.Errorf("RegisterClassExW gagal: %v", err)
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowTitle)),
		wsOverlappedWindow,
		100, 100, 670, 485,
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW gagal: %v", err)
	}
	a.hwnd = hwnd

	// Labels and read-only fields.
	// Long paths are displayed in read-only EDIT controls so the text is not clipped
	// and the user can click, select, copy, or scroll horizontally.
	a.lblStatus = a.createStatic("Status: starting...", 20, 20, 610, 22)
	a.lblMaxAge = a.createStatic("MaxAgeMinutes: -", 20, 48, 610, 22)
	a.createStatic("Folder:", 20, 80, 120, 18)
	a.lblFolder = a.createReadOnlyEdit("-", 20, 100, 610, 30)
	a.createStatic("File terakhir:", 20, 138, 120, 18)
	a.lblLatest = a.createReadOnlyEdit("-", 20, 158, 610, 30)
	a.lblLastCheck = a.createStatic("Last check: -", 20, 198, 610, 22)
	a.lblCount = a.createStatic("File valid: -", 20, 226, 610, 22)
	a.lblHelp = a.createStatic("Quick switch MaxAgeMinutes:", 20, 262, 240, 22)

	// Buttons.
	a.createButton("15 menit", 20, 292, 90, 32, ctrlBtn15)
	a.createButton("20 menit", 120, 292, 90, 32, ctrlBtn20)
	a.createButton("30 menit", 220, 292, 90, 32, ctrlBtn30)
	a.createButton("Buka config", 325, 292, 110, 32, ctrlBtnOpenCfg)
	a.createButton("Buka folder", 445, 292, 110, 32, ctrlBtnOpenFld)
	a.createButton("Minimize to tray", 20, 338, 150, 32, ctrlBtnMinTray)
	a.createButton("Test alert", 185, 338, 110, 32, ctrlBtnTest)
	a.createButton("Exit", 310, 338, 80, 32, ctrlBtnExit)

	procSetTimer.Call(hwnd, guiTimerID, 1000, 0)
	return nil
}

func (a *guiApp) createStatic(text string, x, y, w, h int) uintptr {
	return createControl("STATIC", text, wsChild|wsVisible|ssLeft, x, y, w, h, 0, a.hwnd)
}

func (a *guiApp) createReadOnlyEdit(text string, x, y, w, h int) uintptr {
	return createControl("EDIT", text, wsChild|wsVisible|wsBorder|wsHScroll|esReadOnly|esAutoHScroll, x, y, w, h, 0, a.hwnd)
}

func (a *guiApp) createButton(text string, x, y, w, h int, id int) uintptr {
	return createControl("BUTTON", text, wsChild|wsVisible|wsTabStop|bsPushButton, x, y, w, h, id, a.hwnd)
}

func createControl(className, text string, style uintptr, x, y, w, h int, id int, parent uintptr) uintptr {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(utf16Ptr(className))),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		style,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, uintptr(id), hInstance, 0,
	)
	if hwnd != 0 {
		applyDefaultGUIFont(hwnd)
	}
	return hwnd
}

func windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	app := currentGUI
	switch message {
	case wmCommand:
		if app != nil {
			id := int(wParam & 0xffff)
			app.handleCommand(id)
			return 0
		}
	case wmTimer:
		if app != nil && wParam == guiTimerID {
			app.updateLabels()
			return 0
		}
	case wmSize:
		if app != nil && wParam == sizeMinimized && app.store.Get().MinimizeToTray {
			app.hideToTray()
			return 0
		}
	case wmTrayIcon:
		if app != nil {
			if lParam == wmLButtonDblClk || lParam == wmRButtonUp {
				app.showFromTray()
				return 0
			}
		}
	case wmClose:
		if app != nil && app.store.Get().MinimizeToTray {
			app.hideToTray()
			return 0
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		if app != nil {
			procKillTimer.Call(hwnd, guiTimerID)
			app.removeTrayIcon()
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

func (a *guiApp) handleCommand(id int) {
	switch id {
	case ctrlBtn15:
		a.setMaxAge(15)
	case ctrlBtn20:
		a.setMaxAge(20)
	case ctrlBtn30:
		a.setMaxAge(30)
	case ctrlBtnOpenCfg:
		a.openPath(a.store.path)
	case ctrlBtnOpenFld:
		a.openPath(resolvePath(a.exeDir, a.store.Get().FolderPath))
	case ctrlBtnMinTray:
		a.hideToTray()
	case ctrlBtnTest:
		cfg := a.store.Get()
		msg := "Ini test alert FolderMonitor. Jika popup/beep muncul, alert mode bekerja."
		sendRemoteAlertAsync(cfg, a.logger, "TEST", msg, resolvePath(a.exeDir, cfg.FolderPath), "", time.Time{})
		go alertUser(cfg, cfg.AlertTitle, msg, cfg.AlertMode)
	case ctrlBtnExit:
		procDestroyWindow.Call(a.hwnd)
	}
}

func (a *guiApp) setMaxAge(minutes int) {
	if err := a.store.UpdateMaxAge(minutes); err != nil {
		notifyInfo("FolderMonitor", "Gagal menyimpan MaxAgeMinutes: "+err.Error())
		return
	}
	a.logger.Printf("MaxAgeMinutes switched to %d from GUI", minutes)
	a.updateLabels()
	notifyInfo("FolderMonitor", fmt.Sprintf("MaxAgeMinutes diubah ke %d menit.", minutes))
}

func (a *guiApp) updateSnapshot(s MonitorSnapshot) {
	a.snapMu.Lock()
	a.snap = s
	a.snapMu.Unlock()
}

func (a *guiApp) snapshot() MonitorSnapshot {
	a.snapMu.RLock()
	defer a.snapMu.RUnlock()
	return a.snap
}

func (a *guiApp) updateLabels() {
	if a == nil || a.hwnd == 0 {
		return
	}
	cfg := a.store.Get()
	s := a.snapshot()
	folderPath := resolvePath(a.exeDir, cfg.FolderPath)
	status := s.StatusText
	if status == "" {
		status = "starting..."
	}
	setWindowText(a.lblStatus, "Status: "+status)
	setWindowText(a.lblMaxAge, fmt.Sprintf("MaxAgeMinutes aktif: %d menit", cfg.MaxAgeMinutes))
	setWindowText(a.lblFolder, folderPath)

	latest := "-"
	if s.LatestPath != "" {
		latest = fmt.Sprintf("%s | umur: %.1f menit", s.LatestPath, s.LatestAge.Minutes())
	}
	setWindowText(a.lblLatest, latest)

	lastCheck := "Last check: -"
	if !s.LastCheck.IsZero() {
		lastCheck = "Last check: " + s.LastCheck.Format("2006-01-02 15:04:05")
	}
	if s.LastError != "" {
		lastCheck += " | Error: " + s.LastError
	}
	setWindowText(a.lblLastCheck, lastCheck)
	setWindowText(a.lblCount, fmt.Sprintf("File valid: %d", s.FileCount))
}

func (a *guiApp) hideToTray() {
	if a.hwnd == 0 {
		return
	}
	a.addTrayIcon()
	procShowWindow.Call(a.hwnd, swHide)
}

func (a *guiApp) showFromTray() {
	procShowWindow.Call(a.hwnd, swShow)
	procShowWindow.Call(a.hwnd, swRestore)
	procUpdateWindow.Call(a.hwnd)
}

func (a *guiApp) addTrayIcon() {
	if a.trayAdded || a.hwnd == 0 {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = a.hwnd
	nid.UID = 1
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmTrayIcon
	nid.HIcon = a.icon
	copyUTF16(nid.SzTip[:], "FolderMonitor - double click untuk buka")
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	a.trayAdded = true
}

func (a *guiApp) removeTrayIcon() {
	if !a.trayAdded || a.hwnd == 0 {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = a.hwnd
	nid.UID = 1
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	a.trayAdded = false
}

func (a *guiApp) openPath(path string) {
	if path == "" {
		return
	}
	verb := utf16Ptr("open")
	p := utf16Ptr(path)
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(p)), 0, 0, swShowNormal)
}

func setWindowText(hwnd uintptr, text string) {
	if hwnd == 0 {
		return
	}
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr(text))))
}

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func ensureEmbeddedAppIconFile() string {
	appIconPathOnce.Do(func() {
		if len(embeddedAppIconICO) == 0 {
			return
		}
		baseDir, err := os.UserCacheDir()
		if err != nil || baseDir == "" {
			baseDir = os.TempDir()
		}
		cacheDir := filepath.Join(baseDir, "FolderMonitor")
		_ = os.MkdirAll(cacheDir, 0o755)
		iconPath := filepath.Join(cacheDir, "appicon.ico")
		writeFile := true
		if fi, err := os.Stat(iconPath); err == nil && fi.Size() == int64(len(embeddedAppIconICO)) {
			writeFile = false
		}
		if writeFile {
			_ = os.WriteFile(iconPath, embeddedAppIconICO, 0o644)
		}
		appIconPathCached = iconPath
	})
	return appIconPathCached
}

func ensureEmbeddedAlarmSoundFile() string {
	alarmSoundPathOnce.Do(func() {
		if len(embeddedAlarmWAV) == 0 {
			return
		}
		baseDir, err := os.UserCacheDir()
		if err != nil || baseDir == "" {
			baseDir = os.TempDir()
		}
		cacheDir := filepath.Join(baseDir, "FolderMonitor")
		_ = os.MkdirAll(cacheDir, 0o755)
		soundPath := filepath.Join(cacheDir, "alarm.wav")
		writeFile := true
		if fi, err := os.Stat(soundPath); err == nil && fi.Size() == int64(len(embeddedAlarmWAV)) {
			writeFile = false
		}
		if writeFile {
			_ = os.WriteFile(soundPath, embeddedAlarmWAV, 0o644)
		}
		alarmSoundPathCached = soundPath
	})
	return alarmSoundPathCached
}

func applyDefaultGUIFont(hwnd uintptr) {
	hFont, _, _ := procGetStockObject.Call(defaultGuiFont)
	if hFont != 0 {
		procSendMessageW.Call(hwnd, wmSetFont, hFont, 1)
	}
}

func loadAppIcon(width, height int) uintptr {
	paths := make([]string, 0, 2)
	if p := ensureEmbeddedAppIconFile(); p != "" {
		paths = append(paths, p)
	}
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "appicon.ico"))
	}
	for _, iconPath := range paths {
		if _, err := os.Stat(iconPath); err != nil {
			continue
		}
		h, _, _ := procLoadImageW.Call(
			0,
			uintptr(unsafe.Pointer(utf16Ptr(iconPath))),
			imageIcon,
			uintptr(width),
			uintptr(height),
			lrLoadFromFile|lrDefaultSize,
		)
		if h != 0 {
			return h
		}
	}
	return 0
}

func copyUTF16(dst []uint16, s string) {
	u := syscall.StringToUTF16(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

func notifyInfo(title, message string) {
	t := utf16Ptr(title)
	m := utf16Ptr(message)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), uintptr(mbOK|mbIconInfo|mbSetForeground|mbTopMost))
}

func runAlertChild(payloadPath string) error {
	b, err := os.ReadFile(payloadPath)
	if err != nil {
		return err
	}
	var p alertPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	defer os.Remove(payloadPath)
	if p.LockPath != "" {
		defer os.Remove(p.LockPath)
	}
	if p.Title == "" {
		p.Title = "Folder Monitor Alert"
	}
	alertUserInProcess(p.Config, p.Title, p.Message, p.Mode)
	return nil
}

func alertUser(cfg Config, title string, message string, mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "popup_beep"
	}

	hasBeep := strings.Contains(mode, "beep")
	hasPopup := strings.Contains(mode, "popup")
	if mode == "console" || (!hasPopup && !hasBeep) {
		fmt.Println(message)
		return
	}

	// Avoid rapid duplicate clicks/alerts in the same process.
	alertMu.Lock()
	if time.Since(lastAlertSpawn) < 3*time.Second {
		alertMu.Unlock()
		return
	}
	lastAlertSpawn = time.Now()
	alertMu.Unlock()

	lockPath, lockOK := acquireAlertLock()
	if !lockOK {
		return
	}

	payloadPath, err := writeAlertPayload(alertPayload{
		Config:   cfg,
		Title:    title,
		Message:  message,
		Mode:     mode,
		LockPath: lockPath,
	})
	if err != nil {
		_ = os.Remove(lockPath)
		go alertUserInProcess(cfg, title, message, mode)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		_ = os.Remove(lockPath)
		_ = os.Remove(payloadPath)
		go alertUserInProcess(cfg, title, message, mode)
		return
	}

	cmd := exec.Command(exe, "--alert-child", payloadPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(lockPath)
		_ = os.Remove(payloadPath)
		go alertUserInProcess(cfg, title, message, mode)
		return
	}
}

func alertUserInProcess(cfg Config, title string, message string, mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "popup_beep"
	}

	hasBeep := strings.Contains(mode, "beep")
	hasPopup := strings.Contains(mode, "popup")

	if hasPopup {
		var stopBeep chan struct{}
		if hasBeep {
			stopBeep = make(chan struct{})
			go runConfiguredBeep(cfg, stopBeep, true)
		}

		t := utf16Ptr(title)
		m := utf16Ptr(message)
		procMessageBoxW.Call(
			0,
			uintptr(unsafe.Pointer(m)),
			uintptr(unsafe.Pointer(t)),
			uintptr(mbOK|mbIconWarning|mbSetForeground|mbTopMost),
		)

		if stopBeep != nil {
			close(stopBeep)
		}
	}

	if hasBeep && !hasPopup {
		runConfiguredBeep(cfg, nil, false)
	}

	if mode == "console" || (!hasPopup && !hasBeep) {
		fmt.Println(message)
	}
}

func alertCacheDir() string {
	baseDir, err := os.UserCacheDir()
	if err != nil || baseDir == "" {
		baseDir = os.TempDir()
	}
	dir := filepath.Join(baseDir, "FolderMonitor")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func acquireAlertLock() (string, bool) {
	lockPath := filepath.Join(alertCacheDir(), "alert.lock")
	if fi, err := os.Stat(lockPath); err == nil {
		// Clean stale lock files from crashed/killed alert child processes.
		if time.Since(fi.ModTime()) > 10*time.Minute {
			_ = os.Remove(lockPath)
		} else {
			return "", false
		}
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", false
	}
	_, _ = fmt.Fprintf(f, "pid=%d time=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	_ = f.Close()
	return lockPath, true
}

func writeAlertPayload(p alertPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(alertCacheDir(), "alert-*.json")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func runConfiguredBeep(cfg Config, stop <-chan struct{}, hasConfirmationPopup bool) {
	beepMode := normalizeBeepMode(cfg.AlertBeepMode)

	if beepMode == "short" {
		playShortBeeps(cfg)
		return
	}

	if beepMode == "until_confirmed" && hasConfirmationPopup && stop != nil {
		beepLoop(cfg, stop, 0)
		return
	}

	duration := time.Duration(cfg.AlertBeepDurationSeconds) * time.Second
	if duration <= 0 {
		duration = 30 * time.Second
	}
	beepLoop(cfg, stop, duration)
}

func playShortBeeps(cfg Config) {
	if playAlarmSound(false) {
		time.Sleep(2 * time.Second)
		stopAlarmSound()
		return
	}

	_, tone, interval, _ := beepSettings(cfg)
	for i := 0; i < 3; i++ {
		procMessageBeep.Call(0xFFFFFFFF)
		time.Sleep(tone + interval)
	}
}

func beepLoop(cfg Config, stop <-chan struct{}, duration time.Duration) {
	if playAlarmSound(true) {
		waitForAlertStop(stop, duration)
		stopAlarmSound()
		return
	}

	// Fallback only: if the WAV alarm cannot be loaded, use safe periodic MessageBeep.
	_, tone, interval, _ := beepSettings(cfg)
	deadline := time.Time{}
	if duration > 0 {
		deadline = time.Now().Add(duration)
	}
	for {
		if stop != nil {
			select {
			case <-stop:
				return
			default:
			}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return
		}
		procMessageBeep.Call(0xFFFFFFFF)
		time.Sleep(tone)
		if interval > 0 {
			if stop != nil {
				select {
				case <-stop:
					return
				case <-time.After(interval):
				}
			} else {
				time.Sleep(interval)
			}
		}
	}
}

func waitForAlertStop(stop <-chan struct{}, duration time.Duration) {
	if duration <= 0 {
		if stop == nil {
			return
		}
		<-stop
		return
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	if stop == nil {
		<-timer.C
		return
	}
	select {
	case <-stop:
	case <-timer.C:
	}
}

func playAlarmSound(loop bool) bool {
	path := ensureEmbeddedAlarmSoundFile()
	if path == "" {
		return false
	}
	flags := uintptr(sndFilename | sndAsync)
	if loop {
		flags |= sndLoop
	}
	r, _, _ := procPlaySoundW.Call(uintptr(unsafe.Pointer(utf16Ptr(path))), 0, flags)
	return r != 0
}

func stopAlarmSound() {
	procPlaySoundW.Call(0, 0, 0)
}

func beepSettings(cfg Config) (frequency uintptr, tone time.Duration, interval time.Duration, duration time.Duration) {
	freq := cfg.AlertBeepFrequencyHz
	if freq < 37 {
		freq = 1200
	}
	if freq > 32767 {
		freq = 32767
	}

	toneMs := cfg.AlertBeepToneMilliseconds
	if toneMs <= 0 {
		toneMs = 200
	}
	if toneMs < 100 {
		toneMs = 100
	}
	// Fallback beep safety limits. Main alert sound now uses alarm.wav via PlaySound in a helper process.
	if toneMs > 500 {
		toneMs = 500
	}

	intervalMs := cfg.AlertBeepIntervalMilliseconds
	if intervalMs < 0 {
		intervalMs = 500
	}
	// Avoid too-aggressive fallback loops.
	if intervalMs < 800 {
		intervalMs = 800
	}
	if intervalMs > 5000 {
		intervalMs = 5000
	}

	durationSec := cfg.AlertBeepDurationSeconds
	if durationSec <= 0 {
		durationSec = 30
	}

	return uintptr(freq), time.Duration(toneMs) * time.Millisecond, time.Duration(intervalMs) * time.Millisecond, time.Duration(durationSec) * time.Second
}

func ensureStartupTaskAsync(cfg Config, logger *log.Logger) {
	mode := normalizeStartupMode(cfg.StartupMode)
	exists, err := startupTaskExists(cfg.TaskName)
	if err != nil {
		logger.Println("Startup task check failed:", err)
		return
	}
	if exists {
		return
	}
	if err := installStartupTask(cfg.TaskName, mode); err != nil {
		logger.Println("Auto install startup failed:", err)
		return
	}
	logger.Println("Startup task auto-installed. Mode=", mode)
}

func runCommandCombinedOutput(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("command timeout after %s", timeout)
	}
	return out, err
}

func installStartupTask(taskName string, mode string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}

	taskRun := fmt.Sprintf("\"%s\" --run", exe)
	mode = normalizeStartupMode(mode)

	args := []string{"/Create", "/TN", taskName, "/TR", taskRun, "/F"}
	if mode == "boot" {
		args = append(args, "/SC", "ONSTART", "/RL", "HIGHEST")
	} else {
		args = append(args, "/SC", "ONLOGON")
	}

	out, err := runCommandCombinedOutput(8*time.Second, "schtasks", args...)
	if err != nil {
		return fmt.Errorf("schtasks error: %v | output: %s", err, string(out))
	}
	return nil
}

func uninstallStartupTask(taskName string) error {
	out, err := runCommandCombinedOutput(8*time.Second, "schtasks", "/Delete", "/TN", taskName, "/F")
	if err != nil {
		return fmt.Errorf("schtasks delete error: %v | output: %s", err, string(out))
	}
	return nil
}

func startupTaskExists(taskName string) (bool, error) {
	out, err := runCommandCombinedOutput(5*time.Second, "schtasks", "/Query", "/TN", taskName)
	if err != nil {
		text := strings.ToLower(string(out))
		if strings.Contains(text, "cannot find") || strings.Contains(text, "tidak dapat menemukan") || strings.Contains(text, "error:") {
			return false, nil
		}
		return false, nil
	}
	return true, nil
}

var mutexHandle uintptr

const errorAlreadyExists syscall.Errno = 183

func enforceSingleInstance() {
	mutexName := "Local\\FolderMonitorMutex_1.3.6"
	ret, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(utf16Ptr(mutexName))))
	if ret == 0 {
		return
	}
	errno, ok := err.(syscall.Errno)
	if ok && errno == errorAlreadyExists {
		notifyInfo("FolderMonitor", "FolderMonitor sudah berjalan.")
		os.Exit(0)
	}
	mutexHandle = ret
}
