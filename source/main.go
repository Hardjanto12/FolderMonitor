package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const version = "1.3.6"

// Config is stored in config.json next to the executable.
type Config struct {
	FolderPath                    string   `json:"folderPath"`
	MaxAgeMinutes                 int      `json:"maxAgeMinutes"`
	CheckIntervalSeconds          int      `json:"checkIntervalSeconds"`
	RepeatAlertMinutes            int      `json:"repeatAlertMinutes"`
	IncludeSubfolders             bool     `json:"includeSubfolders"`
	ScanMode                      string   `json:"scanMode"`
	DateFolderPattern             string   `json:"dateFolderPattern"`
	ScanLookbackDays              int      `json:"scanLookbackDays"`
	AllowedExtensions             []string `json:"allowedExtensions"`
	IgnoreExtensions              []string `json:"ignoreExtensions"`
	IgnorePrefixes                []string `json:"ignorePrefixes"`
	IgnoreFileNames               []string `json:"ignoreFileNames"`
	AlertMode                     string   `json:"alertMode"`
	RecoveryAlertMode             string   `json:"recoveryAlertMode"`
	AlertBeepMode                 string   `json:"alertBeepMode"`
	AlertBeepDurationSeconds      int      `json:"alertBeepDurationSeconds"`
	AlertBeepFrequencyHz          int      `json:"alertBeepFrequencyHz"`
	AlertBeepToneMilliseconds     int      `json:"alertBeepToneMilliseconds"`
	AlertBeepIntervalMilliseconds int      `json:"alertBeepIntervalMilliseconds"`
	AlertTitle                    string   `json:"alertTitle"`
	LogPath                       string   `json:"logPath"`
	TaskName                      string   `json:"taskName"`
	AutoInstallStartupOnRun       bool     `json:"autoInstallStartupOnRun"`
	StartupMode                   string   `json:"startupMode"`
	AlertWhenFolderMissing        bool     `json:"alertWhenFolderMissing"`
	ShowRecoveryNotification      bool     `json:"showRecoveryNotification"`
	GUIEnabled                    bool     `json:"guiEnabled"`
	MinimizeToTray                bool     `json:"minimizeToTray"`
	StartMinimizedToTray          bool     `json:"startMinimizedToTray"`
	QuickSwitchMaxAgeMinutes      []int    `json:"quickSwitchMaxAgeMinutes"`
	RemoteAlertEnabled            bool     `json:"remoteAlertEnabled"`
	RemoteAlertURL                string   `json:"remoteAlertUrl"`
	RemoteAlertSecret             string   `json:"remoteAlertSecret"`
	RemoteAlertTimeoutSeconds     int      `json:"remoteAlertTimeoutSeconds"`
	RemoteAlertSourceName         string   `json:"remoteAlertSourceName"`
	RemoteAlertIncludeRecovery    bool     `json:"remoteAlertIncludeRecovery"`
}

type MonitorState string

const (
	StateOK      MonitorState = "OK"
	StateAlert   MonitorState = "ALERT"
	StateUnknown MonitorState = "UNKNOWN"
)

type ScanResult struct {
	LatestPath     string
	LatestTime     time.Time
	FileCount      int
	ScannedFolders []string
}

type MonitorSnapshot struct {
	State         MonitorState
	StatusText    string
	FolderPath    string
	MaxAgeMinutes int
	LatestPath    string
	LatestTime    time.Time
	LatestAge     time.Duration
	FileCount     int
	LastCheck     time.Time
	LastError     string
}

type ConfigStore struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func NewConfigStore(path string, cfg Config) *ConfigStore {
	return &ConfigStore{path: path, cfg: cfg}
}

