package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

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
		func(dir, branch string, _ *poolWorker, _ string) error { return wtm.Stop(dir, branch) })
}

// resumeStacks starts again the stacks the workers held at the pause
// (see stack.go), on the profile the swarm was launched with, kept in its
// pool: a later config change or another --preset must not silently
// replace it.
func resumeStacks(repo string, out io.Writer) error {
	return eachStack(repo, out, "acw resume: the stacks the workers held are started again. The first call to a service may fail while it starts up.",
		func(dir, branch string, w *poolWorker, profile string) error {
			// The others take theirs with acw stack up when they need it.
			if w == nil || w.StackSince == nil {
				return errNoStackHeld
			}
			return wtm.Start(dir, branch, profile, out)
		})
}

var errNoStackHeld = errors.New("holds no stack")

// eachStack runs step on every worker worktree, on the branch it is on
// now (the one wtm keys its stack by: a worker moves it along with wtm
// switch at each task), and
// reports a failing one without stopping at it. The master hears of it
// once every worktree was tried. step gets the open worker of the
// worktree, nil for one no open worker holds.
func eachStack(repo string, out io.Writer, done string, step func(dir, branch string, w *poolWorker, profile string) error) error {
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
			// The worktree is <label>-<stamp>: the slot whose label it
			// starts with, dash included, so worker1 never takes worker12's.
			index := slices.IndexFunc(p.Plan.Workers, func(w workerSpec) bool { return strings.HasPrefix(name, w.label()+"-") }) + 1
			var w *poolWorker
			if i := slices.IndexFunc(p.Workers, func(w poolWorker) bool { return realPath(w.Worktree) == realPath(dir) }); i >= 0 {
				w = &p.Workers[i]
			}
			err = step(dir, branch, w, p.Plan.profileOf(index))
		}
		// A worker whose adopt failed has a worktree acw never got a stack:
		// nothing to stop or start there. One that got one and that wtm no
		// longer reaches is a failure: that stack may still be up, out of
		// acw's reach.
		if errors.Is(err, errNoStackHeld) {
			fmt.Fprintf(out, "%s: holds no stack, left without one.\n", name)
			continue
		}
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
