package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDrainOnceEmitsPendingLinesOnce(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	for _, line := range []string{"worker1 a rendu la main", "worker2 a rendu la main"} {
		if err := appendLine(inbox, line); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := drainOnce(inbox, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "worker1 a rendu la main\nworker2 a rendu la main\n" {
		t.Errorf("first drain = %q, want both lines in order", out.String())
	}

	out.Reset()
	if err := drainOnce(inbox, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("second drain = %q, a line must be emitted only once", out.String())
	}

	// A ping written while nothing watches (between a Monitor's expiry
	// and its re-arm) waits for the next drain instead of being lost.
	if err := appendLine(inbox, "worker3 a rendu la main"); err != nil {
		t.Fatal(err)
	}
	if err := drainOnce(inbox, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "worker3 a rendu la main\n" {
		t.Errorf("third drain = %q", out.String())
	}
}

func TestWatchInboxStopsWhenTheStatusDirIsGone(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "gone", "inbox")
	if err := watchInbox(inbox, &bytes.Buffer{}, 0); err != nil {
		t.Errorf("watchInbox() = %v; a removed status dir (acw stop) is a normal end", err)
	}
}

func TestNextInboxReturnsEveryWaitingLineAtOnce(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	for _, line := range []string{"worker1 a rendu la main", "worker2 est bloqué"} {
		if err := appendLine(inbox, line); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := nextInbox(inbox, &out, 0); err != nil {
		t.Fatal(err)
	}
	if out.String() != "worker1 a rendu la main\nworker2 est bloqué\n" {
		t.Errorf("nextInbox() = %q, want both lines in order, then return", out.String())
	}
}

// It waits for the next message instead of returning empty-handed:
// returning at once would wake the master for nothing, in a loop.
func TestNextInboxWaitsForALine(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = appendLine(inbox, "worker3 a rendu la main")
	}()
	var out bytes.Buffer
	if err := nextInbox(inbox, &out, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if out.String() != "worker3 a rendu la main\n" {
		t.Errorf("nextInbox() = %q", out.String())
	}
}

func TestNextInboxStopsWhenTheStatusDirIsGone(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "gone", "inbox")
	var out bytes.Buffer
	if err := nextInbox(inbox, &out, 0); err != nil || out.Len() != 0 {
		t.Errorf("nextInbox() = %q, %v; a removed status dir (acw stop) ends it silently", out.String(), err)
	}
}

func TestInboxWatchCommand(t *testing.T) {
	const exe, inbox = "/bin/acw", "/repo/.claude/worktrees/.acw-status/inbox"

	got, warn := inboxWatchCommand("claude", "", exe, inbox)
	if got != "/bin/acw __inbox-watch "+inbox || warn {
		t.Errorf("claude + built-in brief = %q, %v; want the watch command, no warning", got, warn)
	}

	if got, warn := inboxWatchCommand("codex", "", exe, inbox); got != "" || warn {
		t.Errorf("codex master = %q, %v; it has no Monitor, pings must stay typed", got, warn)
	}

	if got, warn := inboxWatchCommand("claude", "my brief", exe, inbox); got != "" || !warn {
		t.Errorf("custom brief without {{.InboxWatch}} = %q, %v; want no inbox and a warning", got, warn)
	}

	if got, _ := inboxWatchCommand("claude", "arm {{.InboxWatch}}", exe, inbox); got == "" {
		t.Error("custom brief referencing {{.InboxWatch}} should get the inbox")
	}

	if got, warn := inboxWatchCommand("claude", "run {{.InboxNext}}", exe, inbox); got == "" || warn {
		t.Errorf("custom brief referencing {{.InboxNext}} = %q, %v; want the inbox, no warning", got, warn)
	}
}

// Claude Code asks approval for every Monitor it doesn't have a rule for,
// with no "don't ask again": without this rule the master would stall on
// a prompt at each re-arm, every 30 minutes. The rule must match the
// command the brief gives, and only that command.
func TestMasterArgsAllowOnlyTheInboxWatch(t *testing.T) {
	watch, _ := inboxWatchCommand("claude", "", "/bin/acw", "/repo/inbox")
	got := masterArgs("opus", "/bin/acw", watch)
	i := slices.Index(got, "--allowedTools")
	if i < 0 || i+1 >= len(got) || got[i+1] != "Bash(/bin/acw __inbox-watch:*)" {
		t.Errorf("masterArgs() = %v, want --allowedTools Bash(/bin/acw __inbox-watch:*)", got)
	}
	if !strings.HasPrefix(watch, strings.TrimSuffix(strings.TrimPrefix(got[i+1], "Bash("), ":*)")) {
		t.Errorf("rule %q does not cover the watch command %q", got[i+1], watch)
	}
	next := inboxNextCommand("/bin/acw", "/repo/inbox")
	if i+2 >= len(got) || got[i+2] != "Bash(/bin/acw __inbox-next:*)" {
		t.Errorf("masterArgs() = %v, want a second rule Bash(/bin/acw __inbox-next:*)", got)
	} else if !strings.HasPrefix(next, strings.TrimSuffix(strings.TrimPrefix(got[i+2], "Bash("), ":*)")) {
		t.Errorf("rule %q does not cover the next command %q", got[i+2], next)
	}
	if !slices.Contains(got, "opus") {
		t.Errorf("masterArgs() = %v, dropped the model", got)
	}

	if got := masterArgs("opus", "/bin/acw", ""); slices.Contains(got, "--allowedTools") {
		t.Errorf("masterArgs() without inbox = %v; --allowedTools is Claude Code's own flag", got)
	}
}

func TestShellWordQuotesOnlyWhenNeeded(t *testing.T) {
	for in, want := range map[string]string{
		"/repo/.claude/inbox": "/repo/.claude/inbox",
		"/my repo/inbox":      `'/my repo/inbox'`,
		"/it's/inbox":         `'/it'\''s/inbox'`,
	} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMain(m *testing.M) {
	// drainOnce waits for an in-flight append before reading; no need to
	// in tests, where every append has returned.
	drainSettle = 0
	os.Exit(m.Run())
}