func (s *ConfigStore) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *ConfigStore) UpdateMaxAge(minutes int) error {
	if minutes <= 0 {
		return fmt.Errorf("maxAgeMinutes harus lebih dari 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.MaxAgeMinutes = minutes
	return writeConfig(s.path, s.cfg)
}

func main() {
	installLogin := flag.Bool("install", false, "Install auto-start task at Windows login")
	installBoot := flag.Bool("install-boot", false, "Install auto-start task at Windows boot. Run as Administrator")
	uninstall := flag.Bool("uninstall", false, "Remove auto-start task")
	testAlert := flag.Bool("test-alert", false, "Show a test alert")
	testRemoteAlert := flag.Bool("test-remote-alert", false, "Send a test HTTP POST remote alert")
	runOnly := flag.Bool("run", false, "Run monitor without auto-install")
	headless := flag.Bool("headless", false, "Run monitor without GUI/tray")
	showVersion := flag.Bool("version", false, "Show version")
	alertChild := flag.String("alert-child", "", "Internal alert payload file")
	flag.Parse()

	exeDir, err := executableDir()
	if err != nil {
		fmt.Println("Gagal membaca lokasi executable:", err)
		notifyInfo("FolderMonitor", "Gagal membaca lokasi executable: "+err.Error())
		os.Exit(1)
	}

	cfgPath := filepath.Join(exeDir, "config.json")
	cfg, created, err := loadOrCreateConfig(cfgPath, exeDir)
	if err != nil {
		fmt.Println("Gagal membaca/membuat config.json:", err)
		notifyInfo("FolderMonitor", "Gagal membaca/membuat config.json: "+err.Error())
		os.Exit(1)
	}

	logger := newLogger(resolvePath(exeDir, cfg.LogPath))
	logger.Printf("FolderMonitor v%s started. Config=%s", version, cfgPath)
	if created {
		logger.Printf("Default config.json created. Please edit folderPath if needed.")
		fmt.Println("config.json default sudah dibuat di:", cfgPath)
		fmt.Println("Edit folderPath di config.json kalau folder yang dimonitor belum sesuai.")
	}

	if *alertChild != "" {
		if err := runAlertChild(*alertChild); err != nil {
			logger.Println("Alert child failed:", err)
		}
		return
	}

	if *showVersion {
		msg := fmt.Sprintf("FolderMonitor v%s", version)
		fmt.Println(msg)
		notifyInfo("FolderMonitor", msg)
		return
	}

	if *testAlert {
		msg := "Ini test alert FolderMonitor. Jika popup/beep muncul, alert mode bekerja."
		sendRemoteAlertAsync(cfg, logger, "TEST", msg, resolvePath(exeDir, cfg.FolderPath), "", time.Time{})
		alertUser(cfg, cfg.AlertTitle, msg, cfg.AlertMode)
		return
	}

	if *testRemoteAlert {
		msg := "Ini test remote alert dari FolderMonitor."
		if err := sendRemoteAlert(cfg, "TEST", msg, resolvePath(exeDir, cfg.FolderPath), "", time.Time{}); err != nil {
			fmt.Println("Gagal mengirim test remote alert:", err)
			logger.Println("Test remote alert failed:", err)
			notifyInfo("FolderMonitor", "Gagal mengirim test remote alert: "+err.Error())
			os.Exit(1)
		}
		fmt.Println("Test remote alert berhasil dikirim ke:", cfg.RemoteAlertURL)
		logger.Println("Test remote alert sent:", cfg.RemoteAlertURL)
		notifyInfo("FolderMonitor", "Test remote alert berhasil dikirim.")
		return
	}

	if *uninstall {
		if err := uninstallStartupTask(cfg.TaskName); err != nil {
			fmt.Println("Gagal uninstall startup task:", err)
			logger.Println("Uninstall startup task failed:", err)
			notifyInfo("FolderMonitor", "Gagal uninstall startup task: "+err.Error())
			os.Exit(1)
		}
		msg := "Startup task berhasil dihapus: " + cfg.TaskName
		fmt.Println(msg)
		logger.Println("Startup task removed:", cfg.TaskName)
		notifyInfo("FolderMonitor", msg)
		return
	}

	if *installLogin || *installBoot {
		mode := "login"
		if *installBoot {
			mode = "boot"
		}
		if err := installStartupTask(cfg.TaskName, mode); err != nil {
			fmt.Println("Gagal install startup task:", err)
			logger.Println("Install startup task failed:", err)
			notifyInfo("FolderMonitor", "Gagal install startup task: "+err.Error())
			os.Exit(1)
		}
		msg := "Startup task berhasil dibuat saat user login: " + cfg.TaskName
		if mode == "boot" {
			msg = "Startup task berhasil dibuat saat Windows boot: " + cfg.TaskName + "\n\nCatatan: mode boot biasanya tidak bisa menampilkan popup sebelum user login. Log tetap berjalan."
		}
		fmt.Println(msg)
		logger.Println("Startup task installed. Mode=", mode)
		notifyInfo("FolderMonitor", msg)
		return
	}

	if !*runOnly && cfg.AutoInstallStartupOnRun {
		// Run startup registration in the background. Older versions called schtasks
		// synchronously before the GUI appeared, which could make startup feel frozen
		// on slow Windows/server environments.
		go ensureStartupTaskAsync(cfg, logger)
	}

	store := NewConfigStore(cfgPath, cfg)
	if !*headless && cfg.GUIEnabled {
		if err := runDesktopApp(store, exeDir, logger); err != nil {
			logger.Println("GUI failed, fallback to headless:", err)
			fmt.Println("GUI gagal, fallback ke mode headless:", err)
		}
		return
	}

	runMonitor(store, exeDir, logger, nil, nil)
}

func loadOrCreateConfig(path string, exeDir string) (Config, bool, error) {
	defaultWatch := filepath.Join(exeDir, "watch-folder")
	cfg := Config{
		FolderPath:                    defaultWatch,
		MaxAgeMinutes:                 30,
		CheckIntervalSeconds:          60,
		RepeatAlertMinutes:            5,
		IncludeSubfolders:             true,
		ScanMode:                      "date_folder",
		DateFolderPattern:             "yyyy\\MMdd",
		ScanLookbackDays:              3,
		AllowedExtensions:             []string{},
		IgnoreExtensions:              []string{".tmp", ".part", ".crdownload", ".lock", ".nul"},
		IgnorePrefixes:                []string{"~$"},
		IgnoreFileNames:               []string{"CREATEDIR.NUL"},
		AlertMode:                     "popup_beep",
		RecoveryAlertMode:             "popup",
		AlertBeepMode:                 "duration",
		AlertBeepDurationSeconds:      30,
		AlertBeepFrequencyHz:          1200,
		AlertBeepToneMilliseconds:     300,
		AlertBeepIntervalMilliseconds: 500,
		AlertTitle:                    "Folder Monitor Alert",
		LogPath:                       "logs/monitor.log",
		TaskName:                      "FolderMonitor",
		AutoInstallStartupOnRun:       true,
		StartupMode:                   "login",
		AlertWhenFolderMissing:        true,
		ShowRecoveryNotification:      true,
		GUIEnabled:                    true,
		MinimizeToTray:                true,
		StartMinimizedToTray:          false,
		QuickSwitchMaxAgeMinutes:      []int{15, 20, 30},
		RemoteAlertEnabled:            false,
		RemoteAlertURL:                "http://192.168.1.50:8765/api/alert",
		RemoteAlertSecret:             "change_this_secret",
		RemoteAlertTimeoutSeconds:     3,
		RemoteAlertSourceName:         "FolderMonitor",
		RemoteAlertIncludeRecovery:    false,
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(defaultWatch, 0755); err != nil {
			return cfg, false, err
		}
		if err := writeConfig(path, cfg); err != nil {
			return cfg, false, err
		}
		return cfg, true, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, false, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, false, err
	}

	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	if _, ok := raw["guiEnabled"]; !ok {
		cfg.GUIEnabled = true
	}
	if _, ok := raw["minimizeToTray"]; !ok {
		cfg.MinimizeToTray = true
	}

	applyConfigDefaults(&cfg)
	return cfg, false, nil
}

func applyConfigDefaults(cfg *Config) {
	if cfg.ScanMode == "" {
		cfg.ScanMode = "date_folder"
	}
	cfg.ScanMode = strings.ToLower(strings.TrimSpace(cfg.ScanMode))
	if cfg.ScanMode != "date_folder" && cfg.ScanMode != "all" {
		cfg.ScanMode = "date_folder"
	}
	if cfg.DateFolderPattern == "" {
		cfg.DateFolderPattern = "yyyy\\MMdd"
	}
	if cfg.ScanLookbackDays <= 0 {
		cfg.ScanLookbackDays = 3
	}
	if cfg.ScanLookbackDays > 14 {
		cfg.ScanLookbackDays = 14
	}
	if cfg.MaxAgeMinutes <= 0 {
		cfg.MaxAgeMinutes = 30
	}
	if cfg.CheckIntervalSeconds <= 0 {
		cfg.CheckIntervalSeconds = 60
	}
	if cfg.RepeatAlertMinutes <= 0 {
		cfg.RepeatAlertMinutes = 5
	}
	if cfg.AlertMode == "" {
		cfg.AlertMode = "popup_beep"
	}
	if cfg.RecoveryAlertMode == "" {
		cfg.RecoveryAlertMode = "popup"
	}
	if cfg.AlertBeepMode == "" {
		cfg.AlertBeepMode = "duration"
	}
	cfg.AlertBeepMode = normalizeBeepMode(cfg.AlertBeepMode)
	if cfg.AlertBeepDurationSeconds <= 0 {
		cfg.AlertBeepDurationSeconds = 30
	}
	if cfg.AlertBeepFrequencyHz <= 0 {
		cfg.AlertBeepFrequencyHz = 1200
	}
	if cfg.AlertBeepToneMilliseconds <= 0 {
		cfg.AlertBeepToneMilliseconds = 200
	}
	if cfg.AlertBeepIntervalMilliseconds < 0 {
		cfg.AlertBeepIntervalMilliseconds = 800
	}
	if cfg.AlertTitle == "" {
		cfg.AlertTitle = "Folder Monitor Alert"
	}
	if cfg.LogPath == "" {
		cfg.LogPath = "logs/monitor.log"
	}
	if cfg.TaskName == "" {
		cfg.TaskName = "FolderMonitor"
	}
	if cfg.StartupMode == "" {
		cfg.StartupMode = "login"
	}
	if cfg.IgnoreExtensions == nil {
		cfg.IgnoreExtensions = []string{".tmp", ".part", ".crdownload", ".lock", ".nul"}
	}
	cfg.IgnoreExtensions = ensureStringInList(cfg.IgnoreExtensions, ".nul")
	if cfg.IgnorePrefixes == nil {
		cfg.IgnorePrefixes = []string{"~$"}
	}
	if cfg.IgnoreFileNames == nil {
		cfg.IgnoreFileNames = []string{"CREATEDIR.NUL"}
	}
	cfg.IgnoreFileNames = ensureStringInList(cfg.IgnoreFileNames, "CREATEDIR.NUL")
	if cfg.QuickSwitchMaxAgeMinutes == nil || len(cfg.QuickSwitchMaxAgeMinutes) == 0 {
		cfg.QuickSwitchMaxAgeMinutes = []int{15, 20, 30}
	}
	if cfg.RemoteAlertTimeoutSeconds <= 0 {
		cfg.RemoteAlertTimeoutSeconds = 3
	}
	if cfg.RemoteAlertTimeoutSeconds > 10 {
		cfg.RemoteAlertTimeoutSeconds = 10
	}
	if cfg.RemoteAlertSourceName == "" {
		cfg.RemoteAlertSourceName = "FolderMonitor"
	}
}

func ensureStringInList(list []string, value string) []string {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			return list
		}
	}
	return append(list, value)
}

func writeConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}

func runMonitor(store *ConfigStore, exeDir string, logger *log.Logger, statusCB func(MonitorSnapshot), stop <-chan struct{}) {
	fmt.Println("FolderMonitor berjalan.")
	fmt.Println("Tekan CTRL+C untuk berhenti jika berjalan di console/headless.")

	state := StateUnknown
	var lastAlertTime time.Time
	emptySince := time.Now()
	lastLatestPath := ""
	lastLatestTime := time.Time{}
	lastFolderPath := ""

	for {
		if shouldStop(stop) {
			logger.Println("Monitor stopped.")
			return
		}

		now := time.Now()
		cfg := store.Get()
		folderPath := resolvePath(exeDir, cfg.FolderPath)
		maxAge := time.Duration(cfg.MaxAgeMinutes) * time.Minute
		checkInterval := time.Duration(cfg.CheckIntervalSeconds) * time.Second
		repeatAlert := time.Duration(cfg.RepeatAlertMinutes) * time.Minute

		if checkInterval < 5*time.Second {
			checkInterval = 5 * time.Second
		}
		if repeatAlert < 30*time.Second {
			repeatAlert = 30 * time.Second
		}

		if folderPath != lastFolderPath {
			logger.Printf("Monitoring folder: %s | MaxAge=%d minutes | CheckInterval=%d seconds | ScanMode=%s | LookbackDays=%d", folderPath, cfg.MaxAgeMinutes, int(checkInterval.Seconds()), cfg.ScanMode, cfg.ScanLookbackDays)
			lastFolderPath = folderPath
			emptySince = now
			state = StateUnknown
			lastLatestPath = ""
			lastLatestTime = time.Time{}
		}

		publishStatus(statusCB, MonitorSnapshot{State: state, StatusText: "Scanning folder...", FolderPath: folderPath, MaxAgeMinutes: cfg.MaxAgeMinutes, LastCheck: now})
		result, err := scanFolder(folderPath, cfg)

		if err != nil {
			logger.Println("Scan error:", err)
			publishStatus(statusCB, MonitorSnapshot{State: state, StatusText: "Folder tidak bisa dibaca", FolderPath: folderPath, MaxAgeMinutes: cfg.MaxAgeMinutes, LastCheck: now, LastError: err.Error()})
			if cfg.AlertWhenFolderMissing && shouldSendAlert(state, lastAlertTime, now, repeatAlert) {
				msg := fmt.Sprintf("ALERT: Folder tidak bisa dibaca.\n\nFolder: %s\nError: %v", folderPath, err)
				logger.Println(strings.ReplaceAll(msg, "\n", " | "))
				sendRemoteAlertAsync(cfg, logger, "ALERT", msg, folderPath, "", time.Time{})
				alertUser(cfg, cfg.AlertTitle, msg, cfg.AlertMode)
				state = StateAlert
				lastAlertTime = now
			}
			if sleepOrStop(checkInterval, stop) {
				return
			}
			continue
		}

		if result.FileCount == 0 {
			age := now.Sub(emptySince)
			statusText := fmt.Sprintf("Folder kosong, grace period %.0f/%.0f menit", age.Minutes(), maxAge.Minutes())
			if age >= maxAge {
				statusText = fmt.Sprintf("ALERT: tidak ada file valid selama %.0f menit", age.Minutes())
				if shouldSendAlert(state, lastAlertTime, now, repeatAlert) {
					msg := fmt.Sprintf("ALERT: Tidak ada file valid di folder selama %.0f menit.\n\nFolder: %s", age.Minutes(), folderPath)
					logger.Println(strings.ReplaceAll(msg, "\n", " | "))
					sendRemoteAlertAsync(cfg, logger, "ALERT", msg, folderPath, "", time.Time{})
					alertUser(cfg, cfg.AlertTitle, msg, cfg.AlertMode)
					state = StateAlert
					lastAlertTime = now
				}
			} else if state != StateOK {
				logger.Printf("Status OK. Folder kosong, grace period berjalan %.0f/%.0f menit", age.Minutes(), maxAge.Minutes())
				state = StateOK
			}
			publishStatus(statusCB, MonitorSnapshot{State: state, StatusText: statusText, FolderPath: folderPath, MaxAgeMinutes: cfg.MaxAgeMinutes, FileCount: 0, LastCheck: now})
			if sleepOrStop(checkInterval, stop) {
				return
			}
			continue
		}

		// Reset empty timer once files exist.
		emptySince = now

		latestAge := now.Sub(result.LatestTime)
		latestChanged := result.LatestPath != lastLatestPath || !result.LatestTime.Equal(lastLatestTime)
		if latestChanged {
			logger.Printf("Latest file: %s | LastWriteTime: %s | Age: %.1f minutes", result.LatestPath, result.LatestTime.Format(time.RFC3339), latestAge.Minutes())
			lastLatestPath = result.LatestPath
			lastLatestTime = result.LatestTime
		}

		statusText := "OK"
		if latestAge >= maxAge {
			statusText = fmt.Sprintf("ALERT: tidak ada file baru/update selama %.0f menit", latestAge.Minutes())
			if shouldSendAlert(state, lastAlertTime, now, repeatAlert) {
				msg := fmt.Sprintf(
					"ALERT: Tidak ada file baru/update selama %.0f menit.\n\nFolder: %s\nFile terakhir: %s\nWaktu file terakhir: %s",
					latestAge.Minutes(),
					folderPath,
					result.LatestPath,
					result.LatestTime.Format("2006-01-02 15:04:05"),
				)
				logger.Println(strings.ReplaceAll(msg, "\n", " | "))
				sendRemoteAlertAsync(cfg, logger, "ALERT", msg, folderPath, result.LatestPath, result.LatestTime)
				alertUser(cfg, cfg.AlertTitle, msg, cfg.AlertMode)
				state = StateAlert
				lastAlertTime = now
			}
		} else {
			if state == StateAlert {
				msg := fmt.Sprintf("RECOVERED: File baru/update terdeteksi.\n\nFile: %s\nWaktu: %s", result.LatestPath, result.LatestTime.Format("2006-01-02 15:04:05"))
				logger.Println(strings.ReplaceAll(msg, "\n", " | "))
				if cfg.RemoteAlertIncludeRecovery {
					sendRemoteAlertAsync(cfg, logger, "RECOVERED", msg, folderPath, result.LatestPath, result.LatestTime)
				}
				if cfg.ShowRecoveryNotification {
					alertUser(cfg, cfg.AlertTitle, msg, cfg.RecoveryAlertMode)
				}
			}
			state = StateOK
		}

		publishStatus(statusCB, MonitorSnapshot{State: state, StatusText: statusText, FolderPath: folderPath, MaxAgeMinutes: cfg.MaxAgeMinutes, LatestPath: result.LatestPath, LatestTime: result.LatestTime, LatestAge: latestAge, FileCount: result.FileCount, LastCheck: now})

		if sleepOrStop(checkInterval, stop) {
			return
		}
	}
}

