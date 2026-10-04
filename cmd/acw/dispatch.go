package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
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
			if err := herdr.AgentPrompt(t.name, text); err != nil {
				return err
			}
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

// switchWorkerBranch fetches then puts the worker's worktree on br.Branch.
// Nothing is stashed: local changes make git or wtm refuse, and the error
// says the worker got nothing.
func switchWorkerBranch(repo string, t clearTarget, br branchRequest, out io.Writer) error {
	run, err := readRunInfo(repo)
	if err != nil {
		return fmt.Errorf("swarm run info unreadable: %w", err)
	}
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
		return fmt.Errorf("%s is checked out in %s: give the task to that worker, or have it leave the branch first", br.Branch, holder)
	}
	// wtm.Available first: no wtm call at all on a machine without it.
	step, err := chooseBranchStep(pw.Stacked, pw.Stacked && wtm.Available() && wtm.SwitchAvailable(), gitutil.HasBranch(wt, br.Branch))
	if err != nil {
		return fmt.Errorf("%s not put on %s, nothing was sent to it: %w", t.label, br.Branch, err)
	}
	if err := gitutil.Fetch(wt); err != nil {
		return fmt.Errorf("%s: %w", t.label, err)
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
		err = wtm.Switch(wt, br.Branch, from, run.Profile, out)
	} else {
		err = gitutil.Switch(wt, br.Branch, base, step.create)
	}
	if err != nil {
		return fmt.Errorf("%s not put on %s, nothing was sent to it (run the same command again once fixed): %w", t.label, br.Branch, err)
	}
	fmt.Fprintf(out, "%s: on %s.\n", t.label, br.Branch)
	return nil
}
