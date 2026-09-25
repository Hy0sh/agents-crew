package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/Hy0sh/agents-crew/internal/brief"
	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/layout"
	"github.com/Hy0sh/agents-crew/internal/names"
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
func provisionWorkers(plan provisionPlan) {
	repo, stamp, n := plan.Repo, plan.Stamp, len(plan.Workers)
	slug := names.Slug(repo)
	masterName := names.Master(slug)

	if err := gitutil.Fetch(repo); err != nil {
		fmt.Fprintln(os.Stderr, "git fetch:", err)
	}
	baseRef := gitutil.DefaultBaseRef(repo)

	splits := layout.WorkerSplits(n)
	currentPane := plan.MasterPane

	var adopting sync.WaitGroup
	stacked := stackedWorkers(plan.Workers, plan.MaxStacks)

	// The hook calls this same binary back; without its path the workers
	// still ping, they only lose the status normalization.
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "chemin d'acw introuvable, statuts non normalisés:", err)
	}
	statusDir := names.StatusDir(repo)

	for i := 1; i <= n; i++ {
		label := fmt.Sprintf("worker%d", i) // cosmetic pane label, kept short
		name := names.Worker(slug, i)       // actual herdr agent name, unique per repo
		w := plan.Workers[i-1]

		// A worker outside the code starts in its own dir: no worktree.
		wt := w.Dir
		if wt == "" {
			wt = names.WorkerWorktree(repo, i, stamp)
			if err := gitutil.WorktreeAdd(repo, wt, names.WorkerBranch(i, stamp), baseRef); err != nil {
				fmt.Fprintf(os.Stderr, "%s: git worktree add: %v\n", name, err)
				continue
			}
		}

		split := splits[i-1]
		newPane, err := herdr.PaneSplit(currentPane, split.Direction, split.Ratio, wt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr pane split: %v\n", name, err)
			continue
		}
		currentPane = newPane

		if err := herdr.PaneRename(newPane, label); err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr pane rename: %v\n", name, err)
		}
		hook := pingCommand(plan.Inbox, masterName, label)
		if self != "" {
			hook = stopCommand(self, statusDir, label, hook)
		}
		if err := herdr.AgentStart(name, w.Kind, newPane, workerArgs(w, label, hook)...); err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr agent start: %v\n", name, explainStart(err, wt))
			continue
		}

		if stacked[i-1] && wtm.Available() {
			adopting.Add(1)
			go func(name, wt string) {
				defer adopting.Done()
				if err := wtm.Adopt(wt, plan.Profile); err != nil {
					fmt.Fprintf(os.Stderr, "%s: wtm adopt a échoué — il continue sans environnement dédié: %v\n", name, err)
				}
			}(name, wt)
		}
	}

	adopting.Wait()

	ready := brief.WorkersReadyMessage(slug, n)
	if plan.Inbox != "" {
		if err := appendLine(plan.Inbox, ready); err != nil {
			fmt.Fprintln(os.Stderr, "inbox du master (workers ready):", err)
		}
		return
	}
	if err := herdr.AgentPrompt(masterName, ready); err != nil {
		fmt.Fprintln(os.Stderr, "herdr agent prompt master (workers ready):", err)
	}
}

// workerArgs is what gets forwarded to a worker's own CLI: its model,
// plus — for a Claude Code worker — a Stop hook that pings the master
// every time the worker hands control back.
//
// That ping is the one thing Herdr cannot provide: it has no push
// notification for agent state, so a master only learns a worker moved by
// going to look. Every substitute tried in practice was a discipline the
// master had to keep up (re-arming `agent wait` after each wake-up and
// each dispatch, telling each worker to report in) and disciplines get
// dropped — a finished PR went unnoticed for an afternoon that way. The
// hook also normalizes the worker's status file first (see stopCommand). A
// hook is not a discipline: it fires whatever the worker or the master
// remembered to do, and survives the `/clear` between two tasks that
// wipes everything the worker was told.
//
// Only for `claude` workers: no other agent kind exposes hooks, and they
// simply keep the pre-existing behaviour (the master polls). Passed as
// inline JSON rather than written into the worktree, so nothing lands in
// a file a worker could commit by accident.
//
// Its standing instructions, when it has some, go in as a system prompt
// for the same reason: that also survives `/clear`. Claude only as well;
// another kind gets them from the master, copied into each of its briefs.
func workerArgs(w workerSpec, label, hook string) []string {
	args := modelArgs(w.Model)
	if w.Kind != "claude" {
		return args
	}
	if w.PromptPath != "" {
		args = append(args, "--append-system-prompt-file", w.PromptPath)
	}
	hooks, err := stopHookSettings(hook)
	if err != nil {
		// Only json.Marshal of a literal struct can fail here, which it
		// cannot; the worker still starts, just without its ping.
		fmt.Fprintf(os.Stderr, "%s: hook Stop non installé: %v\n", label, err)
		return args
	}
	return append(args, "--settings", hooks)
}

// pingCommand is the shell command a worker's Stop hook runs: one line
// appended to the master's inbox, or, when there is none (see
// inboxWatchCommand), the same text typed into the master's input.
func pingCommand(inbox, masterName, label string) string {
	msg := shellWord(pingMessage(label))
	if inbox != "" {
		return "printf '%s\\n' " + msg + " >> " + shellWord(inbox)
	}
	return "herdr agent prompt " + shellWord(masterName) + " " + msg
}

// pingMessage tells the master a worker handed control back, from its
// Stop hook or, for a worker without one, from acw's watcher. Deliberately
// says nothing about WHAT changed: neither can know, and a ping that
// guesses would be worse than one that points at the status file.
func pingMessage(label string) string {
	return fmt.Sprintf("%s a rendu la main. Lis son fichier de statut (champs state, decision, pr_url, proof_path) avant toute réaction. "+
		"Si rien n'a changé depuis ton dernier point, ne fais rien et ne lui écris pas.", label)
}

// stopCommand is the full command of a claude worker's Stop hook: the
// status normalization first, so the master reads a fixed file when the
// ping wakes it, then the ping. Joined with ; and not &&: a failed
// normalization must not cost the ping.
func stopCommand(exe, statusDir, label, ping string) string {
	return shellWord(exe) + " " + turnEndUse + " " + shellWord(statusDir) + " " + shellWord(label) + "; " + ping
}

func stopHookSettings(ping string) (string, error) {
	type command struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	settings := struct {
		Hooks map[string][]struct {
			Hooks []command `json:"hooks"`
		} `json:"hooks"`
	}{
		Hooks: map[string][]struct {
			Hooks []command `json:"hooks"`
		}{
			"Stop": {{Hooks: []command{{Type: "command", Command: ping}}}},
		},
	}
	out, err := json.Marshal(settings)
	return string(out), err
}
