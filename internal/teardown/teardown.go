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

	var workspaceID, repo string
	for _, a := range agents {
		if a.Cwd == cwd && names.IsMaster(a.Name) {
			workspaceID = a.WorkspaceID
			repo = a.Cwd
			break
		}
	}
	if workspaceID == "" {
		fmt.Println("Aucun master acw en cours pour ce répertoire.")
		return nil
	}
	fmt.Printf("Arrêt du swarm de %s (workspace %s).\n", repo, workspaceID)

	// Discovered by scanning .claude/worktrees/ for the workerN-* naming
	// convention, not by asking Herdr which agents are named "workerN":
	// a stack can be up (wtm adopt already ran) before the pane for it
	// even exists, let alone before `herdr agent start` names it — a stop
	// run during that window found nothing to clean up otherwise, leaving
	// real Docker stacks orphaned despite reporting success.
	cleanupWorkerWorktrees(repo)

	fmt.Print("Fermeture du workspace Herdr et de ses agents... ")
	if err := herdr.WorkspaceClose(workspaceID, true); err != nil {
		fmt.Println("échec.")
		return fmt.Errorf("herdr workspace close: %w", err)
	}
	fmt.Println("fait.")

	statusDir := names.StatusDir(repo)
	if err := os.RemoveAll(statusDir); err != nil {
		fmt.Fprintf(os.Stderr, "suppression de %s: %v\n", statusDir, err)
	}

	fmt.Println("Swarm arrêté.")
	return nil
}

// cleanupWorkerWorktrees is the slow half of a teardown: each worker's
// environment goes down one after another, and a Docker stack takes its
// time. It narrates every step for that reason — a silent minute reads as
// a hang, and the step that is running is the one worth naming when it
// does hang for real.
func cleanupWorkerWorktrees(repo string) {
	matches, err := filepath.Glob(filepath.Join(names.WorktreesDir(repo), "worker*"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "recherche des worktrees workers: %v\n", err)
		return
	}

	for _, dir := range matches {
		name := filepath.Base(dir)
		if !names.IsWorkerWorktree(name) {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}

		branch, err := gitutil.CurrentBranch(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: résolution de la branche: %v\n", name, err)
			continue
		}
		fmt.Printf("%s (%s) :\n", name, branch)

		if wtm.Available() {
			// Best-effort: a worktree whose environment was never adopted
			// (provisioning failed, or MAX_STACKS left it without one)
			// makes these fail harmlessly, which is fine — the worktree
			// removal below still runs.
			fmt.Print("  arrêt de l'environnement... ")
			if err := wtm.Stop(dir, branch); err != nil {
				fmt.Println("rien à arrêter.")
				// A repo wtm doesn't know, or a worktree never adopted, is
				// the ordinary case (no environment was ever given out) —
				// printing wtm's own "not registered" error under a line
				// that just said there was nothing to stop reads as a
				// failure when nothing failed.
				if !strings.Contains(err.Error(), "is not registered") {
					fmt.Fprintf(os.Stderr, "%s: wtm stop: %v\n", name, err)
				}
			} else {
				fmt.Print("fait. Suppression (conteneurs, volumes, images)... ")
				if err := wtm.Remove(dir, branch); err != nil {
					fmt.Println("échec, voir ci-dessous.")
					fmt.Fprintf(os.Stderr, "%s: wtm remove: %v\n", name, err)
				} else {
					fmt.Println("fait.")
				}
			}
		}

		if err := gitutil.WorktreeRemove(repo, dir); err != nil {
			fmt.Fprintf(os.Stderr, "%s: git worktree remove: %v\n", name, err)
			continue
		}
		if err := gitutil.DeleteBranch(repo, branch); err != nil {
			fmt.Printf("  worktree supprimé, branche %s conservée (voir ci-dessous).\n", branch)
			fmt.Fprintf(os.Stderr, "%s: git branch -D %s: %v\n", name, branch, err)
			continue
		}
		fmt.Printf("  worktree et branche %s supprimés.\n", branch)
	}
}
