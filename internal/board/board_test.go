package board

import (
	"database/sql"
	"strings"
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
func TestUpsertPRKeepsWorker(t *testing.T) {
	b := open(t)
	check := func(worker, status, wantWorker, wantStatus string) {
		t.Helper()
		b.UpsertPR(PR{Repo: "/r", Number: 418, Worker: worker, Status: status, UpdatedAt: noon})
		d, _ := b.Board("/r", noon, noon)
		if len(d.PRs) != 1 || d.PRs[0].Worker != wantWorker || d.PRs[0].Status != wantStatus {
			t.Errorf("after (%q, %q): %+v", worker, status, d.PRs)
		}
	}
	check("worker1", "open", "worker1", "open")
	check("", "merged", "worker1", "merged")
	check("worker2", "merged", "worker2", "merged")
}

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

// The repo active latest comes first, even when another is alphabetically
// first on the same day.
func TestReposOrderedByLatestActivity(t *testing.T) {
	b := open(t)
	b.AddHandled(Handled{Repo: "/a", At: noon})
	b.AddHandled(Handled{Repo: "/b", At: noon.Add(time.Hour)})
	got, err := b.Repos()
	if err != nil || len(got) != 2 || got[0].Repo != "/b" {
		t.Errorf("Repos() = %+v, %v", got, err)
	}
}

func TestKeepWorkers(t *testing.T) {
	b := open(t)
	for _, w := range []Worker{{Repo: "/r", Worker: "worker1"}, {Repo: "/r", Worker: "worker2"}, {Repo: "/o", Worker: "worker1"}} {
		b.UpsertWorker(w)
	}
	// Workers show on today's board only: noon is made today.
	b.KeepWorkers("/r", []string{"worker2"})
	if d, _ := b.Board("/r", noon, noon); len(d.Workers) != 1 || d.Workers[0].Worker != "worker2" {
		t.Errorf("kept = %+v", d.Workers)
	}
	b.KeepWorkers("/r", nil)
	if d, _ := b.Board("/r", noon, noon); len(d.Workers) != 0 {
		t.Errorf("nil keep left %+v", d.Workers)
	}
	if d, _ := b.Board("/o", noon, noon); len(d.Workers) != 1 {
		t.Errorf("other repo = %+v", d.Workers)
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

// A base written by v0.16.0 (version 1, no blocked_on) is migrated in
// place: its rows stay, the new column reads back.
func TestMigratesVersion1Base(t *testing.T) {
	path := t.TempDir() + "/board.db"
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE meta (version INTEGER NOT NULL); INSERT INTO meta VALUES (1);
CREATE TABLE workers (repo TEXT, worker TEXT, state TEXT, subject TEXT, branch TEXT, pr_url TEXT, summary TEXT, since INTEGER, updated_at INTEGER, PRIMARY KEY (repo, worker));
INSERT INTO workers VALUES ('/r', 'worker1', 'coding', 'SHOP-142', '', '', '', 0, 0);`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	for range 2 { // the second open finds it done
		b, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		b.Close()
	}
	b, _ := Open(path)
	defer b.Close()
	if d, _ := b.Board("/r", noon, noon); len(d.Workers) != 1 || d.Workers[0].Subject != "SHOP-142" {
		t.Fatalf("old row = %+v", d.Workers)
	}
	b.UpsertWorker(Worker{Repo: "/r", Worker: "worker1", State: "blocked", BlockedOn: "VAT rounding"})
	if d, _ := b.Board("/r", noon, noon); d.Workers[0].BlockedOn != "VAT rounding" {
		t.Errorf("blocked_on = %q", d.Workers[0].BlockedOn)
	}
}

// Seen is the repo's last beat, with its queue and inbox, today only, nil
// before any.
func TestBeat(t *testing.T) {
	b := open(t)
	if d, _ := b.Board("/r", noon, noon); d.Seen != nil {
		t.Errorf("seen before any beat = %v", d.Seen)
	}
	b.Beat("/r", noon, 0, 0)
	b.Beat("/r", noon.Add(30*time.Second), 2, 5)
	b.Beat("/other", noon.Add(time.Hour), 0, 0)
	if d, _ := b.Board("/r", noon, noon); d.Seen == nil || !d.Seen.Equal(noon.Add(30*time.Second)) || d.Queue != 2 || d.Inbox != 5 {
		t.Errorf("seen = %v, queue %d, inbox %d", d.Seen, d.Queue, d.Inbox)
	}
	if d, _ := b.Board("/r", noon.AddDate(0, 0, -1), noon); d.Seen != nil {
		t.Errorf("past day seen = %v", d.Seen)
	}
}

// A parked decision gets a number, waits until closed once; a second
// answer is refused with the first.
func TestParked(t *testing.T) {
	b := open(t)
	id, err := b.Park(Parked{Repo: "/r", Ticket: "SHOP-7", On: "client", Text: "Which address?\n1. old 2. new", CreatedAt: noon})
	if err != nil || id == 0 {
		t.Fatalf("Park = %d, %v", id, err)
	}
	b.Park(Parked{Repo: "/other", On: "me", Text: "elsewhere", CreatedAt: noon})
	if got, _ := b.OpenParked("/r"); len(got) != 1 || got[0].ID != id || got[0].Ticket != "SHOP-7" {
		t.Errorf("OpenParked = %+v", got)
	}
	if d, _ := b.Board("/r", noon, noon); len(d.Parked) != 1 {
		t.Errorf("board parked = %+v", d.Parked)
	}
	p, err := b.CloseParked(id, "the old one", noon.Add(time.Hour))
	if err != nil || p.Answer != "the old one" || p.ClosedAt == nil {
		t.Fatalf("CloseParked = %+v, %v", p, err)
	}
	if _, err := b.CloseParked(id, "the new one", noon.Add(2*time.Hour)); err == nil || !strings.Contains(err.Error(), "the old one") {
		t.Errorf("second close = %v, want a refusal quoting the first answer", err)
	}
	if got, _ := b.OpenParked("/r"); len(got) != 0 {
		t.Errorf("open after close = %+v", got)
	}
	if _, err := b.GetParked(999); err == nil {
		t.Error("GetParked(999) found something")
	}
}

// A since stays the first one seen while the condition holds, and goes
// when it stops; head and worker survive a row that lacks them.
func TestUpsertPRKeepsTheFirstSince(t *testing.T) {
	b := open(t)
	b.UpsertPR(PR{Repo: "/r", Number: 1, Head: "feat/x", Worker: "worker1", Status: "open", SinceReady: noon, UpdatedAt: noon})
	b.UpsertPR(PR{Repo: "/r", Number: 1, Status: "open", SinceReady: noon.Add(time.Minute), UpdatedAt: noon.Add(time.Minute)})
	d, _ := b.Board("/r", noon, noon)
	if p := d.PRs[0]; !p.SinceReady.Equal(noon) || p.Head != "feat/x" || p.Worker != "worker1" {
		t.Errorf("after a second ready poll = %+v", p)
	}
	b.UpsertPR(PR{Repo: "/r", Number: 1, Status: "open", UpdatedAt: noon.Add(2 * time.Minute)})
	if d, _ := b.Board("/r", noon, noon); !d.PRs[0].SinceReady.IsZero() {
		t.Errorf("no longer ready, since = %v", d.PRs[0].SinceReady)
	}
}

func TestMarks(t *testing.T) {
	b := open(t)
	b.Mark("/r", "worker:worker1:plan_ready:1", noon.Add(-markWindow-time.Hour))
	b.Mark("/r", "worker:worker1:plan_ready:2", noon)
	if d, _ := b.Board("/r", noon, noon); len(d.Marks) != 1 || !d.Marks["worker:worker1:plan_ready:2"].Equal(noon) {
		t.Errorf("marks = %v, want the window's only", d.Marks)
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

// A parked document keeps its kind and path; refused, it stays open,
// marked, until closed; a closed one can't be refused.
func TestParkedDocumentRefused(t *testing.T) {
	b := open(t)
	id, _ := b.Park(Parked{Repo: "/r", Ticket: "SHOP-9", Worker: "worker2", On: "me", Text: "Approve the plan", Kind: "plan", DocPath: "/plans/p.md", CreatedAt: noon})
	p, err := b.RefuseParked(id)
	if err != nil || !p.Refused || p.ClosedAt != nil || p.Kind != "plan" || p.DocPath != "/plans/p.md" {
		t.Fatalf("RefuseParked = %+v, %v", p, err)
	}
	if got, _ := b.OpenParked("/r"); len(got) != 1 || !got[0].Refused {
		t.Errorf("open after refuse = %+v", got)
	}
	if p, _ := b.CloseParked(id, "reworked plan agreed", noon); p.Refused {
		t.Errorf("closed still refused: %+v", p)
	}
	if _, err := b.RefuseParked(id); err == nil {
		t.Error("refusing a closed one went through")
	}
}

// The interrupted tasks of a stop replace the previous ones, except an
// empty snapshot (a second stop after a failed one), which keeps them.
func TestInterrupted(t *testing.T) {
	b := open(t)
	if _, _, ok, err := b.Interrupted("/r"); ok || err != nil {
		t.Fatalf("before any = %v, %v", ok, err)
	}
	b.SaveInterrupted("/r", noon, []byte(`[1]`), false)
	b.SaveInterrupted("/r", noon.Add(time.Hour), []byte(`[2]`), false)
	b.SaveInterrupted("/r", noon.Add(2*time.Hour), []byte(`[]`), true)
	tasks, at, ok, err := b.Interrupted("/r")
	if err != nil || !ok || string(tasks) != `[2]` || !at.Equal(noon.Add(time.Hour)) {
		t.Fatalf("Interrupted = %s, %v, %v, %v", tasks, at, ok, err)
	}
	b.DeleteInterrupted("/r")
	if _, _, ok, _ := b.Interrupted("/r"); ok {
		t.Error("still there after delete")
	}
}

// Each poll's drafts replace the repo's draft rows; a PR no longer a
// draft (open now) is left alone.
func TestSetDrafts(t *testing.T) {
	b := open(t)
	b.SetDrafts("/r", []PR{{Repo: "/r", Number: 1, Title: "one", UpdatedAt: noon}, {Repo: "/r", Number: 2, UpdatedAt: noon}})
	b.UpsertPR(PR{Repo: "/r", Number: 2, Status: "open", UpdatedAt: noon})
	b.SetDrafts("/r", []PR{{Repo: "/r", Number: 3, UpdatedAt: noon}})
	d, _ := b.Board("/r", noon, noon)
	got := map[int]string{}
	for _, p := range d.PRs {
		got[p.Number] = p.Status
	}
	if len(got) != 2 || got[2] != "open" || got[3] != "draft" {
		t.Errorf("prs = %v, want 2 open and 3 draft", got)
	}
}
