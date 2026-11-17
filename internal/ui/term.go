//go:build darwin || linux || freebsd || netbsd || openbsd

// Package ui is Kalonide's terminal interface: colors, a line editor with
// history, an arrow-key picker and the live renderer for agent output.
package ui

import (
	"os"

	"golang.org/x/sys/unix"
)

// Terminal controls the input terminal's mode.
type Terminal struct {
	fd    int
	saved *unix.Termios
}

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGet)
	return err == nil
}

// NewTerminal wraps stdin. It returns nil when stdin is not a terminal.
func NewTerminal() *Terminal {
	fd := int(os.Stdin.Fd())
	st, err := unix.IoctlGetTermios(fd, ioctlGet)
	if err != nil {
		return nil
	}
	return &Terminal{fd: fd, saved: st}
}

// Raw switches to raw mode for key-by-key input. Signals are off: Ctrl-C
// arrives as a byte.
func (t *Terminal) Raw() error {
	st := *t.saved
	st.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	st.Oflag |= unix.OPOST
	st.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	st.Cflag &^= unix.CSIZE | unix.PARENB
	st.Cflag |= unix.CS8
	st.Cc[unix.VMIN] = 1
	st.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(t.fd, ioctlSet, &st)
}

// Quiet is used while the agent works: no echo of stray keys, but Ctrl-C
// still raises SIGINT so a turn can be cancelled.
func (t *Terminal) Quiet() error {
	st := *t.saved
	st.Lflag &^= unix.ECHO | unix.ECHONL
	st.Lflag |= unix.ISIG | unix.ICANON
	return unix.IoctlSetTermios(t.fd, ioctlSet, &st)
}

// Restore puts the terminal back as it was.
func (t *Terminal) Restore() error {
	return unix.IoctlSetTermios(t.fd, ioctlSet, t.saved)
}

// Width returns the terminal width in columns (80 if unknown).
func Width() int {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80
	}
	return int(ws.Col)
}
