package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/teardown"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// The elastic pool: acw starts no worker, the master queues tasks, and
// acw's watcher opens a worker when a task waits and none is free, up to
// the configured count, then closes one left free too long. What to do,
// and in which order, is the master's call; where and when it runs is
// acw's, from rules a model cannot bend (see schedule).
//
// Two files hold it, both under one lock: pool.json, what a worker is
// opened with and each open worker's state, and queue.json, the tasks
// waiting. The master changes the queue (acw queue) and frees a worker
// (acw done) while the watcher reads both and acts on them.

const (
	workerOpening = "opening" // worktree, pane, agent and stack coming up
	workerFree    = "free"    // open, no task
	workerBusy    = "busy"    // has a task, until acw done
	workerClosing = "closing" // being torn down
)

// poolWorker is one open worker.
type poolWorker struct {
	Index int    `json:"index"`
	Pane  string `json:"pane,omitempty"`
	// Worktree is "" for a worker outside the code. Its name carries the
	// stamp of the opening, not the run's: a worker closed then opened
	// again must not collide with the branch its first opening left.
	Worktree string    `json:"worktree,omitempty"`
	Stacked  bool      `json:"stacked,omitempty"`
	State    string    `json:"state"`
	Task     int       `json:"task,omitempty"`
	Since    time.Time `json:"since"`
	// Used is set once it got a task: a kind acw cannot reset between
	// tasks closes as soon as it is free again, not right after opening.
	Used bool `json:"used,omitempty"`
	// TaskBranch is the --branch of the task it is busy with: a task on
	// the same branch waits for that one to end (see schedule).
	TaskBranch string `json:"task_branch,omitempty"`
	// Dispatching is the task being handed to it, until its brief is
	// typed: a watcher that dies in between leaves it busy with a task it
	// never got, which the next watcher puts back (see recoverPool).
	Dispatching *queuedTask `json:"dispatching,omitempty"`
}

func (w poolWorker) label() string { return fmt.Sprintf("worker%d", w.Index) }

// poolState is pool.json.
type poolState struct {
	Plan             provisionPlan `json:"plan"`
	MinWorkers       int           `json:"min_workers"`
	IdleCloseMinutes int           `json:"idle_close_minutes"`
	Workers          []poolWorker  `json:"workers"`
	// Held is how many stacks still take room with no open worker: kept
	// with their worktree because wtm could not remove them (see
	// teardown.Worktree). Read from the disk at every poll, never saved.
	Held int `json:"-"`
}

// heldStacks counts the worker worktrees of repo that acw got a stack and
// that no open worker holds: each is a stack that may still run, unless
// wtm lists it with none (a wtm remove run by hand), and the mark goes.
func heldStacks(repo string, p poolState) int {
	n := 0
	var gone map[string]bool // asked of wtm once, and only if needed
	for _, dir := range teardown.WorkerWorktrees(repo) {
		open := slices.ContainsFunc(p.Workers, func(w poolWorker) bool { return realPath(w.Worktree) == realPath(dir) })
		if open || !teardown.Stacked(dir) {
			continue
		}
		if gone == nil {
			gone = map[string]bool{}
			if paths, err := wtm.Adoptable(repo); err == nil {
				gone = paths
			}
		}
		if gone[realPath(dir)] {
			if err := teardown.Unmark(dir); err != nil {
				fmt.Fprintln(os.Stderr, "forgetting a removed stack:", err)
			}
			continue
		}
		n++
	}
	return n
}

func (p *poolState) worker(index int) *poolWorker {
	for i := range p.Workers {
		if p.Workers[i].Index == index {
			return &p.Workers[i]
		}
	}
	return nil
}

func (p *poolState) remove(index int) {
	p.Workers = slices.DeleteFunc(p.Workers, func(w poolWorker) bool { return w.Index == index })
}

