// Package teardown implements the agents-crew-stop logic: releasing
// every worker's environment, the shared status directory, and the Herdr
// workspace itself. Shared between the `agents-crew stop` subcommand and
// the standalone `agents-crew-stop` binary.
package teardown

import (
	"fmt"
	"os"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// Run tears down the running swarm, if any, printing progress to stdout
// and non-fatal errors to stderr. It returns nil even when there was
// nothing to tear down.
func Run() error {
	agents, err := herdr.AgentList()
	if err != nil {
		return fmt.Errorf("herdr agent list: %w", err)
	}

	var workspaceID, repo string
	for _, a := range agents {
		if a.Name == "master" {
			workspaceID = a.WorkspaceID
			repo = a.Cwd
			break
		}
	}
	if workspaceID == "" {
		fmt.Println("Aucun master agents-crew en cours.")
		return nil
	}

	if wtm.Available() {
		for _, a := range agents {
			if a.WorkspaceID != workspaceID || !strings.HasPrefix(a.Name, "worker") || a.Cwd == "" {
				continue
			}
			branch, err := gitutil.CurrentBranch(a.Cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: resolving branch (%s): %v\n", a.Name, a.Cwd, err)
				continue
			}
			if err := wtm.Stop(a.Cwd, branch); err != nil {
				fmt.Fprintf(os.Stderr, "%s: wtm stop (%s): %v\n", a.Name, a.Cwd, err)
				continue
			}
			if err := wtm.Remove(a.Cwd, branch); err != nil {
				fmt.Fprintf(os.Stderr, "%s: wtm remove (%s): %v\n", a.Name, a.Cwd, err)
			}
		}
	}

	if err := herdr.WorkspaceClose(workspaceID, true); err != nil {
		return fmt.Errorf("herdr workspace close: %w", err)
	}

	if repo != "" {
		statusDir := repo + "/.claude/worktrees/.agents-crew-status"
		if err := os.RemoveAll(statusDir); err != nil {
			fmt.Fprintf(os.Stderr, "suppression de %s: %v\n", statusDir, err)
		}
	}

	fmt.Printf("Workspace %s fermé, environnements nettoyés, fichiers de statut supprimés.\n", workspaceID)
	return nil
}
