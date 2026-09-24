package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
}

// Claude Code asks approval for every Monitor it doesn't have a rule for,
// with no "don't ask again": without this rule the master would stall on
// a prompt at each re-arm, every 30 minutes. The rule must match the
// command the brief gives, and only that command.
func TestMasterArgsAllowOnlyTheInboxWatch(t *testing.T) {
	watch, _ := inboxWatchCommand("claude", "", "/bin/acw", "/repo/inbox")
	got := masterArgs("opus", "/bin/acw", watch, "", "")
	i := slices.Index(got, "--allowedTools")
	if i < 0 || i+1 >= len(got) || got[i+1] != "Bash(/bin/acw __inbox-watch:*)" {
		t.Errorf("masterArgs() = %v, want --allowedTools Bash(/bin/acw __inbox-watch:*)", got)
	}
	if !strings.HasPrefix(watch, strings.TrimSuffix(strings.TrimPrefix(got[i+1], "Bash("), ":*)")) {
		t.Errorf("rule %q does not cover the watch command %q", got[i+1], watch)
	}
	if !slices.Contains(got, "opus") {
		t.Errorf("masterArgs() = %v, dropped the model", got)
	}

	if got := masterArgs("opus", "/bin/acw", "", "", ""); slices.Contains(got, "--allowedTools") {
		t.Errorf("masterArgs() without inbox = %v; --allowedTools is Claude Code's own flag", got)
	}
}

// Filing a question for the user must not itself stall on an approval
// prompt, and the rule must cover the command the brief gives.
func TestMasterArgsAllowTheDecisionQueue(t *testing.T) {
	watch, _ := inboxWatchCommand("claude", "", "/bin/acw", "/repo/inbox")
	decide, _ := decisionCommand("claude", "", "/bin/acw", "/repo", watch)
	got := masterArgs("opus", "/bin/acw", watch, decide, "0b8c-uuid")
	i := slices.Index(got, "--allowedTools")
	if i < 0 || !slices.Contains(got[i+1:], "Bash(/bin/acw __decision:*)") {
		t.Fatalf("masterArgs() = %v, want the decision rule after --allowedTools", got)
	}
	// --allowedTools takes every value after it: the session ID must come
	// before, or it would be read as a tool rule.
	if s := slices.Index(got, "--session-id"); s < 0 || s > i || got[s+1] != "0b8c-uuid" {
		t.Errorf("masterArgs() = %v, want --session-id 0b8c-uuid before --allowedTools", got)
	}
	if !strings.HasPrefix(decide, "/bin/acw __decision ") {
		t.Errorf("decision command %q is not covered by the rule", decide)
	}
}

func TestDecisionCommand(t *testing.T) {
	const watch = "/bin/acw __inbox-watch /repo/inbox"
	if got, warn := decisionCommand("claude", "", "/bin/acw", "/repo", watch); got != "/bin/acw __decision --repo /repo" || warn {
		t.Errorf("decisionCommand() = %q, %v", got, warn)
	}
	// Answers come back through the inbox: no inbox, no queue.
	if got, _ := decisionCommand("claude", "", "/bin/acw", "/repo", ""); got != "" {
		t.Errorf("decisionCommand() without inbox = %q, want none", got)
	}
	if got, warn := decisionCommand("claude", "{{.InboxWatch}}", "/bin/acw", "/repo", watch); got != "" || !warn {
		t.Errorf("custom brief without {{.DecisionCmd}} = %q, %v; want no queue and a warning", got, warn)
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