// queuedTask is one task waiting for a worker.
type queuedTask struct {
	ID     int    `json:"id"`
	Brief  string `json:"brief"` // copied in: the file may change once queued
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
	// Worker, when set, is the only worker the task may go to (a fix
	// after a KO goes back to whoever has the context), 0 otherwise.
	Worker int `json:"worker,omitempty"`
	// Kind, when set, sends it only to the workers whose tasks list it
	// (a reviewer for need-review), never to a general-purpose one.
	Kind string `json:"kind,omitempty"`
	// After lists the tasks it waits for: it goes out once each is ended,
	// neither queued nor on a busy worker (a rebase that needs the pushed
	// result of the task before it).
	After []int `json:"after,omitempty"`
	// AfterMerge lists the tasks whose PR it waits to see merged: ended
	// is not enough when the follow-up builds on the merged code, and
	// acw done comes before the review.
	AfterMerge []int     `json:"after_merge,omitempty"`
	AddedAt    time.Time `json:"added_at"`
	// Error is why handing it out failed: it is skipped until the master
	// moves or removes it, rather than retried every few seconds.
	Error string `json:"error,omitempty"`
}

type taskQueue struct {
	NextID int          `json:"next_id"`
	Tasks  []queuedTask `json:"tasks"`
	// PRs is the PR each ended task left (its worker's pr_url at acw
	// done), while a task waits for its merge; Merged, the tasks whose PR
	// the PR watch saw merged.
	PRs    map[int]string `json:"prs,omitempty"`
	Merged []int          `json:"merged,omitempty"`
}

// waitingFor is what of t's After is not ended yet, still queued or the
// task of a busy worker, and what of its AfterMerge has no merged PR yet.
func waitingFor(t queuedTask, p poolState, q taskQueue) []int {
	var out []int
	for _, id := range t.After {
		queued := slices.ContainsFunc(q.Tasks, func(x queuedTask) bool { return x.ID == id })
		running := slices.ContainsFunc(p.Workers, func(w poolWorker) bool { return w.State == workerBusy && w.Task == id })
		if queued || running {
			out = append(out, id)
		}
	}
	for _, id := range t.AfterMerge {
		if !slices.Contains(q.Merged, id) {
			out = append(out, id)
		}
	}
	return out
}

// mergedPR records that the PR at url was merged: the tasks that left it
// count as merged for AfterMerge. It reports whether one did.
func (q *taskQueue) mergedPR(url string) bool {
	found := false
	for id, u := range q.PRs {
		if u == url {
			q.Merged = append(q.Merged, id)
			delete(q.PRs, id)
			found = true
		}
	}
	return found
}

func taskList(ids []int) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(s, ", ")
}

// withPool runs fn on the pool and the queue under the lock, and writes
// back what fn says it changed.
func withPool(repo string, fn func(p *poolState, q *taskQueue) (changed bool, err error)) error {
	lock, err := os.OpenFile(names.PoolLock(repo), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("no acw swarm in %s", repo)
		}
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	p, q, err := readPool(repo)
	if err != nil {
		return err
	}
	changed, err := fn(&p, &q)
	if err != nil || !changed {
		return err
	}
	if err := writeJSON(names.PoolFile(repo), p); err != nil {
		return err
	}
	return writeJSON(names.QueueFile(repo), q)
}

// readPool reads both files without the lock: each is replaced by a
// rename, so a reader sees a whole one. A missing queue is an empty one.
func readPool(repo string) (poolState, taskQueue, error) {
	var q taskQueue
	p, err := readPoolFile(repo)
	if err != nil {
		return p, q, err
	}
	content, err := os.ReadFile(names.QueueFile(repo))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return p, q, err
	}
	if err == nil {
		if err := json.Unmarshal(content, &q); err != nil {
			return p, q, fmt.Errorf("%s: %w", names.QueueFile(repo), err)
		}
	}
	return p, q, nil
}

