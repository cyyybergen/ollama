//go:build darwin

package ui

import "os/exec"

// openWithDefaultApp opens the file at path with the OS default application.
func openWithDefaultApp(path string) error {
	return exec.Command("open", path).Start()
}
