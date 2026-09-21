// Package teardown implements agents-crew's `stop`: releasing every
// worker's environment, the worktrees themselves, the shared status
// directory, and the Herdr workspace.
package teardown

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

var workerDirName = regexp.MustCompile(`^worker\d+-.+$`)

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
		if a.Cwd == cwd && strings.HasPrefix(a.Name, "master-") {
			workspaceID = a.WorkspaceID
			repo = a.Cwd
			break
		}
	}
	if workspaceID == "" {
		fmt.Println("Aucun master agents-crew en cours pour ce répertoire.")
		return nil
	}

	// Discovered by scanning .claude/worktrees/ for the workerN-* naming
	// convention, not by asking Herdr which agents are named "workerN":
	// a stack can be up (wtm adopt already ran) before the pane for it
	// even exists, let alone before `herdr agent start` names it — a stop
	// run during that window found nothing to clean up otherwise, leaving
	// real Docker stacks orphaned despite reporting success.
	cleanupWorkerWorktrees(repo)

	if err := herdr.WorkspaceClose(workspaceID, true); err != nil {
		return fmt.Errorf("herdr workspace close: %w", err)
	}

	statusDir := filepath.Join(repo, ".claude", "worktrees", ".agents-crew-status")
	if err := os.RemoveAll(statusDir); err != nil {
		fmt.Fprintf(os.Stderr, "suppression de %s: %v\n", statusDir, err)
	}

	fmt.Printf("Workspace %s fermé, environnements et worktrees nettoyés.\n", workspaceID)
	return nil
}

func cleanupWorkerWorktrees(repo string) {
	matches, err := filepath.Glob(filepath.Join(repo, ".claude", "worktrees", "worker*"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "recherche des worktrees workers: %v\n", err)
		return
	}

	for _, dir := range matches {
		name := filepath.Base(dir)
		if !workerDirName.MatchString(name) {
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

		if wtm.Available() {
			// Best-effort: a worktree whose environment was never adopted
			// (provisioning failed, or MAX_STACKS left it without one)
			// makes these fail harmlessly, which is fine — the worktree
			// removal below still runs.
			if err := wtm.Stop(dir, branch); err != nil {
				fmt.Fprintf(os.Stderr, "%s: wtm stop: %v\n", name, err)
			} else if err := wtm.Remove(dir, branch); err != nil {
				fmt.Fprintf(os.Stderr, "%s: wtm remove: %v\n", name, err)
			}
		}

		if err := gitutil.WorktreeRemove(repo, dir); err != nil {
			fmt.Fprintf(os.Stderr, "%s: git worktree remove: %v\n", name, err)
			continue
		}
		if err := gitutil.DeleteBranch(repo, branch); err != nil {
			fmt.Fprintf(os.Stderr, "%s: git branch -D %s: %v\n", name, branch, err)
		}
	}
}