// readPoolFile reads pool.json alone, for what does not need the queue.
func readPoolFile(repo string) (poolState, error) {
	var p poolState
	content, err := os.ReadFile(names.PoolFile(repo))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return p, fmt.Errorf("no acw swarm in %s", repo)
		}
		return p, err
	}
	if err := json.Unmarshal(content, &p); err != nil {
		return p, fmt.Errorf("%s: %w", names.PoolFile(repo), err)
	}
	return p, nil
}

func writeJSON(path string, v any) error {
	content, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(content, '\n'), 0o644)
}

// workerArg turns worker3, or its herdr name, into 3 (see clearLabel).
func workerArg(repo, arg string) (int, error) {
	_, index, err := clearLabel(arg, names.Slug(repo))
	return index, err
}

// addOptions are acw queue add's flags.
type addOptions struct {
	Branch            branchRequest
	Worker, Kind      string
	After, AfterMerge []int
	Top               bool // first in the queue instead of last
}

// queueAdd queues the brief at path, as o says.
func queueAdd(repo, path string, o addOptions, now time.Time, out io.Writer) error {
	br, worker, kind, after, afterMerge, top := o.Branch, o.Worker, o.Kind, o.After, o.AfterMerge, o.Top
	if err := br.check(); err != nil {
		return err
	}
	text, err := readBrief(path)
	if err != nil {
		return err
	}
	index := 0
	if worker != "" {
		if index, err = workerArg(repo, worker); err != nil {
			return err
		}
	}
	return withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		if index > len(p.Plan.Workers) {
			return false, fmt.Errorf("worker%d is beyond this swarm's %d workers", index, len(p.Plan.Workers))
		}
		// A kind no worker takes would wait forever; a worker named with
		// --worker must take the kind it is given.
		if kind != "" {
			if !slices.ContainsFunc(p.Plan.Workers, func(w workerSpec) bool { return slices.Contains(w.Tasks, kind) }) {
				return false, fmt.Errorf("--kind %s: no worker takes it; list it under tasks in a worker-overrides entry", kind)
			}
			if index != 0 && !slices.Contains(p.Plan.Workers[index-1].Tasks, kind) {
				return false, fmt.Errorf("--kind %s: worker%d does not take it", kind, index)
			}
		}
		// Only a task that was queued can be waited for: one already ended
		// is no wait at all, a number never given is a typo.
		for _, id := range after {
			if id < 1 || id > q.NextID {
				return false, fmt.Errorf("--after %d: no task #%d was ever queued here (the last is #%d)", id, id, q.NextID)
			}
		}
		// A merge only the PR watch can see; a task ended without a PR
		// has none to wait for.
		if len(afterMerge) > 0 && !p.Plan.PRWatch {
			return false, errors.New("--after-merge needs pr-watch: acw learns of a merge from it")
		}
		for _, id := range afterMerge {
			if id < 1 || id > q.NextID {
				return false, fmt.Errorf("--after-merge %d: no task #%d was ever queued here (the last is #%d)", id, id, q.NextID)
			}
			pending := slices.ContainsFunc(q.Tasks, func(x queuedTask) bool { return x.ID == id }) ||
				slices.ContainsFunc(p.Workers, func(w poolWorker) bool { return w.State == workerBusy && w.Task == id })
			if _, open := q.PRs[id]; !pending && !open && !slices.Contains(q.Merged, id) {
				return false, fmt.Errorf("--after-merge %d: task #%d ended without a PR, there is no merge to wait for", id, id)
			}
		}
		q.NextID++
		t := queuedTask{ID: q.NextID, Brief: text, Branch: br.Branch, Base: br.Base, Worker: index, Kind: kind, After: after, AfterMerge: afterMerge, AddedAt: now}
		if top {
			q.Tasks = slices.Insert(q.Tasks, 0, t)
		} else {
			q.Tasks = append(q.Tasks, t)
		}
		fmt.Fprintf(out, "task #%d queued, position %d of %d", t.ID, slices.IndexFunc(q.Tasks, func(x queuedTask) bool { return x.ID == t.ID })+1, len(q.Tasks))
		if waits := waitingFor(t, *p, *q); len(waits) > 0 {
			fmt.Fprintf(out, "; it waits for %s (ended with acw done, or its PR merged with --after-merge)", taskList(waits))
		}
		fmt.Fprintln(out, ".")
		return true, nil
	})
}

