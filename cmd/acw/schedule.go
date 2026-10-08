package main

import (
	"slices"
	"time"
)

// schedule decides, from one poll, what the pool does next: which task
// goes to which free worker, which worker to open, which to close. It is
// a pure function of the pool, the queue and what the poll read, so the
// rules hold whatever the master writes: the master orders the queue,
// schedule only ever takes it in that order.
//
// The rules:
//   - a task named for a worker (Worker) waits for that one, opened if it
//     is not; any other task goes to a general-purpose worker, never to
//     one the config set apart (worker-overrides), which only takes the
//     tasks named for it;
//   - a free worker gets the first task it may take, if its agent is
//     ready for one;
//   - a task no free worker can take opens the lowest worker not open,
//     within the configured count and, for a worker in the code when the
//     repo has stacks, within max-stacks, which also counts the stacks
//     kept with a closed worker's worktree; a worker already opening is
//     counted for the first task waiting on it;
//   - a held task (Error) is skipped;
//   - general-purpose workers are opened up to min-workers with nothing
//     queued, and never closed below it;
//   - a free worker no task waits for is closed after idle-close-minutes,
//     at once for a kind acw cannot reset between tasks, and never while
//     its worktree holds changes: the master is told instead.

type actionKind int

const (
	actAssign actionKind = iota
	actOpen
	actClose
	actDirty      // free long enough to close, but its worktree has changes
	actStacksFull // min-workers not met: max-stacks is reached
)

type poolAction struct {
	Kind    actionKind
	Worker  int
	Task    int  // actAssign
	Stacked bool // actOpen
}

// workerPoll is what one poll read about an open worker.
type workerPoll struct {
	// Ready is set when its agent can take a brief now: idle or done in
	// herdr and, for a claude worker, with acw's status line (dispatch
	// confirms its reset through it).
	Ready bool
	// Clean is set when its worktree has no change, or it has none.
	Clean bool
}

func schedule(p poolState, q taskQueue, polls map[int]workerPoll, now time.Time) []poolAction {
	var actions []poolAction
	n := len(p.Plan.Workers)
	spec := func(i int) workerSpec { return p.Plan.Workers[i-1] }
	open := map[int]bool{}
	stacks := p.Held
	var opening []int // generic workers coming up, not yet counted for a task
	for _, w := range p.Workers {
		open[w.Index] = true
		if w.Stacked || (w.State == workerOpening && p.Plan.Stacks && spec(w.Index).Dir == "") {
			stacks++
		}
		if w.State == workerOpening && !spec(w.Index).Overridden {
			opening = append(opening, w.Index)
		}
	}
	taken := map[int]bool{} // free workers given a task this poll
	canOpen := func(i int) bool {
		if open[i] || i < 1 || i > n {
			return false
		}
		return !p.Plan.Stacks || spec(i).Dir != "" || stacks < p.Plan.MaxStacks
	}
	openWorker := func(i int) {
		stacked := p.Plan.Stacks && spec(i).Dir == ""
		if stacked {
			stacks++
		}
		open[i] = true
		actions = append(actions, poolAction{Kind: actOpen, Worker: i, Stacked: stacked})
	}
	ready := func(w poolWorker) bool {
		return w.State == workerFree && !taken[w.Index] && polls[w.Index].Ready
	}

	for _, t := range q.Tasks {
		if t.Error != "" || len(waitingFor(t, p, q)) > 0 {
			continue
		}
		if t.Worker != 0 {
			if w := p.worker(t.Worker); w == nil {
				if canOpen(t.Worker) {
					openWorker(t.Worker)
				}
			} else if ready(*w) {
				taken[w.Index] = true
				actions = append(actions, poolAction{Kind: actAssign, Worker: w.Index, Task: t.ID})
			}
			continue
		}
		if i := slices.IndexFunc(p.Workers, func(w poolWorker) bool { return ready(w) && !spec(w.Index).Overridden }); i >= 0 {
			w := p.Workers[i]
			taken[w.Index] = true
			actions = append(actions, poolAction{Kind: actAssign, Worker: w.Index, Task: t.ID})
			continue
		}
		if len(opening) > 0 {
			opening = opening[1:]
			continue
		}
		for i := 1; i <= n; i++ {
			if !spec(i).Overridden && canOpen(i) {
				openWorker(i)
				break
			}
		}
	}

	// The floor: general-purpose workers kept open with nothing queued,
	// so a task never waits for an opening, and min-workers equal to
	// workers is the fixed swarm of before.
	up := 0
	for _, w := range p.Workers {
		if w.State != workerClosing {
			up++
		}
	}
	for _, a := range actions {
		if a.Kind == actOpen {
			up++
		}
	}
	for i := 1; i <= n && up < p.MinWorkers; i++ {
		if !spec(i).Overridden && canOpen(i) {
			openWorker(i)
			up++
		}
	}
	if up < p.MinWorkers && p.Plan.Stacks && stacks >= p.Plan.MaxStacks {
		actions = append(actions, poolAction{Kind: actStacksFull})
	}

	idle := time.Duration(p.IdleCloseMinutes) * time.Minute
	for _, w := range p.Workers {
		if w.State != workerFree || taken[w.Index] || up <= p.MinWorkers {
			continue
		}
		if slices.ContainsFunc(q.Tasks, func(t queuedTask) bool { return t.Worker == w.Index && t.Error == "" }) {
			continue
		}
		limit := idle
		if spec(w.Index).Kind != "claude" && w.Used {
			limit = 0
		}
		if now.Sub(w.Since) < limit {
			continue
		}
		kind := actClose
		if !polls[w.Index].Clean {
			kind = actDirty
		} else {
			up--
		}
		actions = append(actions, poolAction{Kind: kind, Worker: w.Index})
	}
	return actions
}
