package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/board"
	"github.com/Hy0sh/agents-crew/internal/names"
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
	if err := addDecision(board.Decision{Repo: repo, At: now, Worker: "worker2", Subject: "SHOP-151", Why: "accounting reconciles per line"}, []string{"VAT", "per", "line"}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if err := addDecision(board.Decision{Repo: repo, At: now}, nil, strings.NewReader("#418 stays stacked\n")); err != nil {
		t.Fatal(err)
	}
	if err := addDecision(board.Decision{Repo: repo, At: now}, nil, strings.NewReader("  \n")); err == nil {
		t.Error("an empty decision should be refused")
	}
	d := boardDay(t, repo, now)
	if len(d.Decisions) != 2 || d.Decisions[1].Text != "VAT per line" || d.Decisions[1].Worker != "worker2" || d.Decisions[0].Text != "#418 stays stacked" {
		t.Errorf("decisions = %+v", d.Decisions)
	}
}

func TestDecisionWorkerIsChecked(t *testing.T) {
	cmd := boardCommand()
	cmd.SetArgs([]string{"decision", "--repo", testSwarm(t, 2), "--worker", "bob", "text"})
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
	// A *_ready waits on the user once the turn is over only.
	s.State = "verdict_ready"
	if got := boardWorker("/r", pw, "working", s, now); got.State != "working" {
		t.Errorf("ready while working = %q, want working", got.State)
	}
	if got := boardWorker("/r", pw, "idle", s, now); got.State != "verdict_ready" {
		t.Errorf("ready once idle = %q", got.State)
	}
	pw.State = workerFree
	if got := boardWorker("/r", pw, "idle", s, now); got.State != "free" || got.Subject != "" {
		t.Errorf("free = %+v, want no subject", got)
	}
}

// The worker's own state dates from state_since, and carries blocked_on.
func TestBoardWorkerSinceAndBlockedOn(t *testing.T) {
	now := time.Now()
	pw := poolWorker{Index: 1, State: workerBusy, Since: now.Add(-time.Hour)}
	s := workerStatus{State: "blocked", BlockedOn: "VAT rounding", StateSince: now.Add(-5 * time.Minute).UTC().Format(time.RFC3339)}
	got := boardWorker("/r", pw, "idle", s, now)
	if got.BlockedOn != "VAT rounding" || now.Sub(got.Since).Round(time.Minute) != 5*time.Minute {
		t.Errorf("= %+v", got)
	}
	s.StateSince = ""
	if got := boardWorker("/r", pw, "idle", s, now); !got.Since.Equal(pw.Since) {
		t.Errorf("without state_since = %v, want the pool's", got.Since)
	}
}

