package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckStartReportsEveryMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty directory: nothing resolves

	err := CheckStart("claude", "claude")
	if err == nil {
		t.Fatal("CheckStart() = nil, want an error when herdr and claude are both missing")
	}
	for _, want := range []string{"herdr", "claude", "herdr.dev", "npm install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("CheckStart() error = %q, missing %q", err.Error(), want)
		}
	}
	if strings.Count(err.Error(), "npm install") != 1 {
		t.Errorf("CheckStart() error = %q, want the same kind reported once", err.Error())
	}
}

func TestCheckStartChecksEachKindsOwnBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := CheckStart("claude", "codex")
	if err == nil {
		t.Fatal("CheckStart() = nil, want an error when no agent CLI resolves")
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("CheckStart() error = %q, missing the codex binary", err.Error())
	}
	if strings.Count(err.Error(), "npm install") != 1 {
		t.Errorf("CheckStart() error = %q, the claude npm hint should not be repeated for another kind", err.Error())
	}
}

func TestCheckStartDoesNotRequireClaudeForOtherKinds(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := CheckStart("codex", "codex")
	if err == nil {
		t.Fatal("CheckStart() = nil, want an error when herdr and codex are both missing")
	}
	if strings.Contains(err.Error(), "claude") {
		t.Errorf("CheckStart() error = %q, claude is not a dependency of a codex-only run", err.Error())
	}
}

func TestCheckStopOnlyRequiresHerdr(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := CheckStop()
	if err == nil {
		t.Fatal("CheckStop() = nil, want an error when herdr is missing")
	}
	if strings.Contains(err.Error(), "claude") {
		t.Errorf("CheckStop() error = %q, should not require claude", err.Error())
	}
}

// warnAgents runs WarnIfAgentsFileMissing over a repo seeded with the
// given files (paths relative to it) and returns what it printed.
func warnAgents(t *testing.T, workerKind string, files ...string) string {
	t.Helper()
	repo := t.TempDir()
	t.Setenv("HOME", repo) // also pins the ~/.claude/CLAUDE.md exclusion to this repo
	for _, f := range files {
		path := filepath.Join(repo, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got string
	WarnIfAgentsFileMissing(repo, workerKind, func(format string, a ...any) { got = fmt.Sprintf(format, a...) })
	return got
}

func TestWarnIfAgentsFileMissing(t *testing.T) {
	if got := warnAgents(t, "codex", "CLAUDE.md"); !strings.Contains(got, "AGENTS.md") || !strings.Contains(got, "codex") {
		t.Errorf("CLAUDE.md-only repo with a codex worker: got %q, want a warning", got)
	}
	if got := warnAgents(t, "claude", "CLAUDE.md"); got != "" {
		t.Errorf("claude workers read CLAUDE.md fine: got %q, want silence", got)
	}
	if got := warnAgents(t, "codex", "CLAUDE.md", "AGENTS.md"); got != "" {
		t.Errorf("repo with an AGENTS.md: got %q, want silence", got)
	}
	// ~/.claude/CLAUDE.md loads alongside AGENTS.md instead of shadowing
	// it, so it must never trigger the warning on its own.
	if got := warnAgents(t, "codex", filepath.Join(".claude", "CLAUDE.md")); got != "" {
		t.Errorf("only the user-level ~/.claude/CLAUDE.md: got %q, want silence", got)
	}
}

func TestWarnIfWtmMissingIsNonFatal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var got string
	WarnIfWtmMissing(func(format string, a ...any) { got = format })
	if !strings.Contains(got, "wtm") {
		t.Errorf("expected a wtm-mentioning note, got %q", got)
	}
}
