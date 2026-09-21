package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"agents-crew/internal/brief"
	"agents-crew/internal/gitutil"
	"agents-crew/internal/herdr"
	"agents-crew/internal/layout"
	"agents-crew/internal/wtm"
)

// provisionWorkers runs as a detached background process (see
// launchBackgroundProvisioning): it creates each worker's worktree and
// environment, lays out and starts the worker panes, then tells the
// master they're ready. Errors are logged and provisioning continues for
// the remaining workers where it safely can — a partial swarm beats none.
func provisionWorkers(args []string) {
	if len(args) != 5 {
		fmt.Fprintf(os.Stderr, "provisionWorkers: expected 5 args, got %d\n", len(args))
		os.Exit(1)
	}
	repo, masterPane, stamp := args[0], args[1], args[2]
	n, err := strconv.Atoi(args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	maxStacks, err := strconv.Atoi(args[4])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := gitutil.Fetch(repo); err != nil {
		fmt.Fprintln(os.Stderr, "git fetch:", err)
	}
	baseRef := gitutil.DefaultBaseRef(repo)

	splits := layout.WorkerSplits(n)
	panes := make([]string, n+1) // panes[0] unused, panes[i] = worker i's pane
	currentPane := masterPane

	for i := 1; i <= n; i++ {
		wt := filepath.Join(repo, ".claude", "worktrees", fmt.Sprintf("worker%d-%s", i, stamp))
		branch := fmt.Sprintf("agents/worker%d-%s", i, stamp)
		if err := gitutil.WorktreeAdd(repo, wt, branch, baseRef); err != nil {
			fmt.Fprintf(os.Stderr, "worker%d: git worktree add: %v\n", i, err)
			continue
		}

		if i <= maxStacks && wtm.Available() {
			if err := wtm.Adopt(wt); err != nil {
				fmt.Fprintf(os.Stderr, "worker%d: wtm adopt a échoué — il démarre sans environnement dédié: %v\n", i, err)
			}
		}

		split := splits[i-1]
		newPane, err := herdr.PaneSplit(currentPane, split.Direction, split.Ratio, wt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "worker%d: herdr pane split: %v\n", i, err)
			continue
		}
		panes[i] = newPane
		currentPane = newPane
	}

	// Rename first, once the whole layout is final — then start agents, so
	// no split ever resizes a pane that already hosts a running agent.
	for i := 1; i <= n; i++ {
		if panes[i] == "" {
			continue
		}
		name := fmt.Sprintf("worker%d", i)
		if err := herdr.PaneRename(panes[i], name); err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr pane rename: %v\n", name, err)
		}
	}
	for i := 1; i <= n; i++ {
		if panes[i] == "" {
			continue
		}
		name := fmt.Sprintf("worker%d", i)
		if err := herdr.AgentStart(name, panes[i], "--model", "sonnet"); err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr agent start: %v\n", name, err)
		}
	}

	if err := herdr.AgentPrompt("master", brief.WorkersReadyMessage(n)); err != nil {
		fmt.Fprintln(os.Stderr, "herdr agent prompt master (workers ready):", err)
	}
}
