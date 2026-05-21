//go:build linux || freebsd

package clipboard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

type Format int

const (
	FmtText Format = iota + 1
	FmtImage
)

type backend int

const (
	backendNone backend = iota
	backendWayland
	backendXclip
	backendXsel
)

var active backend

func Init() error {
	if _, ok := os.LookupEnv("WAYLAND_DISPLAY"); ok {
		if _, err := exec.LookPath("wl-copy"); err != nil {
			return fmt.Errorf("wl-copy not found: %w", err)
		}
		if _, err := exec.LookPath("wl-paste"); err != nil {
			return fmt.Errorf("wl-paste not found: %w", err)
		}
		active = backendWayland
		return nil
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		active = backendXclip
		return nil
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		active = backendXsel
		return nil
	}
	return errors.New("no clipboard tool found: install wl-clipboard (Wayland) or xclip/xsel (X11)")
}

func Read(t Format) ([]byte, error) {
	switch active {
	case backendWayland:
		return readWayland(t)
	case backendXclip:
		return readXclip(t)
	case backendXsel:
		return readXsel(t)
	default:
		return nil, errors.New("clipboard not initialized")
	}
}

func Write(t Format, buf []byte) error {
	switch active {
	case backendWayland:
		return writeWayland(t, buf)
	case backendXclip:
		return writeXclip(t, buf)
	case backendXsel:
		return writeXsel(t, buf)
	default:
		return errors.New("clipboard not initialized")
	}
}

func readWayland(t Format) ([]byte, error) {
	cmd := exec.Command("wl-paste", "-nt", formatToMIME(t))
	return runRead(cmd)
}

func writeWayland(t Format, buf []byte) error {
	cmd := exec.Command("wl-copy", "-t", formatToMIME(t))
	return runWrite(cmd, buf)
}

func readXclip(t Format) ([]byte, error) {
	if t == FmtImage {
		cmd := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o")
		return runRead(cmd)
	}
	cmd := exec.Command("xclip", "-selection", "clipboard", "-o")
	return runRead(cmd)
}

func writeXclip(t Format, buf []byte) error {
	if t == FmtImage {
		cmd := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png")
		return runWrite(cmd, buf)
	}
	cmd := exec.Command("xclip", "-selection", "clipboard")
	return runWrite(cmd, buf)
}

func readXsel(t Format) ([]byte, error) {
	if t != FmtText {
		return nil, errors.New("xsel only supports text clipboard")
	}
	cmd := exec.Command("xsel", "--clipboard", "--output")
	return runRead(cmd)
}

func writeXsel(t Format, buf []byte) error {
	if t != FmtText {
		return errors.New("xsel only supports text clipboard")
	}
	cmd := exec.Command("xsel", "--clipboard", "--input")
	return runWrite(cmd, buf)
}

func runRead(cmd *exec.Cmd) ([]byte, error) {
	out := &bytes.Buffer{}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func runWrite(cmd *exec.Cmd, buf []byte) error {
	cmd.Stdin = bytes.NewReader(buf)
	return cmd.Run()
}

func formatToMIME(t Format) string {
	switch t {
	case FmtImage:
		return "image/png"
	case FmtText:
		fallthrough
	default:
		return "text/plain;charset=utf-8"
	}
}
