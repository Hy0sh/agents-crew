package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
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
func TestSlashCommand(t *testing.T) {
	if got, err := slashCommand("  /reload-plugins \n"); err != nil || got != "/reload-plugins" {
		t.Errorf("= %q, %v", got, err)
	}
	for _, bad := range []string{"reload-plugins", "/a\n/b", "/clear", "/clear now", ""} {
		if _, err := slashCommand(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// --interrupt leaves the message first, then presses Escape in the worker:
// stopped, it goes idle and the watcher types the message.
func TestTellInterrupt(t *testing.T) {
	repo := testSwarm(t, 1, poolWorker{Index: 1, State: workerBusy, Task: 3})
	var pressed []string
	pressKeys = func(name string, keys ...string) error {
		pressed = append(append(pressed, name), keys...)
		return nil
	}
	t.Cleanup(func() { pressKeys = herdr.AgentSendKeys })
	if err := tellByHand(repo, "worker1", []string{"stop", "there"}, true, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if want := []string{names.Agent(names.Slug(repo), "worker1"), "esc"}; !slices.Equal(pressed, want) {
		t.Errorf("pressed %q, want %q", pressed, want)
	}
	if content, _ := os.ReadFile(filepath.Join(names.StatusDir(repo), "worker1.tell")); strings.TrimSpace(string(content)) != "stop there" {
		t.Errorf("tell file = %q", content)
	}
	pressed = nil
	if err := tellByHand(repo, "worker1", nil, true, strings.NewReader(" \n"), io.Discard); err == nil || pressed != nil {
		t.Errorf("an empty message: err = %v, pressed %q; want it refused before any key", err, pressed)
	}
}

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
