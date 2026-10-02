package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

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

func dispatchWorker(repo, arg, briefPath string, br branchRequest, out io.Writer) error {
	text, err := readBrief(briefPath)
	if err != nil {
		return err
	}
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
	wtm    bool // wtm switch: the worker has a stack, which gets a fresh dump on the same ports
	create bool // the branch doesn't exist yet: cut it from the base
}

func chooseBranchStep(stacked, switchAvailable, exists bool) branchStep {
	return branchStep{wtm: stacked && switchAvailable, create: !exists}
}

// switchWorkerBranch fetches then puts the worker's worktree on br.Branch.
// Nothing is stashed: local changes make git or wtm refuse, and the error
// says the worker got nothing.
func switchWorkerBranch(repo string, t clearTarget, br branchRequest, out io.Writer) error {
	run, err := readRunInfo(repo)
	if err != nil {
		return fmt.Errorf("swarm run info unreadable: %w", err)
	}
	wt := names.WorkerWorktree(repo, t.index, run.Stamp)
	if _, err := os.Stat(wt); err != nil {
		return fmt.Errorf("%s has no worktree (a worker outside the code has no branch): --branch doesn't apply", t.label)
	}
	worktrees, err := gitutil.WorktreeBranches(repo)
	if err != nil {
		return err
	}
	if holder := branchHolder(worktrees, br.Branch, wt); holder != "" {
		return fmt.Errorf("%s is checked out in %s: give the task to that worker, or have it leave the branch first", br.Branch, holder)
	}
	if err := gitutil.Fetch(wt); err != nil {
		return fmt.Errorf("%s: %w", t.label, err)
	}
	base := br.Base
	if base == "" {
		base = gitutil.DefaultBaseRef(repo)
	}
	stacked := slices.Contains(run.Stacked, t.label)
	// wtm.Available first: no wtm call at all on a machine without it.
	step := chooseBranchStep(stacked, stacked && wtm.Available() && wtm.SwitchAvailable(), gitutil.HasBranch(wt, br.Branch))
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
