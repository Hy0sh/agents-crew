package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Only the clash sections reach the master: the rest of the report (docker
// usage, build cache, leftover volumes) is the user's business.
// The master keeps the left of the screen; the workers share one column,
// the tallest pane halved each time.
func TestSplitFrom(t *testing.T) {
	for _, c := range []struct {
		name      string
		panes     []string
		heights   map[string]int
		want, dir string
		ratio     float64
	}{
		{"first worker", nil, nil, "master", "right", 0.6},
		{"second", []string{"w1"}, map[string]int{"w1": 58}, "w1", "down", 0.5},
		{"tallest first", []string{"w1", "w2", "w3"}, map[string]int{"w1": 15, "w2": 14, "w3": 29}, "w3", "down", 0.5},
		{"tie goes to the last", []string{"w1", "w2"}, map[string]int{"w1": 29, "w2": 29}, "w2", "down", 0.5},
		{"no layout", []string{"w1", "w2"}, nil, "w2", "down", 0.5},
	} {
		pane, dir, ratio := splitFrom("master", c.panes, c.heights)
		if pane != c.want || dir != c.dir || ratio != c.ratio {
			t.Errorf("%s: splitFrom() = %s %s %v, want %s %s %v", c.name, pane, dir, ratio, c.want, c.dir, c.ratio)
		}
	}
}

func TestPortClashesKeepsOnlyTheClashSections(t *testing.T) {
	report := "docker   289 MB used\n\n" +
		"port clashes between worktrees of one project (those two cannot run at the same time):\n" +
		"  9001 some-repo agents/worker1 rustfs / agents/worker2 minio\n" +
		"  the stride is too small\n\n" +
		"78 anonymous volume(s)\n"
	want := "port clashes between worktrees of one project (those two cannot run at the same time):\n" +
		"  9001 some-repo agents/worker1 rustfs / agents/worker2 minio\n" +
		"  the stride is too small"
	if got := portClashes(report); got != want {
		t.Errorf("portClashes() = %q, want %q", got, want)
	}
	if got := portClashes("docker   289 MB used\n"); got != "" {
		t.Errorf("portClashes(no clash) = %q, want nothing", got)
	}
}

// The Stop hook is what replaces the master's polling discipline, and it
// is delivered as a JSON string on a command line — the one place a
// quoting slip would silently produce a worker that starts fine and never
// reports anything.
func TestWorkerSettingsIsValidAndCarriesTheCommands(t *testing.T) {
	ping := pingCommand("", "master-3f9a1c", "worker2", "")
	statusLine := statusLineCommand("/bin/acw", "/repo/.claude/worktrees/.acw-status", "worker2")
	got, err := workerSettings(ping, statusLine)
	if err != nil {
		t.Fatalf("workerSettings() error = %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
		StatusLine *struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("settings are not valid JSON: %v\n%s", err, got)
	}

	stop, ok := parsed.Hooks["Stop"]
	if !ok || len(stop) != 1 || len(stop[0].Hooks) != 1 {
		t.Fatalf("settings do not declare exactly one Stop hook: %s", got)
	}
	cmd := stop[0].Hooks[0]
	if cmd.Type != "command" {
		t.Errorf("hook type = %q, want %q", cmd.Type, "command")
	}
	if cmd.Command != ping {
		t.Errorf("hook command = %q, want %q", cmd.Command, ping)
	}
	if parsed.StatusLine == nil || parsed.StatusLine.Type != "command" || parsed.StatusLine.Command != statusLine {
		t.Errorf("statusLine = %+v, want a command status line running %q", parsed.StatusLine, statusLine)
	}

	// Without acw's own path there is no status line to point at.
	if got, _ := workerSettings(ping, ""); strings.Contains(got, "statusLine") {
		t.Errorf("workerSettings(no status line) = %s, must leave the user's own", got)
	}
}

// Without an inbox (a master that can't watch one), the ping is typed
// into the master's input as before.
func TestPingCommandWithoutInboxPromptsTheMaster(t *testing.T) {
	got := pingCommand("", "master-3f9a1c", "worker2", "")
	if !strings.HasPrefix(got, "herdr agent prompt master-3f9a1c ") {
		t.Errorf("pingCommand() = %q, should prompt the master by name", got)
	}
	if !strings.Contains(got, "worker2") {
		t.Errorf("pingCommand() = %q, should name the worker it fires for", got)
	}
}

// With an inbox, the ping must land there as exactly one line, and never
// touch the master's input. Run through a real shell, from a path with
// the characters a quoting slip trips on: this is a command line nothing
// else checks before a worker silently stops reporting.
func TestPingCommandAppendsOneLineToTheInbox(t *testing.T) {
	dir := filepath.Join(t.TempDir(), `it's $HOME`)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(dir, "inbox")
	got := pingCommand(inbox, "master-3f9a1c", "worker2", "")
	if strings.Contains(got, "herdr") {
		t.Errorf("pingCommand() = %q, must not go through herdr when an inbox is set", got)
	}

	for range 2 {
		if out, err := exec.Command("sh", "-c", got).CombinedOutput(); err != nil {
			t.Fatalf("sh -c %q: %v\n%s", got, err, out)
		}
	}
	content, err := os.ReadFile(inbox)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "worker2 ") {
		t.Errorf("inbox = %q, want two lines starting with worker2", content)
	}
}

func TestAppendLineAppends(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	for _, line := range []string{"one", "two"} {
		if err := appendLine(inbox, line); err != nil {
			t.Fatal(err)
		}
	}
	if content, _ := os.ReadFile(inbox); string(content) != "one\ntwo\n" {
		t.Errorf("inbox = %q", content)
	}
}

func TestWorkerArgsOnlyHooksClaudeWorkers(t *testing.T) {
	claude := workerArgs(workerSpec{Kind: "claude", Model: "sonnet"}, "worker1", "true", "", false)
	if !slices.Contains(claude, "--settings") {
		t.Errorf("workerArgs(claude) = %v, want a --settings carrying the Stop hook", claude)
	}
	if !slices.Contains(claude, "--model") || !slices.Contains(claude, "sonnet") {
		t.Errorf("workerArgs(claude) = %v, dropped the model", claude)
	}

	codex := workerArgs(workerSpec{Kind: "codex", Model: "gpt-5"}, "worker1", "true", "", false)
	if slices.Contains(codex, "--settings") {
		t.Errorf("workerArgs(codex) = %v, --settings is Claude Code's own flag and would break the CLI", codex)
	}
}

func TestWorkerArgsPromptIsASystemPromptForClaudeOnly(t *testing.T) {
	claude := workerArgs(workerSpec{Kind: "claude", PromptPath: "/cfg/verifier.md"}, "worker1", "true", "", false)
	i := slices.Index(claude, "--append-system-prompt-file")
	if i < 0 || i+1 >= len(claude) || claude[i+1] != "/cfg/verifier.md" {
		t.Errorf("workerArgs(claude with prompt) = %v, want --append-system-prompt-file /cfg/verifier.md", claude)
	}
	if !slices.Contains(claude, "--settings") {
		t.Errorf("workerArgs(claude with prompt) = %v, lost the Stop hook", claude)
	}

	codex := workerArgs(workerSpec{Kind: "codex", PromptPath: "/cfg/verifier.md"}, "worker1", "true", "", false)
	if slices.Contains(codex, "--append-system-prompt-file") {
		t.Errorf("workerArgs(codex with prompt) = %v, passed a Claude Code flag to another CLI", codex)
	}
}

// Only a claude worker with a stack may run wtm switch without a prompt:
// it drops its own database, and only the worktree it runs in.
func TestWorkerArgsAllowWtmSwitchOnlyWhenAsked(t *testing.T) {
	got := workerArgs(workerSpec{Kind: "claude"}, "worker1", "true", "", true)
	i := slices.Index(got, "--allowedTools")
	if i < 0 || i+1 >= len(got) || got[i+1] != "Bash(wtm switch:*)" {
		t.Errorf("workerArgs(allowSwitch) = %v, want --allowedTools Bash(wtm switch:*)", got)
	}
	if got := workerArgs(workerSpec{Kind: "claude"}, "worker1", "true", "", false); slices.Contains(got, "--allowedTools") {
		t.Errorf("workerArgs(no stack) = %v, want no --allowedTools", got)
	}
	if got := workerArgs(workerSpec{Kind: "codex"}, "worker1", "true", "", true); slices.Contains(got, "--allowedTools") {
		t.Errorf("workerArgs(codex) = %v, --allowedTools is Claude Code's own flag", got)
	}
}

// fakeAcw is an executable that stands for acw in a hook command: it
// records the arguments it got, one per line, in args next to itself, and
// prints a delta whose backtick and $ a quoting slip would run.
func fakeAcw(t *testing.T, dir string) string {
	t.Helper()
	exe := filepath.Join(dir, "acw")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/args\"\nprintf ' (state: “`x` $HOME”)'\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// Run through a real shell from a path a quoting slip trips on: the hook
// is a command line nothing else checks, and a wrong status dir would
// normalize nothing without a word.
func TestTurnEndCommandNormalizesThenPingsWithTheDelta(t *testing.T) {
	dir := filepath.Join(t.TempDir(), `it's $HOME`)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(dir, "inbox")
	cmd := pingCommand(inbox, "master-3f9a1c", "worker2", turnEndCommand(fakeAcw(t, dir), dir, "worker2"))

	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("sh -c %q: %v\n%s", cmd, err, out)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if want := turnEndUse + "\n" + dir + "\nworker2\n"; string(args) != want {
		t.Errorf("acw got args %q, want %q", args, want)
	}
	content, _ := os.ReadFile(inbox)
	if want := "worker2 handed control back (state: “`x` $HOME”). Read"; !strings.HasPrefix(string(content), want) {
		t.Errorf("inbox = %q, want it to start with %q", content, want)
	}
}

// A normalization that fails must never cost the master its ping.
func TestTurnEndCommandPingsEvenWhenNormalizationFails(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	cmd := pingCommand(inbox, "master-3f9a1c", "worker2", turnEndCommand(filepath.Join(dir, "missing-acw"), dir, "worker2"))

	_ = exec.Command("sh", "-c", cmd).Run()
	if content, _ := os.ReadFile(inbox); !strings.HasPrefix(string(content), "worker2 handed control back. Read") {
		t.Errorf("inbox = %q, the ping must go out after a failed normalization", content)
	}
}
