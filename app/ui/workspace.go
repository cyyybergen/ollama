//go:build windows || darwin

package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ollama/ollama/app/docfiles"
	"github.com/ollama/ollama/app/store"
)

// chatWorkspaceDir returns the per-chat workspace directory used by document
// tools, located under the app data directory.
func chatWorkspaceDir(chatID string) (string, error) {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = filepath.Join(os.Getenv("LOCALAPPDATA"), "Ollama")
	default:
		base = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Ollama")
	}
	// chat IDs are UUIDs; reject anything that could escape the workspaces dir
	if chatID == "" || chatID != filepath.Base(chatID) || !filepath.IsLocal(chatID) {
		return "", fmt.Errorf("invalid chat id %q", chatID)
	}
	return filepath.Join(base, "workspaces", chatID), nil
}

// chatHasDocuments reports whether any user message has a document attachment
// that the document tools can operate on.
func chatHasDocuments(messages []store.Message) bool {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		for _, a := range m.Attachments {
			if docfiles.IsDocument(a.Filename) {
				return true
			}
		}
	}
	return false
}

// syncAttachmentsToWorkspace copies document attachments from user messages
// into the chat workspace so the document tools can read and edit them.
// Existing files are not overwritten, preserving edits made by tools.
func syncAttachmentsToWorkspace(dir string, messages []store.Message) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		for _, a := range m.Attachments {
			if !docfiles.IsDocument(a.Filename) {
				continue
			}
			path, err := docfiles.SafeJoin(dir, a.Filename)
			if err != nil {
				continue
			}
			if _, err := os.Stat(path); err == nil {
				continue
			}
			if err := os.WriteFile(path, a.Data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
