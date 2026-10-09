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
		if err := queueAdd(repo, briefFile(t, text), addOptions{}, t0, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if err := queueAdd(repo, briefFile(t, "urgent"), addOptions{Branch: branchRequest{Branch: "fix/x"}, Worker: "worker2", Top: true}, t0, io.Discard); err != nil {
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
	if err := queueAdd(repo, briefFile(t, "x"), addOptions{Worker: "worker3"}, t0, io.Discard); err == nil {
		t.Error("a task for a worker beyond the count should be refused")
	}
}

// What a dead watcher left half done is undone by the next one: a task
// whose brief never reached its worker goes back first in the queue, the
// worker free; a worker caught opening is dropped. The master is told.
// The field case: worker2 busy #45 without ever getting it, worker3
// stuck opening and holding a max-stacks slot.
func TestRecoverPool(t *testing.T) {
	t45 := queuedTask{ID: 45, Brief: "health proof", Branch: "feat/h"}
	repo := testSwarm(t, 3,
		poolWorker{Index: 1, State: workerBusy, Task: 40},
		poolWorker{Index: 2, State: workerBusy, Task: 45, TaskBranch: "feat/h", Dispatching: &t45, Since: t0},
		poolWorker{Index: 3, State: workerOpening, Worktree: filepath.Join(t.TempDir(), "gone")})
	p, q, _ := readPool(repo)
	p.Plan.Repo, p.Plan.Inbox = repo, names.Inbox(repo)
	q.Tasks = []queuedTask{{ID: 47}}
	if err := writeJSON(names.PoolFile(repo), p); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(names.QueueFile(repo), q); err != nil {
		t.Fatal(err)
	}
	recoverPool(repo)
	p, q, _ = readPool(repo)
	if ids := queueIDs(t, repo); !slices.Equal(ids, []int{45, 47}) || q.Tasks[0].Brief != "health proof" {
		t.Errorf("queue = %v, want #45 back first", ids)
	}
	if w := p.worker(2); w == nil || w.State != workerFree || w.Task != 0 || w.Dispatching != nil || w.TaskBranch != "" || !w.Since.After(t0) {
		t.Errorf("worker2 = %+v, want free, idle from now", w)
	}
	if w := p.worker(1); w == nil || w.State != workerBusy || w.Task != 40 {
		t.Errorf("worker1 = %+v, want its task kept", w)
	}
	if p.worker(3) != nil {
		t.Error("worker3, caught opening, still in the pool")
	}
	if inbox, _ := os.ReadFile(names.Inbox(repo)); !strings.Contains(string(inbox), "task #45 never reached worker2") || !strings.Contains(string(inbox), "worker3 was opening") {
		t.Errorf("inbox = %q", inbox)
	}
}

// Without a kept plan (a swarm an older acw started), acw watch says how
// to get one; the plan file reads back as written.
func TestWatchPlanFile(t *testing.T) {
	repo := testSwarm(t, 1)
	if err := restartWatcher(repo, io.Discard); err == nil || !strings.Contains(err.Error(), "acw stop then acw start") {
		t.Errorf("no plan: %v", err)
	}
	want := watchPlan{Repo: repo, MasterName: "master-x", InboxNext: "acw __inbox-next /i", SilenceMinutes: 30, Stamp: "s"}
	if err := writeJSON(names.WatchPlan(repo), want); err != nil {
		t.Fatal(err)
	}
	if got, err := readWatchPlan(names.WatchPlan(repo)); err != nil || got != want {
		t.Errorf("readWatchPlan = %+v, %v", got, err)
	}
}

// --after-merge needs the PR watch; a task waits until the PR its
// prerequisite ended with is seen merged; one ended without a PR holds
// what waits for it, and can't be waited for afterwards.
func TestQueueAfterMerge(t *testing.T) {
	repo := testSwarm(t, 2, poolWorker{Index: 1, State: workerBusy, Task: 1}, poolWorker{Index: 2, State: workerBusy, Task: 2})
	if err := queueAdd(repo, briefFile(t, "x"), addOptions{AfterMerge: []int{1}}, t0, io.Discard); err == nil || !strings.Contains(err.Error(), "pr-watch") {
		t.Fatalf("without pr-watch = %v", err)
	}
	p, q, _ := readPool(repo)
	p.Plan.PRWatch, q.NextID = true, 2
	if err := writeJSON(names.PoolFile(repo), p); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(names.QueueFile(repo), q); err != nil {
		t.Fatal(err)
	}
	for id := range 2 {
		if err := queueAdd(repo, briefFile(t, "plan SHOP-9"), addOptions{AfterMerge: []int{id + 1}}, t0, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(names.StatusDir(repo), "worker1.json"), []byte(`{"pr_url": "https://github.com/o/r/pull/12"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := markDone(repo, 1, 1, t0); err != nil {
		t.Fatal(err)
	}
	msg, _, err := markDone(repo, 2, 2, t0)
	if err != nil || !strings.Contains(msg, "Task #4 held") {
		t.Fatalf("done without a PR = %q, %v", msg, err)
	}
	p, q, _ = readPool(repo)
	if waits := waitingFor(q.Tasks[0], p, q); !slices.Equal(waits, []int{1}) || q.PRs[1] != "https://github.com/o/r/pull/12" {
		t.Errorf("ended, PR open: waits %v, PRs %v", waits, q.PRs)
	}
	if !q.mergedPR("https://github.com/o/r/pull/12") || len(waitingFor(q.Tasks[0], p, q)) != 0 {
		t.Errorf("after the merge: waits %v", waitingFor(q.Tasks[0], p, q))
	}
	if err := queueAdd(repo, briefFile(t, "x"), addOptions{AfterMerge: []int{2}}, t0, io.Discard); err == nil || !strings.Contains(err.Error(), "without a PR") {
		t.Errorf("--after-merge of a task ended without a PR = %v", err)
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
	if err := queueAdd(repo, briefFile(t, "rebase #2 onto the merged base"), addOptions{}, t0, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := queueAdd(repo, briefFile(t, "stack the next PR"), addOptions{After: []int{1, 2}}, t0, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "waits for #1, #2") {
		t.Errorf("add = %q, want it to say what it waits for", out.String())
	}
	if err := queueAdd(repo, briefFile(t, "x"), addOptions{After: []int{9}}, t0, io.Discard); err == nil {
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

// --kind needs a worker that takes it, and a --worker that takes it.
func TestQueueAddKind(t *testing.T) {
	repo := testSwarm(t, 2)
	p, _, _ := readPool(repo)
	p.Plan.Workers[1] = workerSpec{Kind: "claude", Overridden: true, Tasks: []string{"need-review"}}
	if err := writeJSON(names.PoolFile(repo), p); err != nil {
		t.Fatal(err)
	}
	if err := queueAdd(repo, briefFile(t, "x"), addOptions{Kind: "analysis"}, t0, io.Discard); err == nil || !strings.Contains(err.Error(), "no worker takes it") {
		t.Errorf("a kind nobody takes = %v", err)
	}
	if err := queueAdd(repo, briefFile(t, "x"), addOptions{Worker: "worker1", Kind: "need-review"}, t0, io.Discard); err == nil {
		t.Error("--worker worker1 --kind need-review accepted, worker1 does not take it")
	}
	if err := queueAdd(repo, briefFile(t, "review #12"), addOptions{Kind: "need-review"}, t0, io.Discard); err != nil {
		t.Fatal(err)
	}
	p, q, _ := readPool(repo)
	if got := renderQueue(t0, p, q); !strings.Contains(got, "#1  review #12 · kind need-review") {
		t.Errorf("queue =\n%s", got)
	}
}

// A branch or a base starting with a dash would reach git as an option.
func TestQueueAddRefusesOptionLikeBranches(t *testing.T) {
	repo := testSwarm(t, 1)
	for _, br := range []branchRequest{{Branch: "--orphan=x"}, {Branch: "fix/x", Base: "-d"}} {
		if err := queueAdd(repo, briefFile(t, "x"), addOptions{Branch: br}, t0, io.Discard); err == nil {
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
			if err := queueAdd(repo, path, addOptions{}, t0, io.Discard); err != nil {
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

// What the branch step found goes with the task's line to the master.
func TestBranchNote(t *testing.T) {
	steps := "worker3: context reset.\nworker3: on feat/x, brought up to origin/feat/x (6 commits it lacked).\n"
	if got := branchNote(steps, "worker3"); got != "on feat/x, brought up to origin/feat/x (6 commits it lacked)" {
		t.Errorf("= %q", got)
	}
	if got := branchNote("worker3: context reset.\n", "worker3"); got != "" {
		t.Errorf("no branch step = %q", got)
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
	mem := newPoolMemory()
	poll := func() {
		runPool(repo, p, taskQueue{}, nil, t0, mem)
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
