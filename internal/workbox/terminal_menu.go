package workbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Menus use noncanonical input only while focused. ISIG remains enabled so
// the existing signal context handles Ctrl+C and terminal state is restored.
func terminalSelect(ctx context.Context, w io.Writer, title string, options []string, selected int) (int, *Failure) {
	if selected < 0 || selected >= len(options) {
		selected = 0
	}
	if os.Getenv("TERM") == "dumb" {
		return terminalSelectPlain(ctx, w, title, options, selected)
	}
	fd := int(os.Stdin.Fd())
	old, err := terminalState(fd)
	if err != nil {
		return 0, ioFailure("Could not access terminal")
	}
	next := *old
	next.Lflag &^= unix.ICANON | unix.ECHO
	next.Cc[unix.VMIN] = 1
	next.Cc[unix.VTIME] = 0
	if err = terminalSet(fd, &next); err != nil {
		return 0, ioFailure("Could not enable menu input")
	}
	defer terminalSet(fd, old)
	fmt.Fprintln(w, "\n"+title)
	lines := len(options) + 1
	draw := func() {
		for i, label := range options {
			marker := "  "
			if i == selected {
				marker = "> "
			}
			if i == selected && os.Getenv("NO_COLOR") == "" {
				fmt.Fprintf(w, "\r\x1b[2K\x1b[1m%s%s\x1b[0m\n", marker, label)
			} else {
				fmt.Fprintf(w, "\r\x1b[2K%s%s\n", marker, label)
			}
		}
		fmt.Fprint(w, "\r\x1b[2K  ↑/↓ select · Enter confirm · Esc cancel\n")
	}
	draw()
	for {
		key, e := terminalKey(ctx, fd)
		if e != nil {
			return 0, e
		}
		switch key {
		case "up", "k":
			selected = (selected + len(options) - 1) % len(options)
		case "down", "j":
			selected = (selected + 1) % len(options)
		case "enter":
			fmt.Fprintf(w, "\x1b[%dA\r\x1b[J", lines)
			fmt.Fprintf(w, "  %s\n", options[selected])
			return selected, nil
		case "escape", "cancel":
			return 0, fail("INTERRUPTED", "Initialization cancelled")
		default:
			continue
		}
		fmt.Fprintf(w, "\x1b[%dA", lines)
		draw()
	}
}
func terminalSelectPlain(ctx context.Context, w io.Writer, title string, options []string, selected int) (int, *Failure) {
	fmt.Fprintln(w, "\n"+title)
	for i, label := range options {
		fmt.Fprintf(w, "  %d. %s\n", i+1, label)
	}
	for {
		s, e := terminalLine(ctx, w, "Select a number (0 cancels)", strconv.Itoa(selected+1), false)
		if e != nil {
			return 0, e
		}
		if s == "0" {
			return 0, fail("INTERRUPTED", "Initialization cancelled")
		}
		n, err := strconv.Atoi(s)
		if err == nil && n > 0 && n <= len(options) {
			return n - 1, nil
		}
		fmt.Fprintln(w, "Enter one of the listed numbers.")
	}
}
func terminalByte(ctx context.Context, fd int, timeout time.Duration) (byte, bool, *Failure) {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return 0, false, fail("INTERRUPTED", "Initialization interrupted")
		}
		wait := 100
		if timeout > 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return 0, false, nil
			}
			if remaining < 100*time.Millisecond {
				wait = int(remaining.Milliseconds()) + 1
			}
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(poll, wait)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, false, ioFailure("Could not read menu input")
		}
		if n == 0 {
			continue
		}
		var buf [1]byte
		n, err = unix.Read(fd, buf[:])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, false, ioFailure("Could not read menu input")
		}
		if n == 0 {
			return 0, false, fail("INTERRUPTED", "Terminal input closed")
		}
		return buf[0], true, nil
	}
}
func terminalKey(ctx context.Context, fd int) (string, *Failure) {
	b, _, e := terminalByte(ctx, fd, 0)
	if e != nil {
		return "", e
	}
	switch b {
	case '\r', '\n':
		return "enter", nil
	case 3, 4:
		return "cancel", nil
	case 27:
		// Consume complete CSI/SS3 sequences, including modified arrow keys, rather
		// than leaving their suffix to contaminate a subsequent text field.
		prefix, ok, e := terminalByte(ctx, fd, 300*time.Millisecond)
		if e != nil {
			return "", e
		}
		if !ok {
			return "escape", nil
		}
		if prefix != '[' && prefix != 'O' {
			return "ignore", nil
		}
		sequence := []byte{prefix}
		for len(sequence) < 32 {
			c, ok, e := terminalByte(ctx, fd, 300*time.Millisecond)
			if e != nil {
				return "", e
			}
			if !ok {
				return "ignore", nil
			}
			sequence = append(sequence, c)
			if c >= 0x40 && c <= 0x7e {
				return menuEscape(string(sequence)), nil
			}
		}
		return "ignore", nil
	default:
		return string(b), nil
	}
}
func menuEscape(sequence string) string {
	if len(sequence) < 2 || (sequence[0] != '[' && sequence[0] != 'O') {
		return "ignore"
	}
	body := sequence[1 : len(sequence)-1]
	if strings.Trim(body, "0123456789;") != "" {
		return "ignore"
	}
	switch sequence[len(sequence)-1] {
	case 'A':
		return "up"
	case 'B':
		return "down"
	}
	return "ignore"
}
