// Package teardown implements agents-crew's `stop`: releasing every
// worker's environment, the worktrees themselves, the shared status
// directory, and the Herdr workspace.
package teardown

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// Run tears down the swarm running in the current directory, if any,
// printing progress to stdout and non-fatal errors to stderr. It returns
// nil even when there was nothing to tear down. Scoped to the current
// directory, not global: a swarm running for a different repo is left
// alone, so several can run at once.
func Run() error {
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
	cleanupWorkerWorktrees(repo)

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

func cleanupWorkerWorktrees(repo string) {
	for _, dir := range WorkerWorktrees(repo) {
		name := filepath.Base(dir)
		branch, err := gitutil.CurrentBranch(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: resolving the branch: %v\n", name, err)
			continue
		}
		fmt.Printf("%s (%s):\n", name, branch)

		if wtm.Available() {
			// Best-effort: a worktree whose environment was never adopted
			// (provisioning failed, or MAX_STACKS left it without one)
			// makes these fail harmlessly, which is fine — the worktree
			// removal below still runs.
			fmt.Print("  stopping the environment... ")
			if err := wtm.Stop(dir, branch); err != nil {
				fmt.Println("nothing to stop.")
				// A repo wtm doesn't know, or a worktree never adopted, is
				// the ordinary case (no environment was ever given out) —
				// printing wtm's own "not registered" error under a line
				// that just said there was nothing to stop reads as a
				// failure when nothing failed.
				if !strings.Contains(err.Error(), "is not registered") {
					fmt.Fprintf(os.Stderr, "%s: wtm stop: %v\n", name, err)
				}
			} else {
				fmt.Print("done. Removing (containers, volumes, images)... ")
				if err := wtm.Remove(dir, branch); err != nil {
					fmt.Println("failed, see below.")
					fmt.Fprintf(os.Stderr, "%s: wtm remove: %v\n", name, err)
				} else {
					fmt.Println("done.")
				}
			}
		}

		if err := gitutil.WorktreeRemove(repo, dir); err != nil {
			fmt.Fprintf(os.Stderr, "%s: git worktree remove: %v\n", name, err)
			continue
		}
		// A worker takes each task on a new branch in its worktree: that
		// branch is its work, pushed or not, and stays. Only the branch acw
		// cut for it goes, and only if nothing was committed on it.
		if own := "agents/" + name; branch != own {
			fmt.Printf("  worktree removed, task branch %s kept.\n", branch)
			if err := gitutil.DeleteMergedBranch(repo, own); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s kept: %v\n", name, own, err)
			}
			continue
		}
		if err := gitutil.DeleteBranch(repo, branch); err != nil {
			fmt.Printf("  worktree removed, branch %s kept (see below).\n", branch)
			fmt.Fprintf(os.Stderr, "%s: git branch -D %s: %v\n", name, branch, err)
			continue
		}
		fmt.Printf("  worktree and branch %s removed.\n", branch)
	}
}
