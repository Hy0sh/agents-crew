package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/teardown"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// pauseStacks and resumeStacks stop and restart the workers' stacks for a
// break (a weekend, the night), leaving worktrees, agents and the Herdr
// workspace alone: stopped stacks free the machine's memory, and resume
// brings them back where they were. The master is told in its inbox, so
// a worker's failing test right after resume is not taken for a bug.

func pauseStacks(repo string, out io.Writer) error {
	return eachStack(repo, out, "acw pause: workers' stacks stopped. Worktrees and agents are intact; dispatch nothing that needs an environment before acw resume.",
		func(dir, branch, _ string) error { return wtm.Stop(dir, branch) })
}

// resumeStacks starts the stacks on the profile the swarm was launched
// with, kept in its pool: a later config change or another --preset must
// not silently replace it.
func resumeStacks(repo string, out io.Writer) error {
	return eachStack(repo, out, "acw resume: workers' stacks started again. The first call to a service may fail while it starts up.",
		func(dir, branch, profile string) error { return wtm.Start(dir, branch, profile, out) })
}

// eachStack runs step on every worker worktree, on the branch it is on
// now (the one wtm keys its stack by: a worker moves it along with wtm
// switch at each task), and
// reports a failing one without stopping at it. The master hears of it
// once every worktree was tried.
func eachStack(repo string, out io.Writer, done string, step func(dir, branch, profile string) error) error {
	p, _, err := readPool(repo)
	if err != nil {
		return err
	}
	failed := 0
	for _, dir := range teardown.WorkerWorktrees(repo) {
		name := filepath.Base(dir)
		stacked := teardown.Stacked(dir)
		branch, err := gitutil.CurrentBranch(dir)
		switch {
		case err != nil:
		case branch == "HEAD" && stacked:
			err = errors.New(teardown.Repair(dir, branch, nil))
		case branch != "HEAD":
			err = step(dir, branch, p.Plan.Profile)
		}
		// A worker beyond max-stacks, or whose adopt failed, has a worktree
		// acw never got a stack: nothing to stop or start there. One that
		// got one and that wtm no longer reaches is a failure: that stack
		// may still be up, out of acw's reach.
		noStack := errors.Is(err, wtm.ErrNoStack) || errors.Is(err, wtm.ErrUnregistered)
		if !stacked && (noStack || branch == "HEAD") {
			fmt.Fprintf(out, "%s: no stack, skipped.\n", name)
			continue
		}
		if noStack {
			err = errors.New(teardown.Repair(dir, branch, err))
		}
		if err != nil {
			failed++
			fmt.Fprintf(out, "%s: failed: %v\n", name, err)
			continue
		}
		fmt.Fprintf(out, "%s: done.\n", name)
	}
	tell(p.Plan, done)
	if failed > 0 {
		return fmt.Errorf("%d stack(s) failed, see above", failed)
	}
	return nil
}
