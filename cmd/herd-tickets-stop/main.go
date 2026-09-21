// herd-tickets-stop tears down a swarm started by herd-tickets: each
// worker's environment (via wtm), the shared status directory, and the
// Herdr workspace itself. Closing the terminal alone does nothing — Herdr
// is a persistent server that outlives it, and so do the environments.
package main

import (
	"fmt"
	"os"
	"strings"

	"herd-tickets/internal/herdr"
	"herd-tickets/internal/wtm"
)

func main() {
	if err := stop(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func stop() error {
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
		fmt.Println("Aucun master herd-tickets en cours.")
		return nil
	}

	if wtm.Available() {
		for _, a := range agents {
			if a.WorkspaceID != workspaceID || !strings.HasPrefix(a.Name, "worker") || a.Cwd == "" {
				continue
			}
			if err := wtm.Stop(a.Cwd); err != nil {
				fmt.Fprintf(os.Stderr, "%s: wtm stop (%s): %v\n", a.Name, a.Cwd, err)
				continue
			}
			if err := wtm.Remove(a.Cwd); err != nil {
				fmt.Fprintf(os.Stderr, "%s: wtm remove (%s): %v\n", a.Name, a.Cwd, err)
			}
		}
	}

	if err := herdr.WorkspaceClose(workspaceID, true); err != nil {
		return fmt.Errorf("herdr workspace close: %w", err)
	}

	if repo != "" {
		statusDir := repo + "/.claude/worktrees/.herd-status"
		if err := os.RemoveAll(statusDir); err != nil {
			fmt.Fprintf(os.Stderr, "suppression de %s: %v\n", statusDir, err)
		}
	}

	fmt.Printf("Workspace %s fermé, environnements nettoyés, fichiers de statut supprimés.\n", workspaceID)
	return nil
}