// queueMove puts task id at position pos (1 is next), and clears its
// error: moving it is the master saying it may go again.
func queueMove(repo string, id, pos int, out io.Writer) error {
	return withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		i := slices.IndexFunc(q.Tasks, func(t queuedTask) bool { return t.ID == id })
		if i < 0 {
			return false, fmt.Errorf("no task #%d in the queue", id)
		}
		t := q.Tasks[i]
		t.Error = ""
		q.Tasks = slices.Delete(q.Tasks, i, i+1)
		pos = min(max(pos, 1), len(q.Tasks)+1)
		q.Tasks = slices.Insert(q.Tasks, pos-1, t)
		fmt.Fprintf(out, "task #%d at position %d of %d.\n", id, pos, len(q.Tasks))
		return true, nil
	})
}

func queueRemove(repo string, id int, out io.Writer) error {
	return withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		i := slices.IndexFunc(q.Tasks, func(t queuedTask) bool { return t.ID == id })
		if i < 0 {
			return false, fmt.Errorf("no task #%d in the queue", id)
		}
		q.Tasks = slices.Delete(q.Tasks, i, i+1)
		fmt.Fprintf(out, "task #%d removed.\n", id)
		// What waited for it would otherwise go out without the result it
		// waited for: it is held, for the master to move or remove.
		for j := range q.Tasks {
			if t := &q.Tasks[j]; t.Error == "" && (slices.Contains(t.After, id) || slices.Contains(t.AfterMerge, id)) {
				t.Error = fmt.Sprintf("the task it waited for, #%d, was removed", id)
				fmt.Fprintf(out, "task #%d held: it waited for #%d (move it to let it go anyway, or remove it).\n", t.ID, id)
			}
		}
		return true, nil
	})
}

// markDone frees a busy worker. A worker already free is not an error.
// task, when not 0, is the task the caller means to end: a second done
// for a task already over could otherwise free a worker the watcher has
// just handed the next one, which would then get a third brief on top.
func markDone(repo string, index, task int, now time.Time) (string, int, error) {
	var msg string
	var finished int
	err := withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		w := p.worker(index)
		if w == nil {
			return false, fmt.Errorf("worker%d is not open", index)
		}
		if w.State != workerBusy {
			msg = fmt.Sprintf("%s is already %s.", w.label(), w.State)
			return false, nil
		}
		if task != 0 && w.Task != task {
			return false, fmt.Errorf("%s is on task #%d, not #%d: nothing changed", w.label(), w.Task, task)
		}
		msg = fmt.Sprintf("%s is free (task #%d done).", w.label(), w.Task)
		finished = w.Task
		w.State, w.Task, w.Since, w.TaskBranch, w.Dispatching = workerFree, 0, now, "", nil
		afterMergeOf(q, finished, prURL(repo, w.label()), &msg)
		return true, nil
	})
	return msg, finished, err
}

// prURL is the PR a worker's status file names, normalized, "" without.
func prURL(repo, label string) string {
	s, _ := readWorkerStatus(filepath.Join(names.StatusDir(repo), label+".json"))
	if s.PRURL == "" {
		return ""
	}
	return normalizePRURL(s.PRURL)
}

// afterMergeOf keeps the PR task id ended with, for a task waiting for
// its merge, now or queued later. Without a PR, the tasks that wait for
// it would wait forever: they are held.
// ponytail: the PR of a task closed unmerged stays in q.PRs, a few bytes
// each; prune when a swarm lives long enough to matter.
func afterMergeOf(q *taskQueue, id int, url string, msg *string) {
	if url != "" {
		if q.PRs == nil {
			q.PRs = map[int]string{}
		}
		q.PRs[id] = url
		return
	}
	for j := range q.Tasks {
		if t := &q.Tasks[j]; t.Error == "" && slices.Contains(t.AfterMerge, id) {
			t.Error = fmt.Sprintf("#%d ended without a PR in its worker's status: no merge to wait for", id)
			*msg += fmt.Sprintf(" Task #%d held: it waited for the merge of #%d's PR, and there is none.", t.ID, id)
		}
	}
}