func publishStatus(cb func(MonitorSnapshot), snapshot MonitorSnapshot) {
	if cb != nil {
		cb(snapshot)
	}
}

func shouldStop(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func sleepOrStop(d time.Duration, stop <-chan struct{}) bool {
	if stop == nil {
		time.Sleep(d)
		return false
	}
	select {
	case <-stop:
		return true
	case <-time.After(d):
		return false
	}
}

func shouldSendAlert(state MonitorState, lastAlertTime time.Time, now time.Time, cooldown time.Duration) bool {
	if state != StateAlert {
		return true
	}
	if lastAlertTime.IsZero() {
		return true
	}
	return now.Sub(lastAlertTime) >= cooldown
}

func scanFolder(folderPath string, cfg Config) (ScanResult, error) {
	var result ScanResult
	info, err := os.Stat(folderPath)
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, fmt.Errorf("path bukan folder: %s", folderPath)
	}

	if strings.EqualFold(cfg.ScanMode, "date_folder") {
		folders := buildDateFolders(folderPath, cfg)
		for _, target := range folders {
			info, err := os.Stat(target)
			if err != nil || !info.IsDir() {
				continue
			}
			result.ScannedFolders = append(result.ScannedFolders, target)
			partial, err := scanSingleFolder(target, cfg, true)
			if err != nil {
				continue
			}
			mergeScanResult(&result, partial)
		}
		return result, nil
	}

	partial, err := scanSingleFolder(folderPath, cfg, cfg.IncludeSubfolders)
	if err != nil {
		return result, err
	}
	mergeScanResult(&result, partial)
	if len(result.ScannedFolders) == 0 {
		result.ScannedFolders = []string{folderPath}
	}
	return result, nil
}

