//go:build windows

package ui

import (
	"os/exec"
	"syscall"
)

// openWithDefaultApp opens the file at path with the OS default application.
func openWithDefaultApp(path string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
