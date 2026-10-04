package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/names"
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
		func(dir, branch string, _ runInfo) error { return wtm.Stop(dir, branch) })
}

func resumeStacks(repo string, out io.Writer) error {
	return eachStack(repo, out, "acw resume: workers' stacks started again. The first call to a service may fail while it starts up.",
		func(dir, branch string, run runInfo) error { return wtm.Start(dir, branch, run.Profile, out) })
}

// eachStack runs step on every worker worktree, on the branch it is on
// (the one wtm keys its stack by, which a worker never changes), and
// reports a failing one without stopping at it. The master hears of it
// once every worktree was tried.
func eachStack(repo string, out io.Writer, done string, step func(dir, branch string, run runInfo) error) error {
	if _, err := os.Stat(names.StatusDir(repo)); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no acw swarm in %s", repo)
	}
	run, err := readRunInfo(repo)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", names.RunFile(repo), err)
	}
	failed := 0
	stacked := stackedIn(repo)
	for _, dir := range teardown.WorkerWorktrees(repo) {
		name := filepath.Base(dir)
		branch, err := gitutil.CurrentBranch(dir)
		if err == nil {
			err = step(dir, branch, run)
		}
		// A worker beyond max-stacks, or whose adopt failed, has a worktree
		// wtm never gave a stack to: nothing to stop or start there. One wtm
		// gave a stack to and no longer finds it under its branch is a
		// failure: that stack is still up, out of acw's reach.
		if errors.Is(err, wtm.ErrNoStack) && !stacked(dir) {
			fmt.Fprintf(out, "%s: no stack, skipped.\n", name)
			continue
		}
		if errors.Is(err, wtm.ErrNoStack) {
			err = errors.New(wtm.StrandedHint(dir, branch))
		}
		if err != nil {
			failed++
			fmt.Fprintf(out, "%s: failed: %v\n", name, err)
			continue
		}
		fmt.Fprintf(out, "%s: done.\n", name)
	}
	inbox := run.Inbox
	if run.MasterName == "" {
		// A run.json from an older acw names no master: its inbox is all
		// there is.
		inbox = names.Inbox(repo)
	}
	if err := deliver(inbox, run.MasterName, done); err != nil {
		fmt.Fprintln(out, "message to the master:", err)
	}
	if failed > 0 {
		return fmt.Errorf("%d stack(s) failed, see above", failed)
	}
	return nil
}