// poolMemory is what the watcher remembers of the pool from one poll to
// the next, and nowhere else: lost with the watcher, it costs at most a
// message told twice or a park tried again.
type poolMemory struct {
	// dirtyTold keeps the master from hearing about the same dirty
	// worktree every poll, stacksTold about the same short floor.
	dirtyTold  map[int]bool
	stacksTold bool
	// parking is the branch each worker is being moved off (see runPool),
	// parkFailed the one it failed to be moved off: an entry for another
	// branch than the worker's current one is stale. parkFree writes them
	// from its goroutine, hence mu.
	mu                  sync.Mutex
	parking, parkFailed map[int]string
	// leaving is the branch each worker is leaving for the task it was
	// just given (see runPool): its stack stays indexed there until its
	// switch is over, and a task on that branch would fail on it.
	leaving map[int]string
}

func newPoolMemory() *poolMemory {
	return &poolMemory{dirtyTold: map[int]bool{}, parking: map[int]string{}, parkFailed: map[int]string{}, leaving: map[int]string{}}
}

// pollWorkers reads what schedule needs about each free worker: whether
// its agent can take a brief, the branch it is on, and, once it could be
// closed or must go back to its waiting branch, whether its worktree is
// clean (a git status, so only then).
func pollWorkers(p poolState, agents []herdr.Agent, statusDir string, now time.Time, mem *poolMemory) map[int]workerPoll {
	polls := map[int]workerPoll{}
	slug := names.Slug(p.Plan.Repo)
	idle := time.Duration(p.IdleCloseMinutes) * time.Minute
	for _, w := range p.Workers {
		if w.State != workerFree {
			// The branch it is on, which a task with no --branch had it cut
			// itself, or the one it is still leaving for its new task: a
			// task on it waits (see schedule).
			var poll workerPoll
			if w.Worktree != "" {
				poll.Branch, _ = gitutil.CurrentBranch(w.Worktree)
			}
			mem.mu.Lock()
			if from := mem.leaving[w.Index]; from != "" {
				poll.Branch, poll.Parking = from, true
			}
			mem.mu.Unlock()
			if poll.Branch != "" {
				polls[w.Index] = poll
			}
			continue
		}
		spec := p.Plan.Workers[w.Index-1]
		var poll workerPoll
		if a, ok := herdr.FindAgent(agents, names.Worker(slug, w.Index)); ok && (a.Status == "idle" || a.Status == "done") {
			_, err := os.Stat(filepath.Join(statusDir, w.label()+".usage.json"))
			poll.Ready = spec.Kind != "claude" || err == nil
		} else if ok && (a.Status == "working" || a.Status == "blocked") {
			poll.Active = true
		}
		if info, err := os.Stat(filepath.Join(statusDir, w.label()+".turn")); err == nil {
			poll.LastTurn = info.ModTime()
		}
		if w.Worktree != "" {
			poll.Home = homeBranch(w)
			poll.Branch, _ = gitutil.CurrentBranch(w.Worktree)
		}
		mem.mu.Lock()
		from := mem.parking[w.Index]
		if from == "" {
			from = mem.leaving[w.Index]
		}
		if from != "" {
			// Mid-switch, git may already say Home: the branch left counts.
			// leaving too: a done without a task id can free a worker its
			// new task is still switching.
			poll.Branch, poll.Parking, poll.Ready = from, true, false
		} else if mem.parkFailed[w.Index] != "" && mem.parkFailed[w.Index] == poll.Branch {
			poll.Parking, poll.Ready = true, false
		}
		mem.mu.Unlock()
		if now.Sub(w.Since) >= idle || spec.Kind != "claude" || poll.offHome() {
			poll.Clean = w.Worktree == "" || gitutil.Clean(w.Worktree)
		}
		polls[w.Index] = poll
	}
	return polls
}

