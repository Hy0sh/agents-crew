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