func buildDateFolders(root string, cfg Config) []string {
	lookback := cfg.ScanLookbackDays
	if lookback <= 0 {
		lookback = 3
	}
	if lookback > 14 {
		lookback = 14
	}
	pattern := cfg.DateFolderPattern
	if pattern == "" {
		pattern = "yyyy\\MMdd"
	}
	folders := make([]string, 0, lookback)
	now := time.Now()
	for i := 0; i < lookback; i++ {
		date := now.AddDate(0, 0, -i)
		rel := formatDateFolder(pattern, date)
		folders = append(folders, filepath.Join(root, rel))
	}
	return folders
}

func formatDateFolder(pattern string, t time.Time) string {
	layout := pattern
	layout = strings.ReplaceAll(layout, "yyyy", "2006")
	layout = strings.ReplaceAll(layout, "YYYY", "2006")
	layout = strings.ReplaceAll(layout, "MM", "01")
	layout = strings.ReplaceAll(layout, "dd", "02")
	layout = strings.ReplaceAll(layout, "DD", "02")
	formatted := t.Format(layout)
	formatted = strings.ReplaceAll(formatted, "/", string(os.PathSeparator))
	formatted = strings.ReplaceAll(formatted, "\\", string(os.PathSeparator))
	return formatted
}

func scanSingleFolder(folderPath string, cfg Config, recursive bool) (ScanResult, error) {
	var result ScanResult
	checkFile := func(path string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if shouldIgnoreFile(name, cfg) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		modTime := info.ModTime()
		result.FileCount++
		if result.LatestTime.IsZero() || modTime.After(result.LatestTime) {
			result.LatestTime = modTime
			result.LatestPath = path
		}
		return nil
	}

	if recursive {
		err := filepath.WalkDir(folderPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			return checkFile(path, d)
		})
		return result, err
	}

	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		_ = checkFile(filepath.Join(folderPath, entry.Name()), entry)
	}
	return result, nil
}

