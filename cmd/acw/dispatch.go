package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// acw dispatch hands a worker its next task in one call: wait until it is
// idle, reset its context, type the brief. The master used to run acw
// clear then herdr agent prompt, and a forgotten clear left the previous
// task in the worker's context.

// dispatchSteps are dispatch's calls, in order, replaced in tests. branch
// is nil when no branch was asked for.
type dispatchSteps struct {
	ready  func() (clearTarget, error)
	branch func(clearTarget) error
	reset  func(clearTarget) error
	prompt func(clearTarget) error
}

// run stops at the first step that fails: a worker that could not be put
// on its branch gets neither the reset nor the brief.
func (s dispatchSteps) run() error {
	t, err := s.ready()
	if err != nil {
		return err
	}
	if s.branch != nil {
		if err := s.branch(t); err != nil {
			return err
		}
	}
	if err := s.reset(t); err != nil {
		if s.branch != nil {
			return fmt.Errorf("%w (its branch was already switched)", err)
		}
		return err
	}
	return s.prompt(t)
}

// readBrief is the brief to type, refused when it says nothing.
func readBrief(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("brief unreadable: %w", err)
	}
	text := strings.TrimSpace(string(content))
	if text == "" {
		return "", fmt.Errorf("%s is empty: nothing to dispatch", path)
	}
	return text, nil
}

// dispatchByHand is acw dispatch: the brief at briefPath to a free worker,
// held busy in the pool for the time so acw gives it nothing else, and
// left busy once dispatched, until acw done.
func dispatchByHand(repo, arg, briefPath string, br branchRequest, out io.Writer) error {
	if err := br.check(); err != nil {
		return err
	}
	text, err := readBrief(briefPath)
	if err != nil {
		return err
	}
	index, err := workerArg(repo, arg)
	if err != nil {
		return err
	}
	setState := func(from, to string) error {
		return withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
			w := p.worker(index)
			if w == nil {
				return false, fmt.Errorf("worker%d is not open: queue the task instead (acw queue add --worker worker%d)", index, index)
			}
			if w.State != from {
				return false, fmt.Errorf("worker%d is %s, not %s", index, w.State, from)
			}
			w.State, w.Task, w.Since, w.Used = to, 0, time.Now(), true
			return true, nil
		})
	}
	if err := setState(workerFree, workerBusy); err != nil {
		return err
	}
	if err := dispatchWorker(repo, arg, text, br, out); err != nil {
		_ = setState(workerBusy, workerFree)
		return err
	}
	return nil
}

func dispatchWorker(repo, arg, text string, br branchRequest, out io.Writer) error {
	steps := dispatchSteps{
		ready: func() (clearTarget, error) { return readyToClear(repo, arg, out) },
		reset: func(t clearTarget) error { return resetContext(t, out) },
		prompt: func(t clearTarget) error {
			// A message still waiting was about the previous task.
			statusDir := names.StatusDir(repo)
			_ = os.Remove(filepath.Join(statusDir, t.label+".tell"))
			if err := herdr.AgentPrompt(t.name, text); err != nil {
				return err
			}
			markTold(statusDir, t.label)
			fmt.Fprintf(out, "%s: dispatched.\n", t.label)
			return nil
		},
	}
	if br.Branch != "" {
		steps.branch = func(t clearTarget) error { return switchWorkerBranch(repo, t, br, out) }
	}
	return steps.run()
}

// branchRequest is dispatch's --branch and --base. An empty Branch leaves
// the worker where it is: the common case, where the worker names its
// branch itself once it has read its task.
type branchRequest struct {
	Branch, Base string
}

// check refuses a branch or a base git would read as an option: both go
// on git's command line as they are, and come from the master.
func (br branchRequest) check() error {
	for _, name := range []string{br.Branch, br.Base} {
		if strings.HasPrefix(name, "-") {
			return fmt.Errorf("%q starts with a dash: git would take it for an option", name)
		}
	}
	return nil
}

