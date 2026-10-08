package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/names"
)

// testSwarm is a repo with a pool of n workers and an empty queue.
func testSwarm(t *testing.T, n int, open ...poolWorker) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(names.PoolFile(repo), testPool(n, n, open...)); err != nil {
		t.Fatal(err)
	}
	return repo
}

func briefFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func queueIDs(t *testing.T, repo string) []int {
	t.Helper()
	_, q, err := readPool(repo)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, task := range q.Tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func TestQueueAddMoveRemove(t *testing.T) {
	repo := testSwarm(t, 2)
	for _, text := range []string{"one", "two", "three"} {
		if err := queueAdd(repo, briefFile(t, text), branchRequest{}, "", nil, false, t0, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if err := queueAdd(repo, briefFile(t, "urgent"), branchRequest{Branch: "fix/x"}, "worker2", nil, true, t0, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := queueIDs(t, repo); !slices.Equal(got, []int{4, 1, 2, 3}) {
		t.Fatalf("after add --top: %v, want [4 1 2 3]", got)
	}
	_, q, _ := readPool(repo)
	if q.Tasks[0].Brief != "urgent" || q.Tasks[0].Worker != 2 || q.Tasks[0].Branch != "fix/x" {
		t.Errorf("queued task = %+v, want the brief copied in, worker2, fix/x", q.Tasks[0])
	}

	if err := queueMove(repo, 3, 1, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := queueRemove(repo, 1, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := queueIDs(t, repo); !slices.Equal(got, []int{3, 4, 2}) {
		t.Errorf("after move and remove: %v, want [3 4 2]", got)
	}
	if err := queueRemove(repo, 42, io.Discard); err == nil {
		t.Error("removing a task that is not there should fail")
	}
	if err := queueAdd(repo, briefFile(t, "x"), branchRequest{}, "worker3", nil, false, t0, io.Discard); err == nil {
		t.Error("a task for a worker beyond the count should be refused")
	}
}

// --after names tasks that exist; the queue shows what a task waits for,
// and removing a prerequisite holds the tasks that wait for it.
func TestQueueAfter(t *testing.T) {
	repo := testSwarm(t, 2, poolWorker{Index: 1, State: workerBusy, Task: 1})
	_, q, _ := readPool(repo)
	q.NextID = 1 // task #1 went out to worker1
	if err := writeJSON(names.QueueFile(repo), q); err != nil {
		t.Fatal(err)
	}
	if err := queueAdd(repo, briefFile(t, "rebase #2 onto the merged base"), branchRequest{}, "", nil, false, t0, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := queueAdd(repo, briefFile(t, "stack the next PR"), branchRequest{}, "", []int{1, 2}, false, t0, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "waits for #1, #2") {
		t.Errorf("add = %q, want it to say what it waits for", out.String())
	}
	if err := queueAdd(repo, briefFile(t, "x"), branchRequest{}, "", []int{9}, false, t0, io.Discard); err == nil {
		t.Error("--after a task that never existed was accepted")
	}
	p, q, _ := readPool(repo)
	if got := renderQueue(t0, p, q); !strings.Contains(got, "#3  stack the next PR · waiting for #1, #2") {
		t.Errorf("queue =\n%s", got)
	}
	out.Reset()
	if err := queueRemove(repo, 2, &out); err != nil {
		t.Fatal(err)
	}
	_, q, _ = readPool(repo)
	if len(q.Tasks) != 1 || !strings.Contains(q.Tasks[0].Error, "#2, was removed") || !strings.Contains(out.String(), "task #3 held") {
		t.Errorf("after removing #2: %+v, said %q", q.Tasks, out.String())
	}
}

// A branch or a base starting with a dash would reach git as an option.
func TestQueueAddRefusesOptionLikeBranches(t *testing.T) {
	repo := testSwarm(t, 1)
	for _, br := range []branchRequest{{Branch: "--orphan=x"}, {Branch: "fix/x", Base: "-d"}} {
		if err := queueAdd(repo, briefFile(t, "x"), br, "", nil, false, t0, io.Discard); err == nil {
			t.Errorf("queueAdd(%+v) = nil, want a refusal", br)
		}
	}
	if got := queueIDs(t, repo); len(got) != 0 {
		t.Errorf("queue = %v, nothing should have been queued", got)
	}
}

// Moving a held task is the master letting it go again.
func TestQueueMoveClearsTheHold(t *testing.T) {
	repo := testSwarm(t, 1)
	if err := writeJSON(names.QueueFile(repo), taskQueue{NextID: 1, Tasks: []queuedTask{{ID: 1, Error: "branch held"}}}); err != nil {
		t.Fatal(err)
	}
	if err := queueMove(repo, 1, 1, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, q, _ := readPool(repo); q.Tasks[0].Error != "" {
		t.Errorf("held task still held after a move: %+v", q.Tasks[0])
	}
}

// The master's acw queue and the watcher change the same files: under the
// lock, no write is lost.
func TestQueueConcurrentAddsAllLand(t *testing.T) {
	repo := testSwarm(t, 1)
	path := briefFile(t, "task")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := queueAdd(repo, path, branchRequest{}, "", nil, false, t0, io.Discard); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got := queueIDs(t, repo)
	slices.Sort(got)
	if len(got) != 20 || got[0] != 1 || got[19] != 20 {
		t.Errorf("queue after 20 concurrent adds = %v, want ids 1 to 20", got)
	}
}

func TestMarkDone(t *testing.T) {
	repo := testSwarm(t, 2, poolWorker{Index: 1, State: workerBusy, Task: 3})
	msg, finished, err := markDone(repo, 1, 3, t0)
	if err != nil || !strings.Contains(msg, "free") {
		t.Fatalf("markDone() = %q, %v", msg, err)
	}
	if finished != 3 {
		t.Errorf("finished = %d, want 3", finished)
	}
	p, _, _ := readPool(repo)
	if w := p.worker(1); w.State != workerFree || w.Task != 0 || !w.Since.Equal(t0) {
		t.Errorf("worker1 = %+v, want free since now, no task", w)
	}
	if msg, finished, err := markDone(repo, 1, 0, t0); err != nil || !strings.Contains(msg, "already free") {
		t.Errorf("markDone() twice = %q, %v; want no error, already free", msg, err)
	} else if finished != 0 {
		t.Errorf("finished = %d, want 0", finished)
	}
	if _, _, err := markDone(repo, 2, 0, t0); err == nil {
		t.Error("markDone() on a worker that is not open should fail")
	}
}

// A done for a task already over must not free the worker from the next
// one the watcher just handed it.
func TestMarkDoneRefusesAnotherTask(t *testing.T) {
	repo := testSwarm(t, 1, poolWorker{Index: 1, State: workerBusy, Task: 4})
	if _, _, err := markDone(repo, 1, 3, t0); err == nil || !strings.Contains(err.Error(), "#4") {
		t.Errorf("markDone(task 3) on a worker busy with #4 = %v, want a refusal naming #4", err)
	}
	if p, _, _ := readPool(repo); p.worker(1).State != workerBusy {
		t.Error("worker1 was freed from task #4")
	}
}

func TestPoolCommandsOutsideASwarm(t *testing.T) {
	if err := queueRemove(t.TempDir(), 1, io.Discard); err == nil || !strings.Contains(err.Error(), "no acw swarm") {
		t.Errorf("queueRemove() outside a swarm = %v, want no acw swarm", err)
	}
}

func TestRenderQueue(t *testing.T) {
	p := testPool(3, 3, poolWorker{Index: 1, State: workerBusy, Task: 2, Since: t0.Add(-5 * time.Minute)})
	q := tasks(queuedTask{ID: 3, Brief: "Fix the export\nmore", Worker: 1, AddedAt: t0, Error: "branch held"})
	got := renderQueue(t0, p, q)
	for _, want := range []string{"1 open of 3", "worker1  busy #2 since 5 min ago", "1. #3  Fix the export · for worker1", "held: branch held"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderQueue() missing %q in:\n%s", want, got)
		}
	}
}

// The master hears once that max-stacks keeps the floor short, again only
// after it was met in between.
func TestRunPoolTellsAShortFloorOnce(t *testing.T) {
	repo := testSwarm(t, 3)
	p := testPool(3, 2)
	p.Plan.Repo, p.Plan.Inbox = repo, names.Inbox(repo)
	p.MinWorkers, p.Held = 3, 2
	told := false
	poll := func() {
		runPool(repo, p, taskQueue{}, nil, t0, map[int]bool{}, &told)
	}
	count := func() int {
		inbox, _ := os.ReadFile(p.Plan.Inbox)
		return strings.Count(string(inbox), "max-stacks (2) is reached, 2 of them")
	}
	poll()
	poll()
	if n := count(); n != 1 {
		t.Fatalf("told %d times, want once", n)
	}
	p.MinWorkers = 0
	poll()
	p.MinWorkers = 3
	poll()
	if n := count(); n != 2 {
		t.Errorf("told %d times, want twice: the floor was met in between", n)
	}
}
