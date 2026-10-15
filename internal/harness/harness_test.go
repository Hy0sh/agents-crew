package harness

import (
	"strings"
	"testing"
)

// claude has every capability, another known kind none, and a kind acw
// doesn't know is refused with the list of those it does.
func TestLookup(t *testing.T) {
	c, err := Lookup("claude")
	if err != nil || !c.SystemPrompt || !c.Settings || !c.ResetsContext || !c.InboxMonitor || !c.ReadsClaudeMD || !c.ShowsUntrackedDiffs {
		t.Errorf("claude = %+v, %v; want every capability", c, err)
	}
	if g, err := Lookup("codex"); err != nil || g != (Agent{Kind: "codex"}) {
		t.Errorf("codex = %+v, %v; want the generic agent", g, err)
	}
	_, err = Lookup("claud")
	if err == nil || !strings.Contains(err.Error(), "claude, pi, codex") {
		t.Errorf("Lookup(claud) err = %v, want the known kinds listed", err)
	}
	if o := Of("claud"); o != (Agent{Kind: "claud"}) {
		t.Errorf("Of(claud) = %+v, want the generic agent", o)
	}
}

// Claude Desktop runs Claude Code only, master and workers; any kind runs
// in a herdr pane; an unknown app is refused.
func TestCheckApp(t *testing.T) {
	if err := CheckApp("", "codex", []string{"gemini"}); err != nil {
		t.Errorf("herdr pane with codex = %v", err)
	}
	if err := CheckApp(DesktopApp, "claude", []string{"claude"}); err != nil {
		t.Errorf("desktop with claude = %v", err)
	}
	if err := CheckApp(DesktopApp, "claude", []string{"claude", "codex"}); err == nil || !strings.Contains(err.Error(), "codex") {
		t.Errorf("desktop with a codex worker = %v, want it refused, naming codex", err)
	}
	if err := CheckApp("cursor-app", "claude", nil); err == nil {
		t.Error("an unknown master-app was accepted")
	}
}