// herdr's blocked dates from the first poll that saw it, not from the task.
func TestChangedWorkersDatesBlocked(t *testing.T) {
	last := map[string]board.Worker{}
	t0 := time.Unix(1000, 0)
	w := board.Worker{Worker: "worker1", State: "coding", Since: time.Unix(1, 0), UpdatedAt: t0}
	changedWorkers(last, []board.Worker{w})
	w.State, w.UpdatedAt = "blocked", t0.Add(5*time.Second)
	if got := changedWorkers(last, []board.Worker{w}); len(got) != 1 || !got[0].Since.Equal(t0.Add(5*time.Second)) {
		t.Fatalf("newly blocked = %+v", got)
	}
	w.UpdatedAt = t0.Add(10 * time.Second)
	if got := changedWorkers(last, []board.Worker{w}); len(got) != 0 {
		t.Errorf("still blocked = %+v, want no write", got)
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

func TestPrunedWorkers(t *testing.T) {
	last := map[string]board.Worker{"worker1": {}, "worker2": {}}
	if got := prunedWorkers(last, []string{"worker1", "worker2"}); len(got) != 0 {
		t.Errorf("same set = %v", got)
	}
	if got := prunedWorkers(last, []string{"worker1"}); len(got) != 1 || got[0] != "worker2" {
		t.Errorf("closed = %v", got)
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
	pr := prState{Number: 421, URL: "u", Title: "t", Head: "feat/x", Base: "main", Mergeable: "CONFLICTING", CI: "green", LastReview: "APPROVED by alice", OpenThreads: 2}
	got := boardPR("/r", pr, "worker2", "", true, now)
	if got.Status != "conflicting" || got.CI != "green" || got.Review != "APPROVED by alice · 2 threads open" || got.Worker != "worker2" || got.Head != "feat/x" {
		t.Errorf("= %+v", got)
	}
	if !got.SinceReady.IsZero() || !got.SinceHole.IsZero() {
		t.Errorf("conflicting and held = %+v, want neither ready nor a hole", got)
	}
	if got := boardPR("/r", pr, "", "merged", false, now); got.Status != "merged" || !got.SinceHole.IsZero() {
		t.Errorf("fate = %+v, want merged and no hole", got)
	}
	// Threads open and nobody on it: a hole.
	if got := boardPR("/r", pr, "", "", false, now); !got.SinceHole.Equal(now) {
		t.Errorf("unheld threads = %+v, want a hole", got)
	}
	pr.Mergeable, pr.OpenThreads = "MERGEABLE", 0
	if got := boardPR("/r", pr, "", "", false, now); !got.SinceReady.Equal(now) || !got.SinceHole.IsZero() {
		t.Errorf("approved and green = %+v, want ready", got)
	}
	pr.LastReview = ""
	if got := boardPR("/r", pr, "", "", false, now); got.Status != "open" || got.Review != "" || !got.SinceReady.IsZero() {
		t.Errorf("plain = %+v", got)
	}
}

// A busy worker holds its PR and branch, a free one nothing, a queued
// task its branch.
func TestHeldPRs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "worker1.json"), []byte(`{"pr_url": "https://github.com/o/r/pull/1/", "branch": "feat/a"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "worker2.json"), []byte(`{"pr_url": "https://github.com/o/r/pull/2", "branch": "feat/b"}`), 0o644)
	pool := poolState{Workers: []poolWorker{{Index: 1, State: workerBusy}, {Index: 2, State: workerFree}}}
	urls, branches := heldPRs(dir, pool, taskQueue{Tasks: []queuedTask{{Branch: "feat/c"}}})
	if !urls["https://github.com/o/r/pull/1"] || urls["https://github.com/o/r/pull/2"] || !branches["feat/a"] || branches["feat/b"] || !branches["feat/c"] {
		t.Errorf("urls %v, branches %v", urls, branches)
	}
}

// acw stop keeps what the workers were on and what was queued, and ends
// the pool in the same lock: nothing queued after is lost unseen. A stop
// run again finds no pool and keeps what the first one saved.
func TestSaveInterrupted(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := testSwarm(t, 2,
		poolWorker{Index: 1, State: workerBusy, Task: 3, Label: "worker1", Current: &queuedTask{ID: 3, Brief: "fix the VAT rounding", Branch: "fix/vat"}},
		poolWorker{Index: 2, State: workerFree, Label: "worker2"})
	os.WriteFile(filepath.Join(names.StatusDir(repo), "worker1.json"), []byte(`{"state":"coding","summary":"half way","pr_url":"https://github.com/o/r/pull/12"}`), 0o644)
	writeJSON(names.QueueFile(repo), taskQueue{NextID: 5, Tasks: []queuedTask{{ID: 4, Brief: "review #12", Kind: "need-review", After: []int{3}}}})
	if err := saveInterrupted(repo, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(names.PoolFile(repo)); err == nil {
		t.Error("pool.json still there: a task could still be queued")
	}
	if err := saveInterrupted(repo, io.Discard); err != nil {
		t.Fatalf("second stop = %v", err)
	}
	got, delivered := startPrompt(repo)
	for _, want := range []string{"fix the VAT rounding", "worker1", "fix/vat", "pull/12", "half way", "review #12", "queued", "--kind need-review"} {
		if !strings.Contains(got, want) {
			t.Errorf("start prompt lacks %q:\n%s", want, got)
		}
	}
	delivered()
	if got, _ := startPrompt(repo); got != "" {
		t.Errorf("after delivery = %q, want nothing", got)
	}
}

// The next master's first prompt has the decisions still parked, a
// document's path and a refusal included; nothing left gives no prompt.
func TestStartPromptParked(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := t.TempDir()
	if got, _ := startPrompt(repo); got != "" {
		t.Errorf("nothing left = %q", got)
	}
	withBoard(func(b *board.DB) error {
		b.Park(board.Parked{Repo: repo, Ticket: "SHOP-9", On: "client", Text: "Which address?", CreatedAt: time.Now()})
		id, _ := b.Park(board.Parked{Repo: repo, Ticket: "SHOP-7", On: "me", Worker: "worker2", Text: "Approve the plan", Kind: "plan", DocPath: "/plans/p.md", CreatedAt: time.Now()})
		_, err := b.RefuseParked(id)
		return err
	})
	got, _ := startPrompt(repo)
	for _, want := range []string{"#1 SHOP-9 · waits on client", "/plans/p.md", "refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("start prompt lacks %q:\n%s", want, got)
		}
	}
}

// A document to approve is parked with its path and kind, and frees its
// worker: the user may take days to read it. A path the worker loses at
// acw stop, relative or missing, is refused, and nothing is parked.
func TestParkDocument(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := testSwarm(t, 2, poolWorker{Index: 1, State: workerBusy, Task: 3, Current: &queuedTask{ID: 3, Brief: "plan it"}})
	os.WriteFile(filepath.Join(names.StatusDir(repo), "worker1.json"), []byte(`{"state":"plan_ready","tache":"SHOP-9","branch":"fix/vat"}`), 0o644)
	doc := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(doc, []byte("# Plan\n"), 0o644)
	inTree := filepath.Join(names.WorktreesDir(repo), "worker1-20261009120000", "plan.md")
	os.MkdirAll(filepath.Dir(inTree), 0o755)
	os.WriteFile(inTree, []byte("x"), 0o644)
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := boardCommand()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetIn(strings.NewReader("Approve the plan"))
		cmd.SetArgs(append([]string{"park", "--repo", repo, "--ticket", "SHOP-9", "--on", "me"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}
	for _, bad := range [][]string{
		{"--worker", "worker1", "--doc", "plan.md"},
		{"--worker", "worker1", "--doc", filepath.Join(t.TempDir(), "gone.md")},
		{"--worker", "worker1", "--doc", t.TempDir()},
		{"--worker", "worker1", "--doc", inTree},
		{"--doc", doc},
	} {
		if _, err := run(bad...); err == nil {
			t.Errorf("park %v accepted", bad)
		}
	}
	out, err := run("--worker", "worker1", "--doc", doc)
	if err != nil || !strings.Contains(out, "#1 parked") || !strings.Contains(out, "worker1 is free") {
		t.Fatalf("park --doc = %q, %v", out, err)
	}
	if p, _, _ := readPool(repo); p.worker(1).State != workerFree {
		t.Errorf("worker1 = %+v, want free", p.worker(1))
	}
	d := boardDay(t, repo, time.Now())
	// The branch is kept: the worker is on another task by the time the
	// user accepts, and the follow-up goes back to it.
	if len(d.Parked) != 1 || d.Parked[0].DocPath != doc || d.Parked[0].Kind != "plan" || d.Parked[0].Worker != "worker1" || d.Parked[0].Branch != "fix/vat" {
		t.Errorf("parked = %+v", d.Parked)
	}
	cmd := boardCommand()
	var shown strings.Builder
	cmd.SetOut(&shown)
	cmd.SetArgs([]string{"parked", "1"})
	cmd.Execute()
	if !strings.Contains(shown.String(), "document: "+doc) {
		t.Errorf("parked 1 = %q, want the document's path", shown.String())
	}
	if out, err := run("--worker", "worker1", "--doc", doc); err != nil || !strings.Contains(out, "already free") {
		t.Errorf("park --doc of a free worker = %q, %v; want parked, already free", out, err)
	}
}

func TestDocKind(t *testing.T) {
	for state, want := range map[string]string{"plan_ready": "plan", "verdict_ready": "verdict", "review_ready": "review draft", "coding": "document"} {
		if got := docKind(state); got != want {
			t.Errorf("docKind(%q) = %q, want %q", state, got, want)
		}
	}
}

// Park, list, read back and resume: the number is what the user quotes,
// the answer becomes a decision, a second answer is refused.
func TestParkAndResume(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := t.TempDir()
	run := func(stdin string, args ...string) (string, error) {
		t.Helper()
		cmd := boardCommand()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetIn(strings.NewReader(stdin))
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := run("Which address on a reissue?\n1. the old one\n2. the new one", "park", "--repo", repo, "--ticket", "SHOP-7", "--on", "client")
	if err != nil || !strings.HasPrefix(out, "#1 parked") {
		t.Fatalf("park = %q, %v", out, err)
	}
	if _, err := run("x", "park", "--repo", repo); err == nil {
		t.Error("park without --on accepted")
	}
	if out, _ := run("", "parked", "--repo", repo); !strings.Contains(out, "#1 SHOP-7 · waits on client") || !strings.Contains(out, "Which address") {
		t.Errorf("parked = %q", out)
	}
	if out, _ := run("", "parked", "#1"); !strings.Contains(out, "2. the new one") {
		t.Errorf("parked #1 = %q, want all of it", out)
	}
	if out, err := run("", "edit", "1", "--on", "me", "--repo", repo); err != nil || !strings.Contains(out, "#1 now waits on me") {
		t.Fatalf("edit = %q, %v", out, err)
	}
	if out, _ := run("", "parked", "--repo", repo); !strings.Contains(out, "waits on me") {
		t.Errorf("parked after edit = %q", out)
	}
	if _, err := run("", "edit", "1"); err == nil {
		t.Error("edit without --on accepted")
	}
	if _, err := run("the old one", "resume", "1", "--repo", t.TempDir()); err == nil || !strings.Contains(err.Error(), "belongs to") {
		t.Errorf("resume from another repo = %v, want refused", err)
	}
	if out, err := run("the old one", "resume", "1", "--repo", repo); err != nil || !strings.Contains(out, "#1 closed") {
		t.Fatalf("resume = %q, %v", out, err)
	}
	if _, err := run("the new one", "resume", "1"); err == nil || !strings.Contains(err.Error(), "the old one") {
		t.Errorf("second resume = %v", err)
	}
	if _, err := run("", "edit", "1", "--on", "client"); err == nil || !strings.Contains(err.Error(), "already closed") {
		t.Errorf("edit of a closed one = %v", err)
	}
	d := boardDay(t, repo, time.Now())
	if len(d.Decisions) != 1 || d.Decisions[0].Text != "the old one" || d.Decisions[0].Subject != "SHOP-7" || !strings.Contains(d.Decisions[0].Why, "#1") {
		t.Errorf("decisions = %+v", d.Decisions)
	}
	if out, _ := run("", "parked", "--repo", repo); !strings.Contains(out, "no decision parked") {
		t.Errorf("parked after resume = %q", out)
	}
}
