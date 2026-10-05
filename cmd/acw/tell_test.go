package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Read off a Claude Code pane through herdr agent read --format ansi: an
// empty input shows a dimmed placeholder, which is no one's text.
func TestTypedInput(t *testing.T) {
	rule := "\x1b[2m────────\x1b[0m\r"
	for _, tc := range []struct{ name, screen, want string }{
		{"empty input", rule + "\n❯ \x1b[0m\x1b[2mTry \"fix lint errors\"\x1b[0m\r\n" + rule + "\n  [Haiku 4.5] │ probe\r\n", ""},
		{"typed text", rule + "\n❯ hello there\r\n" + rule + "\n", "hello there"},
		{"choice on screen", " Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No\r\n", "1. Yes"},
		{"no input line", "some output\r\n", ""},
	} {
		if got := typedInput(tc.screen); got != tc.want {
			t.Errorf("%s: typedInput() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Messages queue up in order and come out together, as one.
func TestTellWorkerQueuesMessages(t *testing.T) {
	dir := t.TempDir()
	for _, msg := range []string{"Go for B.\n", "Also: no migration."} {
		if err := tellWorker(dir, "worker1", msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := tellWorker(dir, "worker1", "  \n"); err == nil {
		t.Error("an empty message must be refused")
	}
	content, _ := os.ReadFile(filepath.Join(dir, "worker1.tell"))
	if got, want := masterMessage(string(content)), "Message from the master:\nGo for B.\n\nAlso: no migration."; got != want {
		t.Errorf("delivered = %q, want %q", got, want)
	}
}