// runPool applies one poll's schedule: the pool and queue change under
// the lock, checked again there, then the slow part of each action runs
// in its own goroutine (a dispatch waits for a reset, an opening for a
// stack). mem is what it remembers between polls (see poolMemory).
func runPool(repo string, p poolState, q taskQueue, polls map[int]workerPoll, now time.Time, mem *poolMemory) {
	actions := schedule(p, q, polls, now)
	full := slices.ContainsFunc(actions, func(a poolAction) bool { return a.Kind == actStacksFull })
	if full && !mem.stacksTold {
		tell(p.Plan, stacksFullMessage(p))
	}
	mem.stacksTold = full
	for index := range mem.dirtyTold {
		if w := p.worker(index); w == nil || w.State != workerFree {
			delete(mem.dirtyTold, index)
		}
	}
	if len(actions) == 0 {
		return
	}
	type assignment struct {
		w poolWorker
		t queuedTask
	}
	var assigns []assignment
	var opens []int
	var closes, parks []poolWorker
	err := withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		changed := false
		for _, a := range actions {
			w := p.worker(a.Worker)
			switch a.Kind {
			case actAssign:
				i := slices.IndexFunc(q.Tasks, func(t queuedTask) bool { return t.ID == a.Task })
				if w == nil || w.State != workerFree || i < 0 {
					continue
				}
				t := q.Tasks[i]
				q.Tasks = slices.Delete(q.Tasks, i, i+1)
				w.State, w.Task, w.Since, w.Used, w.TaskBranch = workerBusy, t.ID, now, true, t.Branch
				w.Dispatching = &t
				assigns = append(assigns, assignment{*w, t})
			case actOpen:
				if w != nil {
					continue
				}
				nw := poolWorker{Index: a.Worker, State: workerOpening, Stacked: a.Stacked, Since: now}
				if p.Plan.Workers[a.Worker-1].Dir == "" {
					nw.Worktree = names.WorkerWorktree(repo, a.Worker, now.Format("20060102150405"))
				}
				p.Workers = append(p.Workers, nw)
				opens = append(opens, a.Worker)
			case actClose:
				if w == nil || w.State != workerFree {
					continue
				}
				w.State = workerClosing
				closes = append(closes, *w)
			case actDirty:
				if w != nil && !mem.dirtyTold[a.Worker] {
					mem.dirtyTold[a.Worker] = true
					tell(p.Plan, fmt.Sprintf("%s has been free for a while but its worktree has changes: acw keeps it open. Have them committed or dropped; it closes once its worktree is clean.", w.label()))
				}
				continue
			case actStacksFull:
				continue
			case actPark:
				if w != nil && w.State == workerFree {
					parks = append(parks, *w)
				}
				continue
			}
			changed = true
		}
		return changed, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pool:", err)
		return
	}
	for _, a := range assigns {
		from := polls[a.w.Index].Branch
		if from == "" || from == a.t.Branch {
			background(func() { assignTask(repo, p.Plan, a.w, a.t) })
			continue
		}
		mem.mu.Lock()
		mem.leaving[a.w.Index] = from
		mem.mu.Unlock()
		background(func() {
			assignTask(repo, p.Plan, a.w, a.t)
			mem.mu.Lock()
			delete(mem.leaving, a.w.Index)
			mem.mu.Unlock()
		})
	}
	for _, index := range opens {
		background(func() { openWorker(repo, index) })
	}
	for _, w := range parks {
		from := polls[w.Index].Branch
		mem.mu.Lock()
		mem.parking[w.Index] = from
		mem.mu.Unlock()
		background(func() { mem.parkFree(p, w, from) })
	}
	for _, w := range closes {
		why := fmt.Sprintf("free for %d min with nothing queued for it", p.IdleCloseMinutes)
		if p.Plan.Workers[w.Index-1].Kind != "claude" {
			why = "its task is done, and acw cannot reset its context for another"
		}
		background(func() { closeWorker(repo, w, why) })
	}
}

