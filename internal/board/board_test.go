package board

import (
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *DB {
	t.Helper()
	b, err := Open(t.TempDir() + "/board.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

var noon = time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)

func TestPathHonoursXDGState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got := Path(); got != "/state/acw/board.db" {
		t.Errorf("Path() = %q", got)
	}
}

// A worker row is replaced, not duplicated, and a closed worker's goes.
func TestUpsertAndDeleteWorker(t *testing.T) {
	b := open(t)
	w := Worker{Repo: "/r", Worker: "worker1", State: "busy", Subject: "SHOP-142", Since: noon, UpdatedAt: noon}
	b.UpsertWorker(w)
	w.State = "blocked"
	if err := b.UpsertWorker(w); err != nil {
		t.Fatal(err)
	}
	d, _ := b.Board("/r", noon, noon)
	if len(d.Workers) != 1 || d.Workers[0].State != "blocked" {
		t.Fatalf("workers = %+v", d.Workers)
	}
	b.DeleteWorker("/r", "worker1")
	if d, _ := b.Board("/r", noon, noon); len(d.Workers) != 0 {
		t.Errorf("workers after delete = %+v", d.Workers)
	}
}

// Workers and PRs are the current state: a past day doesn't show them.
func TestPastDayIsNotLive(t *testing.T) {
	b := open(t)
	b.UpsertWorker(Worker{Repo: "/r", Worker: "worker1", UpdatedAt: noon})
	b.UpsertPR(PR{Repo: "/r", Number: 418, UpdatedAt: noon})
	d, err := b.Board("/r", noon.AddDate(0, 0, -1), noon)
	if err != nil || d.Live || len(d.Workers)+len(d.PRs) != 0 {
		t.Errorf("past day = %+v, %v", d, err)
	}
	if d, _ := b.Board("/r", noon, noon); !d.Live || len(d.PRs) != 1 {
		t.Errorf("today = %+v", d)
	}
}

func TestDayBoundsAreLocal(t *testing.T) {
	b := open(t)
	late := time.Date(2026, 10, 6, 23, 59, 0, 0, time.Local)
	b.AddDecision(Decision{Repo: "/r", At: late, Text: "late"})
	b.AddDecision(Decision{Repo: "/r", At: late.Add(2 * time.Minute), Text: "next day"})
	d, _ := b.Board("/r", noon, noon)
	if len(d.Decisions) != 1 || d.Decisions[0].Text != "late" {
		t.Errorf("decisions of the 6th = %+v", d.Decisions)
	}
}

// Latest first, other repos left out.
func TestBoardOrdersAndFiltersByRepo(t *testing.T) {
	b := open(t)
	b.AddHandled(Handled{Repo: "/r", At: noon, Subject: "first"})
	b.AddHandled(Handled{Repo: "/r", At: noon.Add(time.Hour), Subject: "second"})
	b.AddHandled(Handled{Repo: "/other", At: noon, Subject: "elsewhere"})
	d, _ := b.Board("/r", noon, noon)
	if len(d.Handled) != 2 || d.Handled[0].Subject != "second" {
		t.Errorf("handled = %+v", d.Handled)
	}
}

func TestRepos(t *testing.T) {
	b := open(t)
	b.AddDecision(Decision{Repo: "/old", At: noon.AddDate(0, 0, -2)})
	b.AddDecision(Decision{Repo: "/r", At: noon.AddDate(0, 0, -1)})
	b.AddHandled(Handled{Repo: "/r", At: noon})
	got, err := b.Repos()
	if err != nil || len(got) != 2 || got[0].Repo != "/r" || len(got[0].Days) != 2 || got[0].Days[0] != "2026-10-06" {
		t.Errorf("Repos() = %+v, %v", got, err)
	}
}

func TestConcurrentWrites(t *testing.T) {
	path := t.TempDir() + "/board.db"
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := Open(path)
			if err != nil {
				t.Error(err)
				return
			}
			defer b.Close()
			if err := b.AddDecision(Decision{Repo: "/r", At: noon, Text: string(rune('a' + i))}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if d, _ := b.Board("/r", noon, noon); len(d.Decisions) != 8 {
		t.Errorf("decisions = %d, want 8", len(d.Decisions))
	}
}

// Same At timestamp, different inserts: newest insert (larger rowid) comes first.
func TestSameAtTimeOrderedByRowid(t *testing.T) {
	b := open(t)
	b.AddDecision(Decision{Repo: "/r", At: noon, Text: "first"})
	b.AddDecision(Decision{Repo: "/r", At: noon, Text: "second"})
	d, _ := b.Board("/r", noon, noon)
	if len(d.Decisions) != 2 || d.Decisions[0].Text != "second" {
		t.Errorf("decisions = %+v, want second first", d.Decisions)
	}
}
