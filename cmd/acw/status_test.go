package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/herdr"
)

func TestRenderStatusShowsAgesUsageAndTheInbox(t *testing.T) {
	// Local: the inbox time is shown in the user's own clock.
	now := time.Date(2026, 9, 25, 14, 10, 0, 0, time.Local)
	ctx, quota := 23.0, 78.0
	rows := []statusRow{
		{
			Label: "worker1", Agent: "working", State: "in_progress", Task: "some task",
			Branch: "feat/x", BaseBranch: "feat/w", PR: "https://example.test/pr/1",
			Updated: now.Add(-40 * time.Minute), TurnEnd: now.Add(-3 * time.Minute), Activity: now.Add(-2 * time.Minute),
			Context: &ctx, FiveHour: &quota,
		},
		{Label: "worker2", Agent: "idle"},
	}
	got := renderStatus(now, rows, 2, now.Add(-7*time.Minute))
	for _, want := range []string{
		"worker1", "working", "in_progress", "some task",
		"statut il y a 40 min", "tour il y a 3 min", "activité il y a 2 min",
		"ctx 23%", "5h 78%", "feat/x ← feat/w", "https://example.test/pr/1",
		"inbox : 2 messages non lus, le dernier arrivé à 14:03",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderStatus() missing %q in:\n%s", want, got)
		}
	}
	// A worker that has written nothing yet still gets its line.
	var worker2 string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "worker2") {
			worker2 = line
		}
	}
	if !strings.Contains(worker2, "statut -") || !strings.Contains(worker2, "ctx ?%") {
		t.Errorf("worker2 line = %q, want - and ? for what is unknown", worker2)
	}
}

// A worker that failed to start leaves a gap: those after it still show.
func TestScanWorkersSkipsGaps(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker3.json"), []byte(`{"state":"in_progress"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := []herdr.Agent{{Name: "worker1-abc123", Status: "idle"}, {Name: "worker4-abc123", Status: "working"}}
	var labels []string
	for _, r := range scanWorkers(dir, agents, "abc123") {
		labels = append(labels, r.Label)
	}
	if !slices.Equal(labels, []string{"worker1", "worker3", "worker4"}) {
		t.Errorf("scanWorkers() = %v, want worker1, worker3, worker4 past the gap at 2", labels)
	}
}

func TestRenderStatusEmptyInbox(t *testing.T) {
	if got := renderStatus(time.Now(), nil, 0, time.Time{}); !strings.Contains(got, "inbox : vide") {
		t.Errorf("renderStatus() = %q", got)
	}
}