func mergeScanResult(dst *ScanResult, src ScanResult) {
	dst.FileCount += src.FileCount
	if src.LatestTime.IsZero() {
		return
	}
	if dst.LatestTime.IsZero() || src.LatestTime.After(dst.LatestTime) {
		dst.LatestTime = src.LatestTime
		dst.LatestPath = src.LatestPath
	}
}

func shouldIgnoreFile(name string, cfg Config) bool {
	lower := strings.ToLower(name)
	ext := strings.ToLower(filepath.Ext(name))

	for _, ignoredName := range cfg.IgnoreFileNames {
		ignoredName = strings.ToLower(strings.TrimSpace(ignoredName))
		if ignoredName != "" && lower == ignoredName {
			return true
		}
	}

	for _, prefix := range cfg.IgnorePrefixes {
		if prefix != "" && strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return true
		}
	}

	for _, ignored := range cfg.IgnoreExtensions {
		ignored = strings.ToLower(strings.TrimSpace(ignored))
		if ignored != "" && ext == ignored {
			return true
		}
	}

	allowed := normalizeExtList(cfg.AllowedExtensions)
	if len(allowed) == 0 {
		return false
	}
	_, ok := allowed[ext]
	return !ok
}

func normalizeExtList(list []string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range list {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if !strings.HasPrefix(item, ".") {
			item = "." + item
		}
		result[item] = struct{}{}
	}
	return result
}

