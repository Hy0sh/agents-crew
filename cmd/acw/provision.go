package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
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
		fmt.Fprintln(os.Stderr, "acw's path not found, statuses won't be normalized:", err)
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
		delta, statusLine := "", ""
		if self != "" {
			delta = turnEndCommand(self, statusDir, label)
			statusLine = statusLineCommand(self, statusDir, label)
		}
		hook := pingCommand(plan.Inbox, masterName, label, delta)
		if err := herdr.AgentStart(name, w.Kind, newPane, workerArgs(w, label, hook, statusLine)...); err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr agent start: %v\n", name, explainStart(err, wt))
			continue
		}

		if stacked[i-1] && wtm.Available() {
			adopting.Add(1)
			go func(name, wt string) {
				defer adopting.Done()
				if err := wtm.Adopt(wt, plan.Profile); err != nil {
					fmt.Fprintf(os.Stderr, "%s: wtm adopt failed, it goes on without a dedicated environment: %v\n", name, err)
				}
			}(name, wt)
		}
	}

	adopting.Wait()

	ready := brief.WorkersReadyMessage(slug, n)
	// wtm skips clashing ports when it allocates, but not against
	// worktrees recorded before it learnt to, nor other projects: a
	// worker's stack then failed to start and doctor only told afterwards.
	if slices.Contains(stacked, true) && wtm.Available() {
		report, err := wtm.Doctor(repo)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wtm doctor:", err)
		}
		if clashes := portClashes(report); clashes != "" {
			ready += "\n\nwtm doctor reports port clashes: an affected worker may have no stack. Tell me before dispatching it a task that needs one.\n" + clashes
		}
	}
	if plan.Inbox != "" {
		if err := appendLine(plan.Inbox, ready); err != nil {
			fmt.Fprintln(os.Stderr, "master's inbox (workers ready):", err)
		}
		return
	}
	if err := herdr.AgentPrompt(masterName, ready); err != nil {
		fmt.Fprintln(os.Stderr, "herdr agent prompt master (workers ready):", err)
	}
}

// portClashes keeps the port clash sections of a `wtm doctor` report, ""
// when it has none. A section is its heading line and what follows it up
// to a blank line.
func portClashes(report string) string {
	var kept []string
	in := false
	for _, line := range strings.Split(report, "\n") {
		switch {
		case strings.HasPrefix(line, "port clashes"):
			in = true
		case strings.TrimSpace(line) == "":
			in = false
		}
		if in {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
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
// hook also normalizes the worker's status file first (see turnEndCommand). A
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
//
// statusLine, when not empty, is acw's status line for it (see
// recordUsage), in the same --settings.
func workerArgs(w workerSpec, label, hook, statusLine string) []string {
	args := modelArgs(w.Model)
	if w.Kind != "claude" {
		return args
	}
	if w.PromptPath != "" {
		args = append(args, "--append-system-prompt-file", w.PromptPath)
	}
	hooks, err := workerSettings(hook, statusLine)
	if err != nil {
		// Only json.Marshal of a literal struct can fail here, which it
		// cannot; the worker still starts, just without its ping.
		fmt.Fprintf(os.Stderr, "%s: Stop hook not installed: %v\n", label, err)
		return args
	}
	return append(args, "--settings", hooks)
}

// pingCommand is the shell command a worker's Stop hook runs: one line
// appended to the master's inbox, or, when there is none (see
// inboxWatchCommand), the same text typed into the master's input. delta
// is a shell word whose value goes after "handed control back" (see
// turnEndCommand), "" for none.
func pingCommand(inbox, masterName, label, delta string) string {
	if delta == "" {
		delta = "''"
	}
	parts := shellWord(label+pingHead) + " " + delta + " " + shellWord(pingTail)
	if inbox != "" {
		return "printf '%s%s%s\\n' " + parts + " >> " + shellWord(inbox)
	}
	return "herdr agent prompt " + shellWord(masterName) + ` "$(printf '%s%s%s' ` + parts + `)"`
}

const (
	pingHead = " handed control back"
	pingTail = ". Read its status file (fields state, decision, pr_url, proof_path) before reacting, unless this message says it is unchanged. " +
		"If nothing changed since your last status point, do nothing and don't write to it."
)

// pingMessage tells the master a worker handed control back, from acw's
// watcher for a worker without a Stop hook: it says nothing about what
// changed, which only the hook's turn end can tell (see statusDelta).
func pingMessage(label string) string {
	return label + pingHead + pingTail
}

// turnEndCommand is the delta word of a claude worker's Stop hook: it
// normalizes the status file first, so the master reads a fixed file when
// the ping wakes it, and prints what moved in it. A substitution and not
// a command before the ping: a failed normalization prints nothing, and
// the ping still goes out.
func turnEndCommand(exe, statusDir, label string) string {
	return `"$(` + shellWord(exe) + " " + turnEndUse + " " + shellWord(statusDir) + " " + shellWord(label) + `)"`
}

// workerSettings is the --settings JSON of a claude worker: its Stop hook
// and, when statusLine is not empty, acw's status line (see
// recordUsage).
func workerSettings(stop, statusLine string) (string, error) {
	type command struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	settings := struct {
		Hooks map[string][]struct {
			Hooks []command `json:"hooks"`
		} `json:"hooks"`
		StatusLine *command `json:"statusLine,omitempty"`
	}{
		Hooks: map[string][]struct {
			Hooks []command `json:"hooks"`
		}{
			"Stop": {{Hooks: []command{{Type: "command", Command: stop}}}},
		},
	}
	if statusLine != "" {
		settings.StatusLine = &command{Type: "command", Command: statusLine}
	}
	out, err := json.Marshal(settings)
	return string(out), err
}
