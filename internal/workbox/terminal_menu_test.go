package workbox

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMenuArrowSequences(t *testing.T) {
	for sequence, want := range map[string]string{"[A": "up", "[B": "down", "OA": "up", "OB": "down", "[1;5A": "up", "[1;2B": "down", "[C": "ignore", "[200~": "ignore", "x": "ignore"} {
		if got := menuEscape(sequence); got != want {
			t.Errorf("%q: %s, want %s", sequence, got, want)
		}
	}
}
func TestMenuReadsCompleteKeysAndRestoresFollowingInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err = w.Write([]byte("\x1b[A\x1bOB\x1b[1;5A\nq")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"up", "down", "up", "enter", "q"} {
		got, e := terminalKey(context.Background(), int(r.Fd()))
		if e != nil || got != want {
			t.Fatal(got, want, e)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := terminalKey(ctx, int(r.Fd())); e == nil || e.Code != "INTERRUPTED" {
		t.Fatal(e)
	}
}
func TestInitCancellationTextIsConcise(t *testing.T) {
	var out bytes.Buffer
	renderInitOutcome(&out, map[string]any{"state": "failed", "initialized": false}, fail("INTERRUPTED", "Initialization cancelled"))
	if !strings.Contains(out.String(), "No initialization changes") || strings.Contains(out.String(), "null") || strings.Contains(out.String(), "failed") {
		t.Fatal(out.String())
	}
	out.Reset()
	renderInitOutcome(&out, map[string]any{"state": "partial", "initialized": true, "settings": map[string]any{"publication": "published"}, "copy": map[string]any{"state": "copied"}}, fail("INTERRUPTED", "Initialization interrupted"))
	if !strings.Contains(out.String(), "published") || !strings.Contains(out.String(), "copied") || strings.Contains(out.String(), "No initialization changes") {
		t.Fatal(out.String())
	}
}
