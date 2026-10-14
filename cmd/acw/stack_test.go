package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/names"
)

// acw close refuses a busy worker and closes a free one at once, the
// master told.
func TestCloseByHand(t *testing.T) {
	repo := testSwarm(t, 2, poolWorker{Index: 1, State: workerBusy, Task: 7}, poolWorker{Index: 2, State: workerFree})
	p, _, _ := readPool(repo)
	p.Plan.Repo, p.Plan.Inbox = repo, names.Inbox(repo)
	if err := writeJSON(names.PoolFile(repo), p); err != nil {
		t.Fatal(err)
	}
	if err := closeByHand(repo, 1, "worker1", io.Discard); err == nil || !strings.Contains(err.Error(), "busy with task #7") {
		t.Errorf("busy: err = %v, want it refused", err)
	}
	if err := closeByHand(repo, 2, "worker2", io.Discard); err != nil {
		t.Fatal(err)
	}
	if p, _, _ = readPool(repo); p.worker(2) != nil || p.worker(1) == nil {
		t.Errorf("pool = %+v, want worker2 gone, worker1 kept", p.Workers)
	}
	if inbox, _ := os.ReadFile(names.Inbox(repo)); !strings.Contains(string(inbox), "worker2 closed: closed by hand with acw close") {
		t.Errorf("inbox = %q", inbox)
	}
	if err := closeByHand(repo, 2, "worker2", io.Discard); err == nil {
		t.Error("closing a worker not open went through")
	}
}

// A close waits for a move of the worktree under way (the watcher putting
// the worker back on its waiting branch) instead of removing it while wtm
// writes into it.
func TestCloseWaitsForAMoveUnderWay(t *testing.T) {
	repo := testSwarm(t, 1, poolWorker{Index: 1, State: workerFree})
	p, _, _ := readPool(repo)
	p.Plan.Repo, p.Plan.Inbox = repo, names.Inbox(repo)
	if err := writeJSON(names.PoolFile(repo), p); err != nil {
		t.Fatal(err)
	}
	release := lockWorktree(repo, "worker1")
	done := make(chan error)
	go func() { done <- closeByHand(repo, 1, "worker1", io.Discard) }()
	select {
	case err := <-done:
		t.Fatalf("closed during the move: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p, _, _ = readPool(repo); p.worker(1) != nil {
		t.Errorf("pool = %+v, want worker1 closed once the move is over", p.Workers)
	}
}

// acw stack up waits while max-stacks is reached, then takes the place
// given back and starts the stack; acw stack down stops it and frees the
// place.
func TestStackUpWaitsForAPlace(t *testing.T) {
	repo, calls := fakeSwarm(t, "light")
	holdStack(t, repo, false)
	p, _, _ := readPool(repo)
	p.Plan.Stacks, p.Plan.MaxStacks = true, 1
	other := time.Now()
	p.Workers = append(p.Workers, poolWorker{Index: 2, Stacked: true, State: workerBusy, StackSince: &other})
	if err := writeJSON(names.PoolFile(repo), p); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := stackUp(repo, 1, "light", time.Now().Add(50*time.Millisecond), 10*time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); len(got) != 0 || !strings.Contains(out.String(), "still no free place (1 of 1 stacks running)") {
		t.Fatalf("full: wtm calls = %q, said %q", got, out.String())
	}

	dropStack(repo, 2)
	out.Reset()
	// A task that needs a heavier profile asks for it, still under the cap.
	if err := stackUp(repo, 1, "async", time.Now().Add(time.Second), 10*time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); string(got) != "start agents/worker1-20260925140000 --profile async\n" {
		t.Errorf("place free: wtm calls = %q", got)
	}
	p, _, _ = readPool(repo)
	if p.worker(1).StackSince == nil || p.runningStacks() != 1 {
		t.Fatalf("after up: pool = %+v, want worker1 holding the one stack", p.Workers)
	}

	os.Remove(calls)
	if err := stackDown(repo, *p.worker(1), &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); string(got) != "stop agents/worker1-20260925140000\n" {
		t.Errorf("down: wtm calls = %q", got)
	}
	if p, _, _ = readPool(repo); p.runningStacks() != 0 {
		t.Errorf("after down: %d running, want 0", p.runningStacks())
	}
}
