package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The ping says what moved since the previous one, so a turn the master
// triggered itself costs it no read of the status file.
func TestStatusDeltaTellsWhatMovedSinceThePreviousPing(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "worker1.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if got, ping := statusDelta(dir, "worker1", at); got != "" || !ping {
		t.Errorf("statusDelta(no status) = %q, %v; want nothing said, and a ping", got, ping)
	}
	for _, step := range []struct{ status, want string }{
		{`{"state": "in progress", "updated_at": "1"}`, " (state: “in progress”)"},
		{`{"state": "in progress", "updated_at": "2"}`, " (state unchanged, status rewritten: “in progress”)"},
		{`{"state": "PR\nopen", "updated_at": "3"}`, " (state: “in progress” → “PR open”)"},
	} {
		write(step.status)
		if got, ping := statusDelta(dir, "worker1", at); got != step.want || !ping {
			t.Errorf("statusDelta(%s) = %q, %v; want %q and a ping", step.status, got, ping, step.want)
		}
	}
}

// A turn end with nothing moved stays quiet, unless the master spoke to
// the worker since, or nothing moved for unchangedEvery: a worker looping
// or stuck must not go silent for good.
func TestStatusDeltaKeepsUnchangedTurnsQuiet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker1.json"), []byte(`{"state": "coding", "updated_at": "1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if _, ping := statusDelta(dir, "worker1", start); !ping {
		t.Fatal("first turn end: want a ping")
	}
	if got, ping := statusDelta(dir, "worker1", start.Add(time.Minute)); ping {
		t.Errorf("unchanged turn end = %q, want no ping", got)
	}
	markTold(dir, "worker1")
	if got, ping := statusDelta(dir, "worker1", start.Add(2*time.Minute)); !ping || got != " (status unchanged since its previous ping)" {
		t.Errorf("unchanged turn end after the master spoke = %q, %v; want a ping saying unchanged", got, ping)
	}
	if _, ping := statusDelta(dir, "worker1", start.Add(10*time.Minute)); ping {
		t.Error("the master's word must count once, not for every turn after it")
	}
	got, ping := statusDelta(dir, "worker1", start.Add(2*time.Minute+unchangedEvery))
	if !ping || !strings.Contains(got, "unchanged for 15 min") {
		t.Errorf("turn end after %v unchanged = %q, %v; want a ping saying so", unchangedEvery, got, ping)
	}
	if _, ping := statusDelta(dir, "worker1", start.Add(3*time.Minute+unchangedEvery)); ping {
		t.Error("the long-unchanged ping must start the count over")
	}
}

