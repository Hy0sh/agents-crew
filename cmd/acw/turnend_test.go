package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
		path := writeStatus(t, `{"state":"`+state+`","blocked_on":"option A ou B ?"}`, time.Now())
		if err := normalizeStatus(path, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := readStatus(t, path)["blocked_on"]; got != "option A ou B ?" {
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
