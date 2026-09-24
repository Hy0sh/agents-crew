package herdr

import "testing"

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
