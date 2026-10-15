package claudecode

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// acw's hooks, rules and board launch go in next to what the user has,
// and acw stop takes off exactly them: the user's settings come back as
// they were, and files acw made alone are gone.
func TestDesktopInstallUninstall(t *testing.T) {
	dir, status := t.TempDir(), t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := `{"permissions":{"allow":["Bash(git status)","Bash(/bin/acw status:*)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"}]}]}}`
	if err := os.WriteFile(settings, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Desktop{Dir: dir, Session: "s1", Hook: "/bin/acw __desktop-hook " + status, Board: "http://127.0.0.1:7461", InboxNext: "/bin/acw __inbox-next /i"}
	if err := InstallDesktop(status, d, []string{"Bash(/bin/acw status:*)", "Bash(/bin/acw queue:*)"}, "kick"); err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, settings)
	if n := len(got["hooks"].(map[string]any)); n != 3 {
		t.Errorf("hooks = %v, want Stop, SessionStart and PreToolUse", got["hooks"])
	}
	if allow := got["permissions"].(map[string]any)["allow"].([]any); len(allow) != 3 {
		t.Errorf("allow = %v, want the user's two and acw's queue rule once", allow)
	}
	launch := readJSON(t, filepath.Join(dir, ".claude", "launch.json"))
	board := launch["configurations"].([]any)[0].(map[string]any)
	if board["name"] != BoardLaunch || board["url"] != d.Board || board["port"] != float64(7461) {
		t.Errorf("launch = %v, want the board on its port", launch)
	}

	if err := UninstallDesktop(status); err != nil {
		t.Fatal(err)
	}
	var want, back map[string]any
	_ = json.Unmarshal([]byte(mine), &want)
	back = readJSON(t, settings)
	if a, b := mustJSON(want), mustJSON(back); a != b {
		t.Errorf("settings after stop = %s, want the user's back: %s", b, a)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "launch.json")); !os.IsNotExist(err) {
		t.Errorf("launch.json made by acw alone is still there: %v", err)
	}
}

// The hooks answer the master's session only: another session of the
// folder neither gets the brief nor has Edit refused. The kickoff comes
// once.
func TestDesktopHookOnlyForTheMaster(t *testing.T) {
	status := t.TempDir()
	d := Desktop{Dir: t.TempDir(), Session: "master-id", Hook: "x", InboxNext: "acw __inbox-next /i", Board: "http://127.0.0.1:7461"}
	if err := InstallDesktop(status, d, nil, "KICKOFF"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := DesktopHook("pre-tool", status, strings.NewReader(`{"session_id":"someone-else"}`), &out); err != nil || out.Len() != 0 {
		t.Errorf("pre-tool for another session = %q, %v; want nothing", out.String(), err)
	}
	if err := DesktopHook("pre-tool", status, strings.NewReader(`{"session_id":"master-id"}`), &out); err != nil || !strings.Contains(out.String(), `"deny"`) {
		t.Errorf("pre-tool for the master = %q, %v; want a deny", out.String(), err)
	}
	for i, want := range []bool{true, false} {
		out.Reset()
		if err := DesktopHook("session-start", status, strings.NewReader(`{"session_id":"master-id","source":"startup"}`), &out); err != nil {
			t.Fatal(err)
		}
		s := out.String()
		if !strings.Contains(s, "master.system.md") || !strings.Contains(s, "__inbox-next") || !strings.Contains(s, BoardLaunch) {
			t.Errorf("start %d = %s; want the brief, the inbox watch and the board", i, s)
		}
		if strings.Contains(s, "KICKOFF") != want {
			t.Errorf("start %d kickoff present = %v, want %v", i, !want, want)
		}
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(content, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