type RemoteAlertPayload struct {
	Source        string `json:"source"`
	Version       string `json:"version"`
	MachineName   string `json:"machineName"`
	SourceName    string `json:"sourceName"`
	Level         string `json:"level"`
	AlertName     string `json:"alertName"`
	Message       string `json:"message"`
	FolderPath    string `json:"folderPath,omitempty"`
	LatestPath    string `json:"latestPath,omitempty"`
	LatestTime    string `json:"latestTime,omitempty"`
	MaxAgeMinutes int    `json:"maxAgeMinutes"`
	AlertTime     string `json:"alertTime"`
}

func sendRemoteAlertAsync(cfg Config, logger *log.Logger, level, message, folderPath, latestPath string, latestTime time.Time) {
	if !cfg.RemoteAlertEnabled {
		return
	}
	if strings.TrimSpace(cfg.RemoteAlertURL) == "" {
		if logger != nil {
			logger.Println("Remote alert skipped: remoteAlertUrl kosong")
		}
		return
	}
	go func() {
		if err := sendRemoteAlert(cfg, level, message, folderPath, latestPath, latestTime); err != nil {
			if logger != nil {
				logger.Println("Remote alert failed:", err)
			}
			return
		}
		if logger != nil {
			logger.Println("Remote alert sent:", level, cfg.RemoteAlertURL)
		}
	}()
}

