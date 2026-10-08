package main

import (
	"slices"
	"testing"
	"time"
)

// pool of n general-purpose claude workers, stacks capped at maxStacks.
func testPool(n, maxStacks int, open ...poolWorker) poolState {
	specs := make([]workerSpec, n)
	for i := range specs {
		specs[i] = workerSpec{Kind: "claude"}
	}
	return poolState{
		Plan:             provisionPlan{Stacks: true, MaxStacks: maxStacks, Workers: specs},
		IdleCloseMinutes: 10,
		Workers:          open,
	}
}

func free(index int, since time.Time) poolWorker {
	return poolWorker{Index: index, State: workerFree, Since: since, Stacked: true}
}

func tasks(ts ...queuedTask) taskQueue { return taskQueue{Tasks: ts} }

func readyAll(indexes ...int) map[int]workerPoll {
	polls := map[int]workerPoll{}
	for _, i := range indexes {
		polls[i] = workerPoll{Ready: true, Clean: true}
	}
	return polls
}

func TestScheduleEmptyQueueDoesNothing(t *testing.T) {
	if got := schedule(testPool(3, 3), taskQueue{}, nil, t0); len(got) != 0 {
		t.Errorf("schedule() = %+v, want nothing", got)
	}
}

// Tasks go out in the queue's order, to the lowest free worker, and the
// rest open workers up to the count.
func TestScheduleAssignsInOrderThenOpens(t *testing.T) {
	p := testPool(3, 3, free(2, t0))
	q := tasks(queuedTask{ID: 7}, queuedTask{ID: 8}, queuedTask{ID: 9}, queuedTask{ID: 10})
	got := schedule(p, q, readyAll(2), t0)
	want := []poolAction{
		{Kind: actAssign, Worker: 2, Task: 7},
		{Kind: actOpen, Worker: 1, Stacked: true},
		{Kind: actOpen, Worker: 3, Stacked: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("schedule() = %+v, want %+v (task 10 waits: the count is reached)", got, want)
	}
}

// reviewerPool is n general workers whose last one only takes need-review
// tasks, kept open when keep is set.
func reviewerPool(n int, keep bool, open ...poolWorker) poolState {
	p := testPool(n, n, open...)
	p.Plan.Workers[n-1] = workerSpec{Kind: "claude", Overridden: true, Tasks: []string{"need-review"}, Keep: keep}
	return p
}

// A task of a kind goes to a worker that takes it, a task of no kind to a
// general one; neither ever to the other.
func TestScheduleKinds(t *testing.T) {
	review, general := queuedTask{ID: 7, Kind: "need-review"}, queuedTask{ID: 8}
	got := schedule(reviewerPool(3, false, free(1, t0), free(3, t0)), tasks(review, general), readyAll(1, 3), t0)
	if want := []poolAction{{Kind: actAssign, Worker: 3, Task: 7}, {Kind: actAssign, Worker: 1, Task: 8}}; !slices.Equal(got, want) {
		t.Errorf("both free = %+v, want %+v", got, want)
	}
	busy := poolWorker{Index: 3, State: workerBusy, Task: 5}
	if got := schedule(reviewerPool(3, false, free(1, t0), busy), tasks(review), readyAll(1, 3), t0); len(got) != 0 {
		t.Errorf("reviewer busy, worker1 free = %+v, want the review to wait for the reviewer", got)
	}
	if got := schedule(reviewerPool(3, false, free(3, t0)), tasks(general), readyAll(3), t0); slices.ContainsFunc(got, func(a poolAction) bool { return a.Kind == actAssign }) {
		t.Errorf("only the reviewer free = %+v, want no general task on it", got)
	}
	got = schedule(reviewerPool(3, false, free(1, t0)), tasks(review), readyAll(1), t0)
	if want := []poolAction{{Kind: actOpen, Worker: 3, Stacked: true}}; !slices.Equal(got, want) {
		t.Errorf("reviewer not open = %+v, want %+v", got, want)
	}
	opening := poolWorker{Index: 3, State: workerOpening}
	if got := schedule(reviewerPool(3, false, opening), tasks(review, queuedTask{ID: 9, Kind: "need-review"}), readyAll(), t0); len(got) != 0 {
		t.Errorf("reviewer coming up = %+v, want both reviews to wait for it", got)
	}
}

// A kept worker opens with the swarm, outside the floor, and is never
// closed for being idle.
func TestScheduleKeep(t *testing.T) {
	got := schedule(reviewerPool(3, true), taskQueue{}, nil, t0)
	if want := []poolAction{{Kind: actOpen, Worker: 3, Stacked: true}}; !slices.Equal(got, want) {
		t.Errorf("nothing queued = %+v, want %+v", got, want)
	}
	p := reviewerPool(3, true, free(1, t0), free(3, t0))
	p.MinWorkers = 1
	if got := schedule(p, taskQueue{}, readyAll(1, 3), t0.Add(time.Hour)); len(got) != 0 {
		t.Errorf("idle an hour = %+v, want worker1 kept by the floor and worker3 by keep", got)
	}
	p.MinWorkers = 0
	got = schedule(p, taskQueue{}, readyAll(1, 3), t0.Add(time.Hour))
	if want := []poolAction{{Kind: actClose, Worker: 1}}; !slices.Equal(got, want) {
		t.Errorf("no floor = %+v, want %+v", got, want)
	}
}

// A task queued --after another waits until that one is ended: neither in
// the queue nor on a busy worker. While it waits it opens no worker.
func TestScheduleHoldsATaskUntilItsPrerequisitesEnd(t *testing.T) {
	q := tasks(queuedTask{ID: 7}, queuedTask{ID: 8, After: []int{7}})
	got := schedule(testPool(2, 2, free(1, t0), free(2, t0)), q, readyAll(1, 2), t0)
	if want := []poolAction{{Kind: actAssign, Worker: 1, Task: 7}}; !slices.Equal(got, want) {
		t.Errorf("7 queued = %+v, want %+v", got, want)
	}
	busy := poolWorker{Index: 1, State: workerBusy, Task: 7}
	if got := schedule(testPool(2, 2, busy, free(2, t0)), tasks(queuedTask{ID: 8, After: []int{7}}), readyAll(1, 2), t0); len(got) != 0 {
		t.Errorf("7 on worker1 = %+v, want 8 to wait", got)
	}
	if got := schedule(testPool(2, 2, busy), tasks(queuedTask{ID: 8, After: []int{7}}), readyAll(1), t0); len(got) != 0 {
		t.Errorf("7 on worker1, room for one more = %+v, want no worker opened for a waiting task", got)
	}
	ended := poolWorker{Index: 1, State: workerFree, Since: t0}
	got = schedule(testPool(2, 2, ended, free(2, t0)), tasks(queuedTask{ID: 8, After: []int{7}}), readyAll(1, 2), t0)
	if want := []poolAction{{Kind: actAssign, Worker: 1, Task: 8}}; !slices.Equal(got, want) {
		t.Errorf("7 ended = %+v, want %+v", got, want)
	}
}

// A free worker whose agent cannot take a brief yet gets nothing, and a
// worker already opening is counted for the first task waiting.
func TestScheduleWaitsForReadyAndCountsOpening(t *testing.T) {
	p := testPool(3, 3, free(1, t0), poolWorker{Index: 2, State: workerOpening, Since: t0})
	got := schedule(p, tasks(queuedTask{ID: 1}, queuedTask{ID: 2}), map[int]workerPoll{1: {Ready: false}}, t0)
	want := []poolAction{{Kind: actOpen, Worker: 3, Stacked: true}}
	if !slices.Equal(got, want) {
		t.Errorf("schedule() = %+v, want %+v", got, want)
	}
}

// max-stacks caps the workers in the code even under the worker count.
func TestScheduleStackCap(t *testing.T) {
	p := testPool(4, 2, poolWorker{Index: 1, State: workerBusy, Stacked: true})
	got := schedule(p, tasks(queuedTask{ID: 1}, queuedTask{ID: 2}), nil, t0)
	want := []poolAction{{Kind: actOpen, Worker: 2, Stacked: true}}
	if !slices.Equal(got, want) {
		t.Errorf("schedule() = %+v, want %+v: only one stack left", got, want)
	}
}

// A stack kept with a closed worker's worktree still takes its room.
func TestScheduleCountsHeldStacks(t *testing.T) {
	p := testPool(4, 2)
	p.Held = 1
	got := schedule(p, tasks(queuedTask{ID: 1}, queuedTask{ID: 2}), nil, t0)
	if !slices.Equal(got, []poolAction{{Kind: actOpen, Worker: 1, Stacked: true}}) {
		t.Errorf("schedule() = %+v, want one opening: the held stack takes the other place", got)
	}
}

// A task named for a worker waits for that one, opened if it is not; a
// worker set apart only takes what is named for it.
func TestScheduleAffinityAndOverrides(t *testing.T) {
	p := testPool(3, 3, free(1, t0))
	p.Plan.Workers[2].Overridden = true
	q := tasks(queuedTask{ID: 1, Worker: 2}, queuedTask{ID: 2, Worker: 3}, queuedTask{ID: 3})
	got := schedule(p, q, readyAll(1), t0)
	want := []poolAction{
		{Kind: actOpen, Worker: 2, Stacked: true},
		{Kind: actOpen, Worker: 3, Stacked: true},
		{Kind: actAssign, Worker: 1, Task: 3},
	}
	if !slices.Equal(got, want) {
		t.Errorf("schedule() = %+v, want %+v", got, want)
	}

	// Busy worker2: its task waits, and no other worker takes it.
	p = testPool(3, 3, free(1, t0), poolWorker{Index: 2, State: workerBusy, Stacked: true})
	if got := schedule(p, tasks(queuedTask{ID: 1, Worker: 2}), readyAll(1), t0); len(got) != 0 {
		t.Errorf("schedule() = %+v, want worker2's task to wait for worker2", got)
	}
}

func TestScheduleSkipsHeldTasks(t *testing.T) {
	p := testPool(2, 2, free(1, t0))
	got := schedule(p, tasks(queuedTask{ID: 1, Error: "branch held"}, queuedTask{ID: 2}), readyAll(1), t0)
	want := []poolAction{{Kind: actAssign, Worker: 1, Task: 2}}
	if !slices.Equal(got, want) {
		t.Errorf("schedule() = %+v, want %+v", got, want)
	}
}

// Closed after idle-close-minutes, never with changes in its worktree,
// never while a task is named for it.
func TestScheduleClosesIdleWorkers(t *testing.T) {
	p := testPool(3, 3, free(1, t0), free(2, t0.Add(-11*time.Minute)), free(3, t0.Add(-11*time.Minute)))
	polls := readyAll(1, 2)
	polls[3] = workerPoll{Ready: true, Clean: false}
	got := schedule(p, taskQueue{}, polls, t0)
	want := []poolAction{{Kind: actClose, Worker: 2}, {Kind: actDirty, Worker: 3}}
	if !slices.Equal(got, want) {
		t.Errorf("schedule() = %+v, want %+v", got, want)
	}

	held := tasks(queuedTask{ID: 1, Worker: 2, Error: "x"}, queuedTask{ID: 2, Worker: 3})
	got = schedule(testPool(3, 3, free(3, t0.Add(-11*time.Minute))), held, readyAll(3), t0)
	if !slices.Equal(got, []poolAction{{Kind: actAssign, Worker: 3, Task: 2}}) {
		t.Errorf("schedule() = %+v, want worker3 given its task, not closed", got)
	}
}

// A kind acw cannot reset closes once its task is done, not right after
// opening.
func TestScheduleClosesAnUnresettableWorkerAfterItsTask(t *testing.T) {
	p := testPool(2, 2, free(1, t0), free(2, t0))
	p.Plan.Workers[0].Kind, p.Plan.Workers[1].Kind = "codex", "codex"
	p.Workers[1].Used = true
	got := schedule(p, taskQueue{}, readyAll(1, 2), t0)
	if !slices.Equal(got, []poolAction{{Kind: actClose, Worker: 2}}) {
		t.Errorf("schedule() = %+v, want only the used codex worker closed", got)
	}
}

// min-workers opens general-purpose workers with nothing queued and keeps
// them open past idle-close-minutes.
func TestScheduleMinWorkers(t *testing.T) {
	p := testPool(3, 3)
	p.MinWorkers = 2
	got := schedule(p, taskQueue{}, nil, t0)
	want := []poolAction{{Kind: actOpen, Worker: 1, Stacked: true}, {Kind: actOpen, Worker: 2, Stacked: true}}
	if !slices.Equal(got, want) {
		t.Errorf("schedule() = %+v, want %+v", got, want)
	}

	// max-stacks short of min-workers: the floor is not met, and said so.
	p = testPool(3, 2)
	p.MinWorkers, p.Held = 3, 1
	want = []poolAction{{Kind: actOpen, Worker: 1, Stacked: true}, {Kind: actStacksFull}}
	if got := schedule(p, taskQueue{}, nil, t0); !slices.Equal(got, want) {
		t.Errorf("schedule() = %+v, want %+v", got, want)
	}

	old := t0.Add(-time.Hour)
	p = testPool(3, 3, free(1, old), free(2, old), free(3, old))
	p.MinWorkers = 2
	if got := schedule(p, taskQueue{}, readyAll(1, 2, 3), t0); !slices.Equal(got, []poolAction{{Kind: actClose, Worker: 1}}) {
		t.Errorf("schedule() = %+v, want one close, down to min-workers", got)
	}
}
