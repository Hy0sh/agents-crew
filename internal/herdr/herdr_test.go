package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHerdr puts a herdr first on PATH that answers agent start with a
// startup timeout the first time, process-info with only the pane's shell
// in the foreground (shellOnly) or claude, and logs every call.
func fakeHerdr(t *testing.T, shellOnly bool) (calls string) {
	t.Helper()
	bin := t.TempDir()
	calls = filepath.Join(bin, "calls")
	fg := "4242"
	if !shellOnly {
		fg = "4343"
	}
	script := `#!/bin/sh
echo "$@" >> ` + calls + `
case "$1 $2" in
"agent start")
	if [ ! -f ` + bin + `/started ]; then
		touch ` + bin + `/started
		echo '{"error":{"code":"timeout","message":"timed out waiting for agent startup"}}' >&2; exit 1
	fi ;;
"pane process-info") echo '{"result":{"process_info":{"shell_pid":4242,"foreground_process_group_id":` + fg + `}}}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// A start typed into a pane whose shell was still starting is swallowed,
// and the shell sits there: herdr times out with claude never run. With
// only the shell in the foreground, Ctrl-C frees it and the start is
// tried again once.
func TestAgentStartRetriesWhenTheShellSwallowedIt(t *testing.T) {
	calls := fakeHerdr(t, true)
	if err := AgentStart("master-abc123", "claude", "w1:p1", "--model", "opus"); err != nil {
		t.Fatalf("AgentStart() = %v, want the second try to succeed", err)
	}
	got, _ := os.ReadFile(calls)
	if !strings.Contains(string(got), "pane send-keys w1:p1 C-c") || strings.Count(string(got), "agent start") != 2 {
		t.Errorf("herdr calls =\n%s\nwant Ctrl-C then a second agent start", got)
	}
}

// The start waits for the new pane's shell to draw something before typing
// into it, so the swallowed start above is the exception.
func TestAgentStartWaitsForTheShellFirst(t *testing.T) {
	calls := fakeHerdr(t, true)
	_ = AgentStart("master-abc123", "claude", "w1:p1")
	got, _ := os.ReadFile(calls)
	if !strings.HasPrefix(string(got), "pane wait-output w1:p1 ") {
		t.Errorf("herdr calls =\n%s\nwant a pane wait-output before the agent start", got)
	}
}

// A timeout with claude in the foreground is a slow start, not a lost
// one: no Ctrl-C, which would kill it.
func TestAgentStartLeavesASlowAgentAlone(t *testing.T) {
	calls := fakeHerdr(t, false)
	if err := AgentStart("master-abc123", "claude", "w1:p1"); err == nil {
		t.Fatal("AgentStart() = nil, want the timeout")
	}
	if got, _ := os.ReadFile(calls); strings.Contains(string(got), "send-keys") {
		t.Errorf("herdr calls =\n%s\nwant no Ctrl-C", got)
	}
}

// A master started in another folder (master-dir) has another cwd: it is
// found by its name, which already carries its repo's hash.
func TestFindAgentByName(t *testing.T) {
	agents := []Agent{
		{Name: "master-other1", WorkspaceID: "w1"},
		{Name: "master-abc123", WorkspaceID: "w2"},
	}
	a, ok := FindAgent(agents, "master-abc123")
	if !ok || a.WorkspaceID != "w2" {
		t.Errorf("FindAgent() = %+v, %v; want the w2 master", a, ok)
	}
	if _, ok := FindAgent(agents, "master-zzz999"); ok {
		t.Error("FindAgent() found an agent that isn't there")
	}
}