func sendRemoteAlert(cfg Config, level, message, folderPath, latestPath string, latestTime time.Time) error {
	machineName, _ := os.Hostname()
	latestTimeText := ""
	if !latestTime.IsZero() {
		latestTimeText = latestTime.Format("2006-01-02 15:04:05")
	}
	payload := RemoteAlertPayload{
		Source:        "foldermonitor",
		Version:       version,
		MachineName:   machineName,
		SourceName:    cfg.RemoteAlertSourceName,
		Level:         level,
		AlertName:     cfg.AlertTitle,
		Message:       message,
		FolderPath:    folderPath,
		LatestPath:    latestPath,
		LatestTime:    latestTimeText,
		MaxAgeMinutes: cfg.MaxAgeMinutes,
		AlertTime:     time.Now().Format("2006-01-02 15:04:05"),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	timeout := time.Duration(cfg.RemoteAlertTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if timeout > 10*time.Second {
		timeout = 10 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodPost, cfg.RemoteAlertURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "FolderMonitor/"+version)
	if cfg.RemoteAlertSecret != "" {
		req.Header.Set("X-Relay-Secret", cfg.RemoteAlertSecret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("remote relay returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func executableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func resolvePath(base string, path string) string {
	if path == "" {
		return base
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

func newLogger(logPath string) *log.Logger {
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		fmt.Println("Gagal membuat folder log:", err)
		return log.New(os.Stdout, "", log.LstdFlags)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Println("Gagal membuka log file:", err)
		return log.New(os.Stdout, "", log.LstdFlags)
	}
	return log.New(f, "", log.LstdFlags)
}

func normalizeStartupMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "boot" || mode == "startup" || mode == "onstart" {
		return "boot"
	}
	return "login"
}

func normalizeBeepMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "until_confirmed", "untilconfirmed", "until_confirm", "confirm", "confirmation":
		return "until_confirmed"
	case "duration", "timed", "timer":
		return "duration"
	case "short", "default":
		return "short"
	default:
		return "until_confirmed"
	}
}
