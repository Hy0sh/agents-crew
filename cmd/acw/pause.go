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
	return eachStack(repo, out, "acw pause : stacks des workers arrêtées. Worktrees et agents intacts, rien à dispatcher qui ait besoin d'un environnement avant acw resume.",
		func(dir, branch string, _ runInfo) error { return wtm.Stop(dir, branch) })
}

func resumeStacks(repo string, out io.Writer) error {
	return eachStack(repo, out, "acw resume : stacks des workers relancées. Le premier appel à un service peut échouer le temps qu'il démarre.",
		func(dir, branch string, run runInfo) error { return wtm.Start(dir, branch, run.Profile, out) })
}

// eachStack runs step on every worker worktree, on the branch it is on
// (the one wtm keys its stack by, which a worker never changes), and
// reports a failing one without stopping at it. The master hears of it
// once every worktree was tried.
func eachStack(repo string, out io.Writer, done string, step func(dir, branch string, run runInfo) error) error {
	if _, err := os.Stat(names.StatusDir(repo)); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("aucun swarm acw dans %s", repo)
	}
	run, err := readRunInfo(repo)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", names.RunFile(repo), err)
	}
	failed := 0
	for _, dir := range teardown.WorkerWorktrees(repo) {
		name := filepath.Base(dir)
		branch, err := gitutil.CurrentBranch(dir)
		if err == nil {
			err = step(dir, branch, run)
		}
		if err != nil {
			failed++
			fmt.Fprintf(out, "%s : échec : %v\n", name, err)
			continue
		}
		fmt.Fprintf(out, "%s : fait.\n", name)
	}
	if err := appendLine(names.Inbox(repo), done); err != nil {
		fmt.Fprintln(out, "message au master:", err)
	}
	if failed > 0 {
		return fmt.Errorf("%d stack(s) en échec, voir ci-dessus", failed)
	}
	return nil
}
