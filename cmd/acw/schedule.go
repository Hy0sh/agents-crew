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
//     ready for one; a task on a branch a clean free worker is on waits
//     for that worker, or, if it can't take it, for it to be parked;
//   - a free worker left on its task's branch goes back to its waiting
//     branch, unless a queued task is for that branch; a task waits
//     while its branch is being left;
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
	actPark       // free, clean, off its waiting branch: back on it
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
	// Branch is the branch its worktree is on, Home its waiting branch;
	// both "" for a worker outside the code.
	Branch, Home string
	// Parking is set while the watcher puts it back on Home, or after
	// that failed on this same Branch: not Ready, and not moved again.
	Parking bool
}

// offHome says a worker's worktree is on a branch other than its waiting
// one.
func (w workerPoll) offHome() bool { return w.Branch != "" && w.Branch != w.Home }

// freeHolder is the free worker whose worktree is on branch, nil for
// none or for no branch.
func freeHolder(p poolState, polls map[int]workerPoll, branch string) *poolWorker {
	if branch == "" {
		return nil
	}
	for i, w := range p.Workers {
		if w.State == workerFree && polls[w.Index].Branch == branch {
			return &p.Workers[i]
		}
	}
	return nil
}

// canTake says worker w, of spec s, may be given t: the one it names, or
// any it takes.
func canTake(s workerSpec, w poolWorker, t queuedTask) bool {
	return t.Worker == w.Index || t.Worker == 0 && s.takes(t)
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
	counted := map[int]bool{} // workers of a kind coming up, already counted for a task

	// Kept workers open with the swarm, whatever is queued.
	for i := 1; i <= n; i++ {
		if spec(i).Keep && canOpen(i) {
			openWorker(i)
		}
	}

	// A clean free worker on the branch of a task further down the queue is
	// kept for that one: given an earlier task elsewhere, it would leave
	// the branch, and the task on it would wait for its switch, then for
	// another worker to switch back to it.
	reserved := map[int]bool{}
	for _, t := range q.Tasks {
		if t.Error != "" || len(waitingFor(t, p, q)) > 0 {
			continue
		}
		if h := freeHolder(p, polls, t.Branch); h != nil && polls[h.Index].Clean && canTake(spec(h.Index), *h, t) {
			reserved[h.Index] = true
		}
	}
	claimed := map[string]bool{} // branches given a task this poll
	assign := func(w int, t queuedTask) {
		taken[w] = true
		actions = append(actions, poolAction{Kind: actAssign, Worker: w, Task: t.ID})
		if t.Branch != "" {
			claimed[t.Branch] = true
		}
	}
	for _, t := range q.Tasks {
		if t.Error != "" || len(waitingFor(t, p, q)) > 0 {
			continue
		}
		// Its branch is being left by a worker (back to its waiting
		// branch, or off to its next task), is another busy worker's task
		// branch, or went to a task earlier in this poll: it waits for it
		// to be free rather than fail on it and be held.
		if t.Branch != "" && (claimed[t.Branch] || slices.ContainsFunc(p.Workers, func(w poolWorker) bool {
			return polls[w.Index].Parking && polls[w.Index].Branch == t.Branch ||
				w.State == workerBusy && (w.TaskBranch == t.Branch || polls[w.Index].Branch == t.Branch)
		})) {
			continue
		}
		// Its branch is held by a clean free worker: the task is for that
		// one, once ready; if it can't take it, it goes back to its waiting
		// branch first (see the parking below). Handed to another worker,
		// the branch would be taken back from one the next poll may give
		// another task, and the two would race on its worktree. A dirty
		// holder can't be moved: the task goes on, and is held with why.
		if h := freeHolder(p, polls, t.Branch); h != nil && polls[h.Index].Clean {
			if canTake(spec(h.Index), *h, t) {
				// Kept for it: neither parked nor closed while it gets ready.
				if ready(*h) {
					assign(h.Index, t)
				}
				taken[h.Index] = true
			}
			continue
		}
		if t.Worker != 0 {
			if w := p.worker(t.Worker); w == nil {
				if canOpen(t.Worker) {
					openWorker(t.Worker)
				}
			} else if ready(*w) {
				assign(w.Index, t)
			}
			continue
		}
		if i := slices.IndexFunc(p.Workers, func(w poolWorker) bool { return ready(w) && !reserved[w.Index] && spec(w.Index).takes(t) }); i >= 0 {
			assign(p.Workers[i].Index, t)
			continue
		}
		// A task of a kind waits for a worker that takes it: one coming
		// up, else the lowest one not open. Never a general-purpose one.
		if t.Kind != "" {
			if i := slices.IndexFunc(p.Workers, func(w poolWorker) bool {
				return w.State == workerOpening && spec(w.Index).takes(t) && !counted[w.Index]
			}); i >= 0 {
				counted[p.Workers[i].Index] = true
				continue
			}
			for i := 1; i <= n; i++ {
				if spec(i).takes(t) && canOpen(i) {
					openWorker(i)
					break
				}
			}
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
	// A kept worker is on top of it, not part of it.
	up := 0
	for _, w := range p.Workers {
		if w.State != workerClosing && !spec(w.Index).Keep {
			up++
		}
	}
	for _, a := range actions {
		if a.Kind == actOpen && !spec(a.Worker).Keep {
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

	// A free worker left on its task's branch goes back to its waiting
	// branch, unless a queued task it can take is for that branch: it
	// may get it. Neither one is closed this poll.
	for _, w := range p.Workers {
		poll := polls[w.Index]
		if w.State != workerFree || taken[w.Index] {
			continue
		}
		if poll.Parking {
			taken[w.Index] = true
			continue
		}
		if !poll.offHome() || !poll.Clean || slices.ContainsFunc(q.Tasks, func(t queuedTask) bool {
			return t.Error == "" && t.Branch == poll.Branch && canTake(spec(w.Index), w, t)
		}) {
			continue
		}
		taken[w.Index] = true
		actions = append(actions, poolAction{Kind: actPark, Worker: w.Index})
	}

	idle := time.Duration(p.IdleCloseMinutes) * time.Minute
	for _, w := range p.Workers {
		if w.State != workerFree || taken[w.Index] || spec(w.Index).Keep || up <= p.MinWorkers {
			continue
		}
		if slices.ContainsFunc(q.Tasks, func(t queuedTask) bool {
			return t.Error == "" && (t.Worker == w.Index || t.Worker == 0 && t.Kind != "" && spec(w.Index).takes(t))
		}) {
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
