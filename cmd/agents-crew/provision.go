package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/Hy0sh/agents-crew/internal/brief"
	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/layout"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// provisionWorkers runs as a detached background process (see
// launchBackgroundProvisioning). For each worker, in order: `git worktree
// add` (seconds), split its pane off the shared layout, rename it, start
// its agent — that whole chain is fast, so each pane and agent appears
// within seconds of launch, one after another, instead of waiting for
// every worker at once. `wtm adopt` (the slow part, real services coming
// up) runs in its own goroutine per worker once the pane is already live,
// so N workers provision their environments concurrently instead of
// serially. Errors are logged and provisioning continues for the
// remaining workers where it safely can — a partial swarm beats none.
func provisionWorkers(args []string) {
	if len(args) != 6 {
		fmt.Fprintf(os.Stderr, "provisionWorkers: expected 6 args, got %d\n", len(args))
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
	workerModel := args[5]

	if err := gitutil.Fetch(repo); err != nil {
		fmt.Fprintln(os.Stderr, "git fetch:", err)
	}
	baseRef := gitutil.DefaultBaseRef(repo)

	splits := layout.WorkerSplits(n)
	currentPane := masterPane

	var adopting sync.WaitGroup

	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("worker%d", i)
		wt := filepath.Join(repo, ".claude", "worktrees", fmt.Sprintf("worker%d-%s", i, stamp))
		branch := fmt.Sprintf("agents/worker%d-%s", i, stamp)

		if err := gitutil.WorktreeAdd(repo, wt, branch, baseRef); err != nil {
			fmt.Fprintf(os.Stderr, "%s: git worktree add: %v\n", name, err)
			continue
		}

		split := splits[i-1]
		newPane, err := herdr.PaneSplit(currentPane, split.Direction, split.Ratio, wt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr pane split: %v\n", name, err)
			continue
		}
		currentPane = newPane

		if err := herdr.PaneRename(newPane, name); err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr pane rename: %v\n", name, err)
		}
		if err := herdr.AgentStart(name, newPane, "--model", workerModel); err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr agent start: %v\n", name, err)
			continue
		}

		if i <= maxStacks && wtm.Available() {
			adopting.Add(1)
			go func(name, wt string) {
				defer adopting.Done()
				if err := wtm.Adopt(wt); err != nil {
					fmt.Fprintf(os.Stderr, "%s: wtm adopt a échoué — il continue sans environnement dédié: %v\n", name, err)
				}
			}(name, wt)
		}
	}

	adopting.Wait()

	if err := herdr.AgentPrompt("master", brief.WorkersReadyMessage(n)); err != nil {
		fmt.Fprintln(os.Stderr, "herdr agent prompt master (workers ready):", err)
	}
}
