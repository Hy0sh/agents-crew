package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
)

func TestRenderStatusShowsAgesUsageAndTheInbox(t *testing.T) {
	// Local: the inbox time is shown in the user's own clock.
	now := time.Date(2026, 9, 25, 14, 10, 0, 0, time.Local)
	ctx, quota := 23.0, 78.0
	rows := []statusRow{
		{
			Label: "worker1", Name: "worker1-3f9a1c", Agent: "working", State: "in_progress", StateSince: now.Add(-25 * time.Minute), Task: "some task",
			Branch: "feat/x", BaseBranch: "feat/w", PR: "https://example.test/pr/1",
			Updated: now.Add(-40 * time.Minute), TurnEnd: now.Add(-3 * time.Minute), Activity: now.Add(-2 * time.Minute),
			Context: &ctx, FiveHour: &quota, Busy: "1 shell, 3 monitors",
		},
		{Label: "worker2", Agent: "idle"},
	}
	got := renderStatus(now, rows, 2, now.Add(-7*time.Minute), true)
	if strings.Contains(got, "watcher") {
		t.Errorf("a running watcher is not worth a line:\n%s", got)
	}
	for _, want := range []string{
		"worker1 (herdr worker1-3f9a1c)", "working", "in_progress since 25 min ago, waiting on 1 shell, 3 monitors", "some task",
		"status 40 min ago", "turn 3 min ago", "activity 2 min ago",
		"ctx 23%", "5h 78%", "feat/x ← feat/w", "https://example.test/pr/1",
		"inbox: 2 unread messages, the last one arrived at 14:03",
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
	if !strings.Contains(worker2, "status -") || !strings.Contains(worker2, "ctx ?%") {
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
	if got := renderStatus(time.Now(), nil, 0, time.Time{}, true); !strings.Contains(got, "inbox: empty") {
		t.Errorf("renderStatus() = %q", got)
	}
}

// A watcher that died dispatches nothing more, and nothing else says so.
func TestStatusSaysWhenTheWatcherIsGone(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if watcherRunning(repo) {
		t.Error("no watcher ever ran, yet it reads as running")
	}
	lock, err := os.OpenFile(names.WatchLock(repo), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if !watcherRunning(repo) {
		t.Error("the watcher holds its lock, yet it reads as gone")
	}
	lock.Close()
	if watcherRunning(repo) {
		t.Error("the watcher let go of its lock, yet it reads as running")
	}
	if got := renderStatus(time.Now(), nil, 0, time.Time{}, false); !strings.Contains(got, "watcher: not running") {
		t.Errorf("renderStatus() = %q, want the dead watcher named", got)
	}
}
