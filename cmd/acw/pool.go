package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
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
}

func (w poolWorker) label() string { return fmt.Sprintf("worker%d", w.Index) }

// poolState is pool.json.
type poolState struct {
	Plan             provisionPlan `json:"plan"`
	MinWorkers       int           `json:"min_workers"`
	IdleCloseMinutes int           `json:"idle_close_minutes"`
	Workers          []poolWorker  `json:"workers"`
	// Stranded lists the worktrees a close kept because wtm no longer
	// found their stack (see closeWorker): acw stop must keep them too.
	Stranded []string `json:"stranded,omitempty"`
}

// stackedIn says whether wtm gave the worktree at dir a stack, from the
// pool of repo's swarm: an open worker with one, or a worktree a close
// kept for that reason. False without a pool.
func stackedIn(repo string) func(dir string) bool {
	p, _, err := readPool(repo)
	return func(dir string) bool {
		if err != nil {
			return false
		}
		for _, w := range p.Workers {
			if w.Stacked && realPath(w.Worktree) == realPath(dir) {
				return true
			}
		}
		return slices.ContainsFunc(p.Stranded, func(s string) bool { return realPath(s) == realPath(dir) })
	}
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
	Worker  int       `json:"worker,omitempty"`
	AddedAt time.Time `json:"added_at"`
	// Error is why handing it out failed: it is skipped until the master
	// moves or removes it, rather than retried every few seconds.
	Error string `json:"error,omitempty"`
}

type taskQueue struct {
	NextID int          `json:"next_id"`
	Tasks  []queuedTask `json:"tasks"`
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
	var p poolState
	var q taskQueue
	content, err := os.ReadFile(names.PoolFile(repo))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return p, q, fmt.Errorf("no acw swarm in %s", repo)
		}
		return p, q, err
	}
	if err := json.Unmarshal(content, &p); err != nil {
		return p, q, fmt.Errorf("%s: %w", names.PoolFile(repo), err)
	}
	content, err = os.ReadFile(names.QueueFile(repo))
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

// queueAdd queues the brief at path, last, or first with top.
func queueAdd(repo, path string, br branchRequest, worker string, top bool, now time.Time, out io.Writer) error {
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
		q.NextID++
		t := queuedTask{ID: q.NextID, Brief: text, Branch: br.Branch, Base: br.Base, Worker: index, AddedAt: now}
		if top {
			q.Tasks = slices.Insert(q.Tasks, 0, t)
		} else {
			q.Tasks = append(q.Tasks, t)
		}
		fmt.Fprintf(out, "task #%d queued, position %d of %d.\n", t.ID, slices.IndexFunc(q.Tasks, func(x queuedTask) bool { return x.ID == t.ID })+1, len(q.Tasks))
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
		return true, nil
	})
}

// markDone frees a busy worker. A worker already free is not an error:
// the master may say twice that a task is over.
func markDone(repo string, index int, now time.Time) (string, error) {
	var msg string
	err := withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		w := p.worker(index)
		if w == nil {
			return false, fmt.Errorf("worker%d is not open", index)
		}
		if w.State != workerBusy {
			msg = fmt.Sprintf("%s is already %s.", w.label(), w.State)
			return false, nil
		}
		msg = fmt.Sprintf("%s is free (task #%d done).", w.label(), w.Task)
		w.State, w.Task, w.Since = workerFree, 0, now
		return true, nil
	})
	return msg, err
}

// pollWorkers reads what schedule needs about each free worker: whether
// its agent can take a brief, and, once it could be closed, whether its
// worktree is clean (a git status, so only then).
func pollWorkers(p poolState, agents []herdr.Agent, statusDir string, now time.Time) map[int]workerPoll {
	polls := map[int]workerPoll{}
	slug := names.Slug(p.Plan.Repo)
	idle := time.Duration(p.IdleCloseMinutes) * time.Minute
	for _, w := range p.Workers {
		if w.State != workerFree {
			continue
		}
		spec := p.Plan.Workers[w.Index-1]
		var poll workerPoll
		if a, ok := herdr.FindAgent(agents, names.Worker(slug, w.Index)); ok && (a.Status == "idle" || a.Status == "done") {
			_, err := os.Stat(filepath.Join(statusDir, w.label()+".usage.json"))
			poll.Ready = spec.Kind != "claude" || err == nil
		}
		if now.Sub(w.Since) >= idle || spec.Kind != "claude" {
			poll.Clean = w.Worktree == "" || gitutil.Clean(w.Worktree)
		}
		polls[w.Index] = poll
	}
	return polls
}

// runPool applies one poll's schedule: the pool and queue change under
// the lock, checked again there, then the slow part of each action runs
// in its own goroutine (a dispatch waits for a reset, an opening for a
// stack). dirtyTold keeps the master from hearing about the same dirty
// worktree every poll.
func runPool(repo string, p poolState, q taskQueue, polls map[int]workerPoll, now time.Time, dirtyTold map[int]bool) {
	actions := schedule(p, q, polls, now)
	for index := range dirtyTold {
		if w := p.worker(index); w == nil || w.State != workerFree {
			delete(dirtyTold, index)
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
	var closes []poolWorker
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
				w.State, w.Task, w.Since, w.Used = workerBusy, t.ID, now, true
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
				if w != nil && !dirtyTold[a.Worker] {
					dirtyTold[a.Worker] = true
					tell(p.Plan, fmt.Sprintf("%s has been free for a while but its worktree has changes: acw keeps it open. Have them committed or dropped; it closes once its worktree is clean.", w.label()))
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
		go assignTask(repo, p.Plan, a.w, a.t)
	}
	for _, index := range opens {
		go openWorker(repo, index)
	}
	for _, w := range closes {
		why := fmt.Sprintf("free for %d min with nothing queued for it", p.IdleCloseMinutes)
		if p.Plan.Workers[w.Index-1].Kind != "claude" {
			why = "its task is done, and acw cannot reset its context for another"
		}
		go closeWorker(repo, w, why)
	}
}

// assignTask hands a queued task to the worker the pool gave it to: the
// same steps as acw dispatch. A failure frees the worker and puts the
// task back first in the queue, held with the reason, for the master to
// move once it is fixed.
func assignTask(repo string, plan provisionPlan, w poolWorker, t queuedTask) {
	err := dispatchWorker(repo, w.label(), t.Brief, branchRequest{Branch: t.Branch, Base: t.Base}, os.Stderr)
	if err == nil {
		tell(plan, fmt.Sprintf("task #%d → %s: %s", t.ID, w.label(), firstLine(t.Brief)))
		return
	}
	t.Error = err.Error()
	_ = withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		if pw := p.worker(w.Index); pw != nil && pw.State == workerBusy && pw.Task == t.ID {
			pw.State, pw.Task, pw.Since = workerFree, 0, time.Now()
		}
		q.Tasks = slices.Insert(q.Tasks, 0, t)
		return true, nil
	})
	tell(plan, fmt.Sprintf("task #%d could not be given to %s, held first in the queue: %v. Fix the cause, then acw queue move %d 1 (or remove it).", t.ID, w.label(), err, t.ID))
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
		if t.Branch != "" {
			fmt.Fprintf(&b, " · on %s", t.Branch)
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
