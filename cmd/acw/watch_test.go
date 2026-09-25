package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)

func kinds(events []watchEvent) []eventKind {
	var out []eventKind
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestWatcherReportsEachEntryIntoBlockedOnce(t *testing.T) {
	w := newWatcher(30 * time.Minute)
	v := workerView{Label: "worker1", Hooked: true, Status: "blocked", Activity: t0}
	if got := kinds(w.observe(t0, []workerView{v})); !slices.Equal(got, []eventKind{eventBlocked}) {
		t.Errorf("first sight of a blocked worker = %v, want one blocked event", got)
	}
	if got := w.observe(t0.Add(5*time.Second), []workerView{v}); len(got) != 0 {
		t.Errorf("still blocked = %v, want nothing more", got)
	}
	v.Status = "working"
	w.observe(t0.Add(10*time.Second), []workerView{v})
	v.Status = "blocked"
	if got := kinds(w.observe(t0.Add(15*time.Second), []workerView{v})); !slices.Equal(got, []eventKind{eventBlocked}) {
		t.Errorf("blocked again = %v, want a new event", got)
	}
}

func TestWatcherReportsSilenceOncePerEpisode(t *testing.T) {
	w := newWatcher(30 * time.Minute)
	v := workerView{Label: "worker1", Hooked: true, Status: "working", Activity: t0}
	w.observe(t0, []workerView{v})
	if got := w.observe(t0.Add(29*time.Minute), []workerView{v}); len(got) != 0 {
		t.Errorf("29 min = %v, want nothing yet", got)
	}
	got := w.observe(t0.Add(31*time.Minute), []workerView{v})
	if len(got) != 1 || got[0].Kind != eventSilent || got[0].For != 31*time.Minute {
		t.Errorf("31 min = %+v, want one silent event for 31m", got)
	}
	if got := w.observe(t0.Add(40*time.Minute), []workerView{v}); len(got) != 0 {
		t.Errorf("40 min = %v, want no repeat in the same episode", got)
	}
	v.Activity = t0.Add(41 * time.Minute)
	w.observe(t0.Add(41*time.Minute), []workerView{v})
	if got := kinds(w.observe(t0.Add(72*time.Minute), []workerView{v})); !slices.Equal(got, []eventKind{eventSilent}) {
		t.Errorf("new episode = %v, want a new silent event", got)
	}
}

// The case from real use: a 37-minute turn of real coding.
func TestWatcherKeepsQuietWhileTheWorktreeMoves(t *testing.T) {
	w := newWatcher(30 * time.Minute)
	v := workerView{Label: "worker1", Hooked: true, Status: "working", Activity: t0}
	for m := 0; m <= 37; m++ {
		v.Activity = t0.Add(time.Duration(m/2*2) * time.Minute)
		if got := w.observe(t0.Add(time.Duration(m)*time.Minute), []workerView{v}); len(got) != 0 {
			t.Fatalf("minute %d = %v, want no silence while files keep changing", m, got)
		}
	}
}

func TestWatcherReportsTurnEndOnlyForWorkersWithoutTheHook(t *testing.T) {
	w := newWatcher(30 * time.Minute)
	hooked := workerView{Label: "worker1", Hooked: true, Status: "working", Activity: t0}
	bare := workerView{Label: "worker2", Status: "working", Activity: t0}
	w.observe(t0, []workerView{hooked, bare})
	hooked.Status, bare.Status = "idle", "done"
	got := w.observe(t0.Add(5*time.Second), []workerView{hooked, bare})
	if len(got) != 1 || got[0].Label != "worker2" || got[0].Kind != eventTurnEnd {
		t.Errorf("turn ends = %+v, want one for worker2 only (worker1's Stop hook already pings)", got)
	}
}

// A watcher left behind by an acw stop that found no master, or by a
// stop and start within one poll, must see the status dir is no longer
// its own, instead of messaging the next swarm twice.
func TestOwnsStatusDir(t *testing.T) {
	dir := t.TempDir()
	if ownsStatusDir(dir, "20260925140000") {
		t.Error("owns a status dir with no stamp in it")
	}
	if err := os.WriteFile(filepath.Join(dir, "stamp"), []byte("20260925140000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ownsStatusDir(dir, "20260925140000") {
		t.Error("does not own the status dir its own run stamped")
	}
	if ownsStatusDir(dir, "20260925090000") {
		t.Error("owns a status dir stamped by a later run")
	}
}

func TestPingMessageNamesTheWorker(t *testing.T) {
	if got := pingMessage("worker2"); !strings.HasPrefix(got, "worker2 a rendu la main.") {
		t.Errorf("pingMessage() = %q", got)
	}
}

func TestBlockedMessage(t *testing.T) {
	pane := strings.Repeat("old line\n", 30) + "\n\nBash command\n  rm -rf /tmp/x\nDo you want to proceed?\n"
	got := blockedMessage("worker2", 1, pane)
	if !strings.HasPrefix(got, "worker2 est bloqué") || !strings.Contains(got, "rm -rf /tmp/x") {
		t.Errorf("blockedMessage() = %q, want the worker named and the pending command", got)
	}
	if n := strings.Count(got, "old line"); n > 12 {
		t.Errorf("blockedMessage() kept %d old lines, want the pane cut to its last lines", n)
	}
	if got := blockedMessage("worker2", 2, pane); !strings.HasPrefix(got, "2e blocage") || !strings.Contains(got, "worker2 est bloqué") {
		t.Errorf("blockedMessage(2nd) = %q, want the repeat called out first", got)
	}
	// Nothing resets the count yet: it must not claim to be per task.
	if got := blockedMessage("worker2", 2, pane); !strings.Contains(got, "depuis le démarrage du swarm") {
		t.Errorf("blockedMessage(2nd) = %q, want the count said to run since the swarm started", got)
	}
}

func TestSilentMessage(t *testing.T) {
	if got := silentMessage("worker1", 31*time.Minute+20*time.Second); !strings.HasPrefix(got, "worker1 travaille depuis 31 min") {
		t.Errorf("silentMessage() = %q", got)
	}
}

func TestWatcherRemindsAnUnreadInboxOnce(t *testing.T) {
	w := newWatcher(30 * time.Minute)
	if w.remindInbox(t0, true) || w.remindInbox(t0.Add(4*time.Minute), true) {
		t.Error("reminded before 5 minutes of unread messages")
	}
	if !w.remindInbox(t0.Add(6*time.Minute), true) {
		t.Error("no reminder after 6 minutes of unread messages")
	}
	if w.remindInbox(t0.Add(10*time.Minute), true) {
		t.Error("reminded twice for the same unread messages")
	}
	w.remindInbox(t0.Add(11*time.Minute), false)
	if w.remindInbox(t0.Add(12*time.Minute), true) {
		t.Error("a new batch was reminded after only 1 minute")
	}
}