// A message the master left goes to the worker as the Stop hook's block
// decision, instead of a ping: the turn goes on with it. The turn end
// that follows is its answer, and reaches the master.
func TestTurnEndHandsTheMastersMessageToTheWorker(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	if err := os.WriteFile(filepath.Join(dir, "worker1.json"), []byte(`{"state": "coding", "updated_at": "1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if err := turnEnd(dir, "worker1", inbox, "master-x", at, io.Discard); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(inbox)

	if err := tellWorker(dir, "worker1", "Go for option B."); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := turnEnd(dir, "worker1", inbox, "master-x", at.Add(time.Minute), &out); err != nil {
		t.Fatal(err)
	}
	var decision struct{ Decision, Reason string }
	if err := json.Unmarshal(out.Bytes(), &decision); err != nil || decision.Decision != "block" || !strings.Contains(decision.Reason, "Message from the master:\nGo for option B.") {
		t.Errorf("hook output = %q, want a block decision carrying the message", out.String())
	}
	if _, err := os.Stat(inbox); err == nil {
		t.Error("a turn that goes on with the master's message must not ping")
	}

	out.Reset()
	if err := turnEnd(dir, "worker1", inbox, "master-x", at.Add(2*time.Minute), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("hook output = %q with no message left, want nothing", out.String())
	}
	if content, _ := os.ReadFile(inbox); !strings.HasPrefix(string(content), "worker1 handed control back (status unchanged") {
		t.Errorf("inbox = %q, want the ping answering the master's message", content)
	}
}

// writeStatus writes content as a worker would, with a known mtime.
func writeStatus(t *testing.T, content string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker1.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func readStatus(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(content, &status); err != nil {
		t.Fatalf("status is no longer valid JSON: %v\n%s", err, content)
	}
	return status
}

// The case seen in real use: a local time written with a Z, and a
// blocked_on left behind once the worker was back at work.
func TestNormalizeStatusStampsFromTheFileAndClearsAStaleBlock(t *testing.T) {
	written := time.Date(2026, 9, 25, 14, 5, 12, 0, time.UTC)
	now := written.Add(3 * time.Minute)
	path := writeStatus(t, `{"tache":"some task","state":"in_progress","updated_at":"2026-09-25T16:05:00Z","blocked_on":"waiting for a decision"}`, written)

	if err := normalizeStatus(path, now); err != nil {
		t.Fatal(err)
	}

	got := readStatus(t, path)
	if got["updated_at"] != "2026-09-25T14:05:12Z" {
		t.Errorf("updated_at = %v, want the file's own mtime in UTC", got["updated_at"])
	}
	if got["last_turn_end"] != "2026-09-25T14:08:12Z" {
		t.Errorf("last_turn_end = %v, want now in UTC", got["last_turn_end"])
	}
	if got["blocked_on"] != "" {
		t.Errorf("blocked_on = %v, want it cleared when state is not blocked", got["blocked_on"])
	}
	if got["tache"] != "some task" {
		t.Errorf("tache = %v, other fields must be kept as they are", got["tache"])
	}
}

// The next turn must still read the worker's write time, not ours.
func TestNormalizeStatusKeepsTheWorkersMtime(t *testing.T) {
	written := time.Date(2026, 9, 25, 14, 5, 12, 0, time.UTC)
	path := writeStatus(t, `{"state":"in_progress"}`, written)

	for _, now := range []time.Time{written.Add(time.Minute), written.Add(20 * time.Minute)} {
		if err := normalizeStatus(path, now); err != nil {
			t.Fatal(err)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(written) {
		t.Errorf("mtime = %v, want the worker's %v back after the rewrite", info.ModTime(), written)
	}
	if got := readStatus(t, path)["updated_at"]; got != "2026-09-25T14:05:12Z" {
		t.Errorf("updated_at after two turns = %v, want the worker's write time", got)
	}
}

// state_since is the turn end at which the current state was first seen,
// so a queue of workers in the same state can be ordered from the files.
// The worker rewrites its file whole and drops what it did not write: the
// state acw last saw lives beside it, in workerN.since.
func TestNormalizeStatusStampsStateSinceOnAChange(t *testing.T) {
	first := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
	path := writeStatus(t, `{"state":"demo_ready"}`, first)

	if err := normalizeStatus(path, first); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, path)["state_since"]; got != "2026-09-25T14:00:00Z" {
		t.Fatalf("first turn: state_since = %v, want now", got)
	}

	// Same state, file rewritten whole by the worker without the field.
	if err := os.WriteFile(path, []byte(`{"state":"demo_ready","summary":"waiting"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := normalizeStatus(path, first.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, path)["state_since"]; got != "2026-09-25T14:00:00Z" {
		t.Errorf("same state: state_since = %v, want it kept", got)
	}

	if err := os.WriteFile(path, []byte(`{"state":"demo_step"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := normalizeStatus(path, first.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, path)["state_since"]; got != "2026-09-25T14:30:00Z" {
		t.Errorf("new state: state_since = %v, want now", got)
	}
}

func TestNormalizeStatusKeepsBlockedOnWhileBlocked(t *testing.T) {
	path := writeStatus(t, `{"state":"blocked","blocked_on":"which option?"}`, time.Now())
	if err := normalizeStatus(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, path)["blocked_on"]; got != "which option?" {
		t.Errorf("blocked_on = %v, must survive while state is blocked", got)
	}
}

// state is free text written by the worker, in French as often as not:
// a question must survive whatever word it used for being blocked.
func TestNormalizeStatusKeepsBlockedOnForAnyBlockedWording(t *testing.T) {
	for _, state := range []string{"bloqué", "BLOCKED", "blocked_on_decision", "Bloque"} {
		path := writeStatus(t, `{"state":"`+state+`","blocked_on":"option A or B?"}`, time.Now())
		if err := normalizeStatus(path, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := readStatus(t, path)["blocked_on"]; got != "option A or B?" {
			t.Errorf("state %q: blocked_on = %v, the question must survive", state, got)
		}
	}
}

func TestNormalizeStatusAddsNoBlockedOnKey(t *testing.T) {
	path := writeStatus(t, `{"state":"in_progress"}`, time.Now())
	if err := normalizeStatus(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := readStatus(t, path)["blocked_on"]; ok {
		t.Error("blocked_on appeared; acw only clears it, never adds it")
	}
}

// Whatever the worker left, the hook must not destroy it nor fail loudly:
// it runs at every turn end, and a status the worker is halfway through
// is its business.
func TestNormalizeStatusLeavesAnythingButAnObjectAlone(t *testing.T) {
	for _, content := range []string{`null`, `[1,2]`, `{"state":`, ``} {
		path := writeStatus(t, content, time.Now())
		if err := normalizeStatus(path, time.Now()); err != nil {
			t.Errorf("normalizeStatus(%q) = %v, want nil", content, err)
		}
		if got, _ := os.ReadFile(path); string(got) != content {
			t.Errorf("normalizeStatus(%q) rewrote the file to %q", content, got)
		}
	}
}

// The turn end is marked even before the worker ever wrote a status
// file: the watcher reads it to tell a finished turn from a stuck one.
func TestMarkTurnEnd(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 25, 14, 5, 0, 0, time.UTC)
	if err := markTurnEnd(dir, "worker1", at); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "worker1.turn"))
	if err != nil || !info.ModTime().Equal(at) {
		t.Errorf("worker1.turn = %v, %v; want it stamped at %v", info, err, at)
	}
}

func TestNormalizeStatusWithoutAFileDoesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker1.json")
	if err := normalizeStatus(path, time.Now()); err != nil {
		t.Errorf("normalizeStatus(missing) = %v, want nil", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("normalizeStatus created a status file the worker never wrote")
	}
}