// branchHolder is the worktree other than self where branch is checked
// out, "" when none: git refuses a branch checked out twice, and the
// message should name who holds it before anything is fetched.
func branchHolder(worktrees map[string]string, branch, self string) string {
	own := realPath(self)
	for path, b := range worktrees {
		if b == branch && realPath(path) != own {
			return path
		}
	}
	return ""
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// branchStep is how dispatch puts a worker on its branch.
type branchStep struct {
	wtm    bool // wtm switch: the worker has a stack, which follows it to the branch, on the same ports
	create bool // the branch doesn't exist yet: cut it from the base
}

// chooseBranchStep refuses a worker with a stack when wtm has no switch:
// wtm keeps a stack under its branch, and a plain git switch leaves it
// under the old one, where acw stop and pause no longer find it.
func chooseBranchStep(stacked, switchAvailable, exists bool) (branchStep, error) {
	if stacked && !switchAvailable {
		return branchStep{}, fmt.Errorf("its stack would stay behind on its current branch: changing the branch of a worker with a stack needs wtm 0.26 or later (wtm switch)")
	}
	return branchStep{wtm: stacked, create: !exists}, nil
}

// releaseBranch takes branch back from the worktree at holder when it is a
// free worker's: a worker freed after asking for a review keeps its branch
// checked out, and git gives a branch to one worktree only, so the
// reviewer, then whoever fixes, could never check it out. The free worker
// goes back to the branch it opened on, through the same switch as a task
// (wtm switch for a stack, which follows it). A busy worker keeps its
// branch, and so does a free one with uncommitted work: nothing is lost.
// ponytail: a task handed to that free worker in the same poll may switch
// its worktree at the same moment; one of the two fails on git's lock and
// its task is held with the reason.
func releaseBranch(p poolState, holder, branch string, out io.Writer) error {
	i := slices.IndexFunc(p.Workers, func(w poolWorker) bool { return w.Worktree != "" && realPath(w.Worktree) == realPath(holder) })
	if i < 0 {
		return fmt.Errorf("%s is checked out in %s, which is no worker of this swarm: leave the branch there first", branch, holder)
	}
	hw := p.Workers[i]
	if hw.State != workerFree {
		return fmt.Errorf("%s is checked out by %s, %s: give the task to that worker, or wait until it is done", branch, hw.label(), hw.State)
	}
	return parkWorker(p, hw, out)
}

// homeBranch is the branch a worker's worktree opened on, its waiting
// branch.
func homeBranch(w poolWorker) string {
	return names.WorkerBranch(w.Index, strings.TrimPrefix(filepath.Base(w.Worktree), w.label()+"-"))
}

// parkWorker puts a worker's worktree back on its waiting branch, so that
// the branch of the task it ended is free for whoever takes it next: the
// watcher does it for a free worker (see schedule), releaseBranch for one
// that still holds the branch a task needs. A worktree with uncommitted
// changes is left alone: moving it would carry them along or fail.
func parkWorker(p poolState, w poolWorker, out io.Writer) error {
	if w.Worktree == "" {
		return nil
	}
	home := homeBranch(w)
	current, err := gitutil.CurrentBranch(w.Worktree)
	if err != nil || current == home {
		return err
	}
	if !gitutil.Clean(w.Worktree) {
		return fmt.Errorf("%s has uncommitted changes on %s in %s: not moved to its waiting branch, and %s can't go to another worker until they are committed or dropped", w.label(), current, w.Worktree, current)
	}
	if w.Stacked {
		if !wtm.Available() || !wtm.SwitchAvailable() {
			return fmt.Errorf("%s stays on %s: its stack would stay behind without wtm switch", w.label(), current)
		}
		// No stack on the waiting branch: nobody works there, and the next
		// task's switch starts a fresh one anyway.
		err = wtm.SwitchNoStart(w.Worktree, home, out)
	} else {
		err = gitutil.Switch(w.Worktree, home, "", false)
	}
	if err != nil {
		return fmt.Errorf("%s could not be moved off %s to its waiting branch: %w", w.label(), current, err)
	}
	fmt.Fprintf(out, "%s: off %s, back on its waiting branch %s, no stack until its next task.\n", w.label(), current, home)
	return nil
}

// switchWorkerBranch fetches then puts the worker's worktree on br.Branch.
// Nothing is stashed: local changes make git or wtm refuse, and the error
// says the worker got nothing.
func switchWorkerBranch(repo string, t clearTarget, br branchRequest, out io.Writer) error {
	p, _, err := readPool(repo)
	if err != nil {
		return err
	}
	pw := p.worker(t.index)
	if pw == nil || pw.Worktree == "" {
		return fmt.Errorf("%s has no worktree (a worker outside the code has no branch): --branch doesn't apply", t.label)
	}
	wt := pw.Worktree
	worktrees, err := gitutil.WorktreeBranches(repo)
	if err != nil {
		return err
	}
	if holder := branchHolder(worktrees, br.Branch, wt); holder != "" {
		if err := releaseBranch(p, holder, br.Branch, out); err != nil {
			return fmt.Errorf("%s not put on %s, nothing was sent to it: %w", t.label, br.Branch, err)
		}
	}
	// wtm.Available first: no wtm call at all on a machine without it.
	step, err := chooseBranchStep(pw.Stacked, pw.Stacked && wtm.Available() && wtm.SwitchAvailable(), gitutil.HasBranch(wt, br.Branch))
	if err != nil {
		return fmt.Errorf("%s not put on %s, nothing was sent to it: %w", t.label, br.Branch, err)
	}
	if err := gitutil.Fetch(wt); err != nil {
		return fmt.Errorf("%s: %w", t.label, err)
	}
	// A local copy of the branch can predate commits pushed since: a worker
	// put on it would rebase the old version and force-push over them.
	ahead, behind, err := gitutil.Divergence(wt, br.Branch)
	if err != nil {
		return fmt.Errorf("%s: %w", t.label, err)
	}
	if ahead > 0 && behind > 0 {
		return fmt.Errorf("%s not put on %s, nothing was sent to it: the local branch and origin/%s have diverged (%d commits only here, %d only on origin); reconcile them, or delete the local branch to start from origin's", t.label, br.Branch, br.Branch, ahead, behind)
	}
	base := br.Base
	if base == "" {
		base = gitutil.DefaultBaseRef(repo)
	}
	if step.wtm {
		from := ""
		if step.create {
			from = base
		}
		err = wtm.Switch(wt, br.Branch, from, p.Plan.profileOf(t.index), out)
	} else {
		err = gitutil.Switch(wt, br.Branch, base, step.create)
	}
	if err != nil {
		// wtm switch checks out first and brings the stack after: a failure
		// may leave the worktree on the branch with its stack not up yet.
		return fmt.Errorf("%s may not be on %s yet, or be on it with its environment not ready; nothing was sent to it: run the same command again to finish: %w", t.label, br.Branch, err)
	}
	switch {
	case behind > 0:
		if err := gitutil.FastForward(wt, br.Branch); err != nil {
			return fmt.Errorf("%s is on %s but %d commits behind origin's, and could not catch up; nothing was sent to it: %w", t.label, br.Branch, behind, err)
		}
		fmt.Fprintf(out, "%s: on %s, brought up to origin/%s (%d commits it lacked).\n", t.label, br.Branch, br.Branch, behind)
	case ahead > 0:
		fmt.Fprintf(out, "%s: on %s, with %d local commits not on origin.\n", t.label, br.Branch, ahead)
	default:
		fmt.Fprintf(out, "%s: on %s.\n", t.label, br.Branch)
	}
	return nil
}
