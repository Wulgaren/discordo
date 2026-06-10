//go:build darwin

package notifications

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"

	gosxnotifier "github.com/deckarep/gosx-notifier"
)

// systemNotifierPath resolves a system-installed terminal-notifier binary.
//
// gosx-notifier extracts a bundled terminal-notifier.app into the tmp dir and
// only re-extracts it when the executable itself is missing. macOS purges tmp
// files individually after ~3 days of inactivity, which can delete parts of
// the app bundle (e.g. Info.plist) while keeping the executable, leaving a
// permanently broken notifier that exits with status 1. A system install
// (e.g. via Homebrew) lives in a durable location, so prefer it.
var systemNotifierPath = sync.OnceValue(func() string {
	if path, err := exec.LookPath("terminal-notifier"); err == nil {
		return path
	}
	for _, path := range []string{
		"/opt/homebrew/bin/terminal-notifier",
		"/usr/local/bin/terminal-notifier",
	} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
})

func sendDesktopNotification(title string, message string, image string, playSound bool, _ int) error {
	if path := systemNotifierPath(); path != "" {
		return notifyWithBinary(path, title, message, image, playSound)
	}

	// Fall back to the bundled (tmp dir) notifier from gosx-notifier.
	notification := gosxnotifier.NewNotification(message)
	notification.Title = title
	notification.ContentImage = image
	if playSound {
		notification.Sound = gosxnotifier.Default
	}
	if err := notification.Push(); err != nil {
		return fmt.Errorf("bundled terminal-notifier: %w%s", err, exitErrStderr(err))
	}
	return nil
}

func notifyWithBinary(path, title, message, image string, playSound bool) error {
	args := []string{"-message", message}
	if title != "" {
		args = append(args, "-title", title)
	}
	if image != "" {
		args = append(args, "-contentImage", image)
	}
	if playSound {
		args = append(args, "-sound", "default")
	}

	cmd := exec.Command(path, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := ""
		if msg := bytes.TrimSpace(stderr.Bytes()); len(msg) > 0 {
			detail = ": " + string(msg)
		}
		return fmt.Errorf("terminal-notifier (%s): %w%s", path, err, detail)
	}
	return nil
}

// exitErrStderr extracts captured stderr from an exec.ExitError so "exit
// status 1" errors carry the actual failure reason in the log.
func exitErrStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return ": " + string(bytes.TrimSpace(exitErr.Stderr))
	}
	return ""
}
