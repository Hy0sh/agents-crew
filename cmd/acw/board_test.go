package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/board"
)

func boardDay(t *testing.T, repo string, day time.Time) board.Day {
	t.Helper()
	b, err := board.Open(board.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	d, err := b.Board(repo, day, day)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Text as arguments or on stdin, like acw tell; an empty one is refused.
func TestAddDecision(t *testing.T) {
	repo, now := t.TempDir(), time.Now()
	if err := addDecision(repo, "worker2", "SHOP-151", "accounting reconciles per line", []string{"VAT", "per", "line"}, strings.NewReader(""), now); err != nil {
		t.Fatal(err)
	}
	if err := addDecision(repo, "", "", "", nil, strings.NewReader("#418 stays stacked\n"), now); err != nil {
		t.Fatal(err)
	}
	if err := addDecision(repo, "", "", "", nil, strings.NewReader("  \n"), now); err == nil {
		t.Error("an empty decision should be refused")
	}
	d := boardDay(t, repo, now)
	if len(d.Decisions) != 2 || d.Decisions[1].Text != "VAT per line" || d.Decisions[1].Worker != "worker2" || d.Decisions[0].Text != "#418 stays stacked" {
		t.Errorf("decisions = %+v", d.Decisions)
	}
}

func TestDecisionWorkerIsChecked(t *testing.T) {
	cmd := boardCommand()
	cmd.SetArgs([]string{"decision", "--repo", t.TempDir(), "--worker", "bob", "text"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not a worker") {
		t.Errorf("--worker bob = %v, want a refusal", err)
	}
}

// blocked from herdr wins, then the worker's own state while busy, then
// the pool's.
func TestBoardWorkerState(t *testing.T) {
	now := time.Now()
	pw := poolWorker{Index: 1, State: workerBusy, Since: now}
	s := workerStatus{Tache: "SHOP-142 stacked discounts", State: "coding", Summary: "front next", Branch: "feat/x", PRURL: "https://github.com/o/r/pull/418"}
	if got := boardWorker("/r", pw, "blocked", s, now); got.State != "blocked" || got.Subject != s.Tache || got.Summary != "front next" {
		t.Errorf("blocked = %+v", got)
	}
	if got := boardWorker("/r", pw, "working", s, now); got.State != "coding" {
		t.Errorf("busy = %q, want the worker's state", got.State)
	}
	pw.State = workerFree
	if got := boardWorker("/r", pw, "idle", s, now); got.State != "free" || got.Subject != "" {
		t.Errorf("free = %+v, want no subject", got)
	}
}

// Only what moved is written again; UpdatedAt alone is not a move.
func TestChangedWorkers(t *testing.T) {
	last := map[string]board.Worker{}
	w := board.Worker{Worker: "worker1", State: "coding", UpdatedAt: time.Unix(1, 0)}
	if got := changedWorkers(last, []board.Worker{w}); len(got) != 1 {
		t.Fatalf("first = %d", len(got))
	}
	w.UpdatedAt = time.Unix(2, 0)
	if got := changedWorkers(last, []board.Worker{w}); len(got) != 0 {
		t.Errorf("same state = %d, want 0", len(got))
	}
	w.State = "blocked"
	if got := changedWorkers(last, []board.Worker{w}); len(got) != 1 {
		t.Errorf("new state = %d, want 1", len(got))
	}
}

func TestPRFromURL(t *testing.T) {
	now := time.Now()
	p, ok := prFromURL("/r", "https://github.com/o/r/pull/418/", "worker1", now)
	if !ok || p.Number != 418 || p.URL != "https://github.com/o/r/pull/418" || p.Worker != "worker1" {
		t.Errorf("= %+v, %v", p, ok)
	}
	for _, bad := range []string{"", "pending", "https://github.com/o/r/issues/3", "https://github.com/o/r/pull/x"} {
		if _, ok := prFromURL("/r", bad, "worker1", now); ok {
			t.Errorf("%q gave a PR", bad)
		}
	}
}

func TestBoardPR(t *testing.T) {
	now := time.Now()
	pr := prState{Number: 421, URL: "u", Title: "t", Base: "main", Mergeable: "CONFLICTING", CI: "green", LastReview: "APPROVED by alice", OpenThreads: 2}
	got := boardPR("/r", pr, "worker2", "", now)
	if got.Status != "conflicting" || got.CI != "green" || got.Review != "APPROVED by alice · 2 threads open" || got.Worker != "worker2" {
		t.Errorf("= %+v", got)
	}
	if got := boardPR("/r", pr, "", "merged", now); got.Status != "merged" {
		t.Errorf("fate = %q, want merged", got.Status)
	}
	pr.Mergeable, pr.OpenThreads, pr.LastReview = "MERGEABLE", 0, ""
	if got := boardPR("/r", pr, "", "", now); got.Status != "open" || got.Review != "" {
		t.Errorf("plain = %+v", got)
	}
}