// parkFree puts a free worker back on its waiting branch, its wtm output
// in the watcher's log. A failure is told to the master once: the worker
// is not moved again while it stays on that branch.
func (mem *poolMemory) parkFree(p poolState, w poolWorker, from string) {
	err := parkWorker(p, w, os.Stderr)
	mem.mu.Lock()
	delete(mem.parking, w.Index)
	if err != nil {
		mem.parkFailed[w.Index] = from
	} else {
		delete(mem.parkFailed, w.Index)
	}
	mem.mu.Unlock()
	if err != nil {
		tell(p.Plan, fmt.Sprintf("%s is free but stays on %s: %v", w.label(), from, err))
	}
}

// stacksFullMessage tells the master why fewer than min-workers are open.
func stacksFullMessage(p poolState) string {
	msg := fmt.Sprintf("acw keeps fewer than min-workers (%d) open: max-stacks (%d) is reached", p.MinWorkers, p.Plan.MaxStacks)
	if p.Held == 0 {
		return msg + ". Tell me: max-stacks is below min-workers."
	}
	return msg + fmt.Sprintf(", %d of them by worktrees no open worker holds (left by an earlier run). Tell me: `wtm list` in %s shows them, and `wtm remove <branch>` frees the ones no longer needed.", p.Held, p.Plan.Repo)
}

// inflight counts what the pool runs in the background: the watcher
// waits for it before it stops (see runWatch).
var inflight sync.WaitGroup

func background(f func()) {
	inflight.Go(f)
}

// assignTask hands a queued task to the worker the pool gave it to: the
// same steps as acw dispatch. A failure frees the worker and puts the
// task back first in the queue, held with the reason, for the master to
// move once it is fixed.
func assignTask(repo string, plan provisionPlan, w poolWorker, t queuedTask) {
	var steps bytes.Buffer
	err := dispatchWorker(repo, w.label(), t.Brief, branchRequest{Branch: t.Branch, Base: t.Base}, io.MultiWriter(os.Stderr, &steps))
	if err == nil {
		_ = withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
			pw := p.worker(w.Index)
			if pw == nil || pw.Dispatching == nil || pw.Dispatching.ID != t.ID {
				return false, nil
			}
			pw.Dispatching = nil
			return true, nil
		})
		msg := fmt.Sprintf("task #%d → %s: %s", t.ID, w.label(), firstLine(t.Brief))
		// What the branch step found (caught up with origin, local commits)
		// is for the master, not only the watcher's log.
		if note := branchNote(steps.String(), w.label()); note != "" {
			msg += " (" + note + ")"
		}
		tell(plan, msg)
		return
	}
	t.Error = err.Error()
	_ = withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		if pw := p.worker(w.Index); pw != nil && pw.State == workerBusy && pw.Task == t.ID {
			pw.State, pw.Task, pw.Since, pw.TaskBranch, pw.Dispatching = workerFree, 0, time.Now(), "", nil
		}
		q.Tasks = slices.Insert(q.Tasks, 0, t)
		return true, nil
	})
	tell(plan, fmt.Sprintf("task #%d could not be given to %s, held first in the queue: %v. Fix the cause, then acw queue move %d 1 (or remove it).", t.ID, w.label(), err, t.ID))
}

