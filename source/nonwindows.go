//go:build !windows

package main

import (
	"fmt"
	"log"
	"runtime"
)

func alertUser(cfg Config, title string, message string, mode string) {
	fmt.Printf("[%s] %s\n", title, message)
}

func runAlertChild(payloadPath string) error {
	return fmt.Errorf("alert child hanya didukung otomatis di Windows. OS saat ini: %s", runtime.GOOS)
}

func ensureStartupTaskAsync(cfg Config, logger *log.Logger) {
	logger.Println("Auto startup hanya didukung otomatis di Windows. OS saat ini:", runtime.GOOS)
}

func notifyInfo(title, message string) {
	fmt.Printf("[%s] %s\n", title, message)
}

func runDesktopApp(store *ConfigStore, exeDir string, logger *log.Logger) error {
	return fmt.Errorf("GUI/tray hanya didukung otomatis di Windows. OS saat ini: %s", runtime.GOOS)
}

func installStartupTask(taskName string, mode string) error {
	return fmt.Errorf("install startup hanya didukung otomatis di Windows. OS saat ini: %s", runtime.GOOS)
}

func uninstallStartupTask(taskName string) error {
	return fmt.Errorf("uninstall startup hanya didukung otomatis di Windows. OS saat ini: %s", runtime.GOOS)
}

func startupTaskExists(taskName string) (bool, error) {
	return false, nil
}

func enforceSingleInstance() {}
