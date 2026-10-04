// Package teardown implements agents-crew's `stop`: releasing every
// worker's environment, the worktrees themselves, the shared status
// directory, and the Herdr workspace.
package teardown

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// Run tears down the swarm running in the current directory, if any,
// printing progress to stdout and non-fatal errors to stderr. It returns
// nil even when there was nothing to tear down. Scoped to the current
// directory, not global: a swarm running for a different repo is left
// alone, so several can run at once. stacked says whether wtm gave the
// worktree at a dir a stack (see Worktree).
func Run(stacked func(dir string) bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	agents, err := herdr.AgentList()
	if err != nil {
		return fmt.Errorf("herdr agent list: %w", err)
	}

	// By name, not by the master's pane cwd: with master-dir it runs
	// elsewhere, and the name already carries this directory's hash.
	repo := cwd
	master, ok := herdr.FindAgent(agents, names.Master(names.Slug(repo)))
	if !ok {
		fmt.Println("No acw master running for this directory.")
		return nil
	}
	workspaceID := master.WorkspaceID
	fmt.Printf("Stopping the swarm of %s (workspace %s).\n", repo, workspaceID)

	// Discovered by scanning .claude/worktrees/ for the workerN-* naming
	// convention, not by asking Herdr which agents are named "workerN":
	// a stack can be up (wtm adopt already ran) before the pane for it
	// even exists, let alone before `herdr agent start` names it — a stop
	// run during that window found nothing to clean up otherwise, leaving
	// real Docker stacks orphaned despite reporting success.
	cleanupWorkerWorktrees(repo, stacked)

	fmt.Print("Closing the Herdr workspace and its agents... ")
	if err := herdr.WorkspaceClose(workspaceID, true); err != nil {
		fmt.Println("failed.")
		return fmt.Errorf("herdr workspace close: %w", err)
	}
	fmt.Println("done.")

	statusDir := names.StatusDir(repo)
	if err := os.RemoveAll(statusDir); err != nil {
		fmt.Fprintf(os.Stderr, "removing %s: %v\n", statusDir, err)
	}

	fmt.Println("Swarm stopped.")
	return nil
}

// cleanupWorkerWorktrees is the slow half of a teardown: each worker's
// environment goes down one after another, and a Docker stack takes its
// time. It narrates every step for that reason — a silent minute reads as
// a hang, and the step that is running is the one worth naming when it
// does hang for real.
// WorkerWorktrees lists the worker worktrees of any run in repo, found on
// disk by their naming convention (see cleanupWorkerWorktrees for why not
// through Herdr). A worker outside the code has none, and is not listed.
func WorkerWorktrees(repo string) []string {
	matches, err := filepath.Glob(filepath.Join(names.WorktreesDir(repo), "worker*"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "looking for worker worktrees: %v\n", err)
		return nil
	}
	var dirs []string
	for _, dir := range matches {
		if !names.IsWorkerWorktree(filepath.Base(dir)) {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func cleanupWorkerWorktrees(repo string, stacked func(dir string) bool) {
	for _, dir := range WorkerWorktrees(repo) {
		Worktree(repo, dir, stacked(dir))
	}
}

// Worktree releases one worker worktree: its environment, the worktree,
// and the branch acw cut for it. The task branch it was left on stays.
// acw stop runs it on every worker; the elastic pool on a worker it
// closes. When wtm gave it a stack (stacked) but has none under its
// current branch, the worktree is kept and Worktree returns why: removed,
// it would leave that stack running with nothing left to find it by.
func Worktree(repo, dir string, stacked bool) (kept string) {
	name := filepath.Base(dir)
	branch, err := gitutil.CurrentBranch(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: resolving the branch: %v\n", name, err)
		return
	}
	fmt.Printf("%s (%s):\n", name, branch)

	if wtm.Available() {
		// Removed whether it runs or was stopped (acw pause, a reboot): a
		// stopped stack wtm still lists is one nobody will clean up.
		fmt.Print("  removing the environment (containers, volumes, images)... ")
		err := wtm.Remove(dir, branch)
		switch {
		case stacked && err != nil:
			// Not found under its branch, or not removed: either way it
			// may still run, and the worktree is what leads back to it.
			kept = wtm.StrandedHint(dir, branch)
			if !errors.Is(err, wtm.ErrNoStack) {
				kept = fmt.Sprintf("wtm remove failed (%v); run it again from %s", err, dir)
			}
			fmt.Printf("not removed, worktree kept: %s.\n", kept)
			return kept
		case errors.Is(err, wtm.ErrNoStack):
			// A worktree wtm never gave a stack to (beyond max-stacks, a
			// failed adopt, a repo wtm doesn't know): nothing to remove.
			fmt.Println("none.")
		case err != nil:
			fmt.Println("failed, see below.")
			fmt.Fprintf(os.Stderr, "%s: wtm remove: %v\n", name, err)
		default:
			fmt.Println("done.")
		}
	}

	if err := gitutil.WorktreeRemove(repo, dir); err != nil {
		fmt.Fprintf(os.Stderr, "%s: git worktree remove: %v\n", name, err)
		return
	}
	// A worker takes each task on a new branch in its worktree: that
	// branch is its work, pushed or not, and stays. Only the branch acw
	// cut for it goes, and only if nothing was committed on it.
	if own := "agents/" + name; branch != own {
		fmt.Printf("  worktree removed, task branch %s kept.\n", branch)
		if err := gitutil.DeleteMergedBranch(repo, own); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s kept: %v\n", name, own, err)
		}
		return
	}
	if err := gitutil.DeleteBranch(repo, branch); err != nil {
		fmt.Printf("  worktree removed, branch %s kept (see below).\n", branch)
		fmt.Fprintf(os.Stderr, "%s: git branch -D %s: %v\n", name, branch, err)
		return
	}
	fmt.Printf("  worktree and branch %s removed.\n", branch)
	return ""
}