// recoverPool undoes what a watcher that died left half done, before the
// next one polls: a task handed to a worker that never got its brief goes
// back first in the queue and the worker is free again; a worker caught
// opening or closing is dropped from the pool, which opens another when
// needed. The master hears of each.
// ponytail: a worktree or a stack a dropped opening already made stays
// behind, named in the message; acw stop removes it with the others.
func recoverPool(repo string) {
	var told []string
	var plan provisionPlan
	err := withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		plan = p.Plan
		changed := false
		for i := range p.Workers {
			w := &p.Workers[i]
			if w.State == workerBusy && w.Dispatching != nil && w.Dispatching.ID == w.Task {
				q.Tasks = slices.Insert(q.Tasks, 0, *w.Dispatching)
				told = append(told, fmt.Sprintf("task #%d never reached %s (the watcher stopped while handing it out): back first in the queue, %s free again.", w.Task, w.label(), w.label()))
				w.State, w.Task, w.Since, w.TaskBranch, w.Dispatching = workerFree, 0, time.Now(), "", nil
				changed = true
			}
		}
		kept := p.Workers[:0]
		for _, w := range p.Workers {
			if w.State != workerOpening && w.State != workerClosing {
				kept = append(kept, w)
				continue
			}
			msg := fmt.Sprintf("%s was %s when the watcher stopped: dropped from the pool.", w.label(), w.State)
			if w.Worktree != "" && fileExists(w.Worktree) {
				msg += fmt.Sprintf(" Its worktree %s may hold a stack: see wtm list; acw stop removes it.", w.Worktree)
			}
			told = append(told, msg)
			changed = true
		}
		p.Workers = kept
		return changed, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "recovering the pool:", err)
	}
	for _, msg := range told {
		fmt.Fprintln(os.Stderr, msg)
		tell(plan, msg)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// branchNote is the line switchWorkerBranch wrote for label, without the
// label, "" without one.
func branchNote(steps, label string) string {
	for _, line := range strings.Split(steps, "\n") {
		if note, ok := strings.CutPrefix(line, label+": on "); ok {
			return "on " + strings.TrimSuffix(note, ".")
		}
	}
	return ""
}

// renderQueue is acw queue's listing: the workers, then the tasks in the
// order they will go out.
func renderQueue(now time.Time, p poolState, q taskQueue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "workers (%d open of %d):\n", len(p.Workers), len(p.Plan.Workers))
	if len(p.Workers) == 0 {
		b.WriteString("  none\n")
	}
	for _, w := range p.Workers {
		fmt.Fprintf(&b, "  %s  %s", w.label(), w.State)
		if w.Task != 0 {
			fmt.Fprintf(&b, " #%d", w.Task)
		}
		fmt.Fprintf(&b, " since %s\n", age(now, w.Since))
	}
	fmt.Fprintf(&b, "queue (%d):\n", len(q.Tasks))
	if len(q.Tasks) == 0 {
		b.WriteString("  empty\n")
	}
	for i, t := range q.Tasks {
		fmt.Fprintf(&b, "  %d. #%d  %s", i+1, t.ID, firstLine(t.Brief))
		if t.Worker != 0 {
			fmt.Fprintf(&b, " · for worker%d", t.Worker)
		}
		if t.Kind != "" {
			fmt.Fprintf(&b, " · kind %s", t.Kind)
		}
		if t.Branch != "" {
			fmt.Fprintf(&b, " · on %s", t.Branch)
		}
		if waits := waitingFor(t, p, q); len(waits) > 0 {
			fmt.Fprintf(&b, " · waiting for %s", taskList(waits))
		}
		if i := slices.IndexFunc(p.Workers, func(w poolWorker) bool {
			if t.Branch == "" || w.State != workerBusy {
				return false
			}
			on, _ := gitutil.CurrentBranch(w.Worktree)
			return w.TaskBranch == t.Branch || w.Worktree != "" && on == t.Branch
		}); i >= 0 {
			fmt.Fprintf(&b, " · waiting for %s to end #%d on that branch", p.Workers[i].label(), p.Workers[i].Task)
		}
		fmt.Fprintf(&b, " · queued %s", age(now, t.AddedAt))
		if t.Error != "" {
			fmt.Fprintf(&b, "\n     held: %s (move or remove it)", t.Error)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return oneLine(line)
}
