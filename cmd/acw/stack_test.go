package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/names"
)

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
	if err := stackUp(repo, 1, time.Now().Add(50*time.Millisecond), 10*time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); len(got) != 0 || !strings.Contains(out.String(), "still no free place (1 of 1 stacks running)") {
		t.Fatalf("full: wtm calls = %q, said %q", got, out.String())
	}

	dropStack(repo, 2)
	out.Reset()
	if err := stackUp(repo, 1, time.Now().Add(time.Second), 10*time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); string(got) != "start agents/worker1-20260925140000 --profile light\n" {
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
