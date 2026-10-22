package workbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/orochaa/go-clack/core"
	"github.com/orochaa/go-clack/prompts/symbols"
	"github.com/orochaa/go-clack/prompts/theme"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Use Clack's selection model and theme with Workbox's synchronous, cancellable
// input loop. The upstream Run method blocks on ReadRune during cancellation.
// Keeping terminal ownership here also preserves SIGINT and avoids background
// readers consuming input intended for the next prompt.
func terminalSelect(ctx context.Context, w io.Writer, title string, options []string, selected int) (int, *Failure) {
	if len(options) == 0 {
		return 0, fail("INVALID_ARGUMENTS", "Menu has no options")
	}
	if selected < 0 || selected >= len(options) {
		selected = 0
	}
	if plainPrompt(w) {
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
	fmt.Fprint(w, "\x1b[?25l")
	defer fmt.Fprint(w, "\x1b[?25h")
	// Use a non-terminal formatting sink: Clack then uses its fixed 80-column
	// fallback. Its formatter cannot safely handle zero widths or mid-frame
	// resizing. Actual frames are written only through w below.
	layout, err := os.Open(os.DevNull)
	if err != nil {
		return 0, ioFailure("Could not prepare terminal layout")
	}
	defer layout.Close()
	p := newClackSelect(layout, title, options, selected)
	lines := 0
	draw := func() {
		if lines > 0 {
			fmt.Fprintf(w, "\x1b[%dA\r\x1b[J", lines)
		}
		frame := p.Render(&p.Prompt)
		// go-clack's color detection checks the terminal before NO_COLOR. Enforce
		// our public no-color contract without mutating library global state.
		if os.Getenv("NO_COLOR") != "" {
			frame = clackColor.ReplaceAllString(frame, "")
		}
		frame = strings.ReplaceAll(frame, "\r\n", "\n")
		fmt.Fprintln(w, frame)
		lines = strings.Count(frame, "\n") + 1
	}
	draw()
	for {
		key, e := terminalKey(ctx, fd)
		if e != nil {
			p.State = core.CancelState
			draw()
			return 0, e
		}
		switch key {
		case "up", "k":
			// Preserve wraparound navigation from the existing Workbox menus.
			if p.CursorIndex == 0 {
				p.Emit(core.KeyEvent, &core.Key{Name: core.EndKey})
			} else {
				p.Emit(core.KeyEvent, &core.Key{Name: core.UpKey})
			}
		case "down", "j":
			if p.CursorIndex == len(options)-1 {
				p.Emit(core.KeyEvent, &core.Key{Name: core.HomeKey})
			} else {
				p.Emit(core.KeyEvent, &core.Key{Name: core.DownKey})
			}
		case "enter":
			p.State = core.SubmitState
			draw()
			return p.Value, nil
		case "escape", "cancel":
			p.State = core.CancelState
			draw()
			return 0, fail("INTERRUPTED", "Initialization cancelled")
		default:
			continue
		}
		p.State = core.ActiveState
		if plainPrompt(w) {
			fmt.Fprint(w, "\nTerminal size changed; continuing with numbered choices.\n")
			if err := terminalSet(fd, old); err != nil {
				return 0, ioFailure("Could not restore terminal")
			}
			fmt.Fprint(w, "\x1b[?25h")
			return terminalSelectPlain(ctx, w, title, options, p.Value)
		}
		draw()
	}
}

var clackColor = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plainPrompt(w io.Writer) bool {
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	if f, ok := w.(*os.File); ok {
		width, height, err := term.GetSize(int(f.Fd()))
		// The upstream formatter is byte-oriented. In small viewports use the
		// scrolling numbered fallback instead of risking broken wrapped frames.
		if err != nil || width < 80 || height < 18 {
			return true
		}
	}
	return false
}

func newClackSelect(output *os.File, title string, options []string, selected int) *core.SelectPrompt[int] {
	choices := make([]*core.SelectOption[int], len(options))
	for i, label := range options {
		choices[i] = &core.SelectOption[int]{Label: label, Value: i}
	}
	return core.NewSelectPrompt(core.SelectPromptParams[int]{
		Output: output, InitialValue: selected, Options: choices,
		Render: func(p *core.SelectPrompt[int]) string {
			value := p.Options[p.CursorIndex].Label
			active := value
			if p.State != core.SubmitState && p.State != core.CancelState {
				rows := make([]string, len(p.Options))
				for i, choice := range p.Options {
					marker := symbols.RADIO_INACTIVE
					if i == p.CursorIndex {
						marker = symbols.RADIO_ACTIVE
					}
					rows[i] = marker + " " + choice.Label
				}
				active = strings.Join(rows, "\n") + "\n↑/↓ select · Enter confirm · Esc cancel"
			}
			if p.State == core.CancelState {
				value = "Cancelled"
			}
			return theme.ApplyTheme(theme.ThemeParams[int]{Context: p.Prompt, Message: title, Value: value, ValueWithCursor: active})
		},
	})
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
