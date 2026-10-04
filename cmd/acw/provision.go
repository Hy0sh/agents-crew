package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/teardown"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// openWorker brings up worker index, already in the pool as opening (see
// applyActions): its worktree, its pane next to the others, its agent,
// then its stack, the slow part. Run in its own goroutine by the watcher,
// so blocks and silences are still reported while a stack comes up. Once
// done the worker is free and the next poll gives it a task; on a
// failure, what was made is undone and the master is told.
func openWorker(repo string, index int) {
	p, _, err := readPool(repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opening a worker:", err)
		return
	}
	plan := p.Plan
	pw := p.worker(index)
	if pw == nil {
		return
	}
	slug := names.Slug(repo)
	masterName := names.Master(slug)
	label := pw.label()               // cosmetic pane label, kept short
	name := names.Worker(slug, index) // actual herdr agent name, unique per repo
	w := plan.Workers[index-1]
	fail := func(step string, err error, pane string) {
		fmt.Fprintf(os.Stderr, "%s: %s: %v\n", name, step, err)
		if pane != "" {
			_ = herdr.PaneClose(pane)
		}
		if pw.Worktree != "" {
			teardown.Worktree(repo, pw.Worktree)
		}
		_ = withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
			p.remove(index)
			return true, nil
		})
		tell(plan, fmt.Sprintf("%s could not be opened (%s: %v). The tasks waiting for it stay queued.", label, step, err))
	}

	// A worker outside the code starts in its own dir: no worktree.
	cwd := w.Dir
	if pw.Worktree != "" {
		cwd = pw.Worktree
	}
	pane, step, err := placeWorker(repo, plan.MasterPane, *pw, cwd)
	if err != nil {
		if step == "git worktree add" {
			pw.Worktree = "" // not made: nothing to tear down
		}
		fail(step, err, pane)
		return
	}
	if err := herdr.PaneRename(pane, label); err != nil {
		fmt.Fprintf(os.Stderr, "%s: herdr pane rename: %v\n", name, err)
	}

	// The hook calls this same binary back; without its path the worker
	// still pings, it only loses the status normalization.
	delta, statusLine := "", ""
	if self, err := os.Executable(); err == nil {
		statusDir := names.StatusDir(repo)
		delta = turnEndCommand(self, statusDir, label)
		statusLine = statusLineCommand(self, statusDir, label)
	} else {
		fmt.Fprintln(os.Stderr, "acw's path not found, statuses won't be normalized:", err)
	}
	hook := pingCommand(plan.Inbox, masterName, label, delta)
	if err := herdr.AgentStart(name, w.Kind, pane, workerArgs(w, label, hook, statusLine, plan.SwitchAllowed && pw.Stacked)...); err != nil {
		fail("herdr agent start", explainStart(err, cwd), pane)
		return
	}

	opened := label + " opened"
	stacked := pw.Stacked
	if stacked {
		if err := adoptAlone(pw.Worktree, plan.Profile); err != nil {
			fmt.Fprintf(os.Stderr, "%s: wtm adopt failed, it goes on without a dedicated environment: %v\n", name, err)
			// wtm records the index and the path before it starts the
			// stack: an unknown profile, a port clash or a missing dump
			// left them behind, with volumes and sometimes containers.
			if branch, err := gitutil.CurrentBranch(pw.Worktree); err == nil {
				if err := wtm.Remove(pw.Worktree, branch); err != nil {
					fmt.Fprintf(os.Stderr, "%s: undoing the failed adopt: %v\n", name, err)
				}
			}
			stacked = false
			opened += ", WITHOUT its environment (wtm adopt failed): give it no task that needs one, " +
				"and tell it never to run wtm switch, which it was allowed before the adopt failed"
		} else {
			opened += " with its environment"
			if err := teardown.MarkStacked(pw.Worktree); err != nil {
				fmt.Fprintf(os.Stderr, "%s: recording its stack: %v\n", name, err)
			}
			// wtm skips clashing ports when it allocates, but not against
			// worktrees recorded before it learnt to, nor other projects:
			// a stack then failed to start and doctor only told afterwards.
			report, err := wtm.Doctor(repo)
			if err != nil {
				fmt.Fprintln(os.Stderr, "wtm doctor:", err)
			}
			branch, _ := gitutil.CurrentBranch(pw.Worktree)
			if clashes := portClashes(report, branch); clashes != "" {
				opened += "\nwtm doctor reports port clashes, its stack may be down:\n" + clashes
			}
		}
	}
	_ = withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		if w := p.worker(index); w != nil {
			w.State, w.Since, w.Stacked = workerFree, time.Now(), stacked
		}
		return true, nil
	})
	tell(plan, opened+": acw gives it the next task it may take.")
}

// placing serializes the fast half of every opening, from git worktree add
// to the pane's split: workers opened in the same poll took the same
// .git/config lock (one add failed and left its branch behind) and all
// split off the master, none seeing the others' panes yet.
var placing sync.Mutex

// adopting runs one wtm adopt at a time. wtm checks a new index's ports
// against the other worktrees' from a registry read before it locks it:
// two adopts at once each missed the other, took neighbouring indices,
// and with a stride of 1 two of their services got the same host port,
// one stack failing to start. Slower when several open at once, but each
// pane and agent is already up while its stack waits.
var adopting sync.Mutex

func adoptAlone(dir, profile string) error {
	adopting.Lock()
	defer adopting.Unlock()
	return wtm.Adopt(dir, profile)
}

// placeWorker makes w's worktree, when it has one, and its pane, recorded
// in the pool before the next opening looks for where to split. step
// names what failed; pane is the one made, if any, for the caller to close.
func placeWorker(repo, masterPane string, w poolWorker, cwd string) (pane, step string, err error) {
	placing.Lock()
	defer placing.Unlock()
	if w.Worktree != "" {
		if err := gitutil.Fetch(repo); err != nil {
			fmt.Fprintln(os.Stderr, "git fetch:", err)
		}
		branch := names.WorkerBranch(w.Index, strings.TrimPrefix(filepath.Base(w.Worktree), w.label()+"-"))
		if err := gitutil.WorktreeAdd(repo, w.Worktree, branch, gitutil.DefaultBaseRef(repo)); err != nil {
			// Best effort: git may have made the branch before failing.
			_ = gitutil.DeleteBranch(repo, branch)
			return "", "git worktree add", err
		}
	}
	p, _, err := readPool(repo)
	if err != nil {
		return "", "reading the pool", err
	}
	var others []string
	for _, o := range p.Workers {
		if o.Index != w.Index && o.Pane != "" {
			others = append(others, o.Pane)
		}
	}
	var heights map[string]int
	if len(others) > 0 {
		if heights, err = herdr.PaneHeights(masterPane); err != nil {
			fmt.Fprintln(os.Stderr, "herdr pane layout:", err)
		}
	}
	anchor, direction, ratio := splitFrom(masterPane, others, heights)
	if pane, err = herdr.PaneSplit(anchor, direction, ratio, cwd); err != nil {
		return "", "herdr pane split", err
	}
	err = withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		if pw := p.worker(w.Index); pw != nil {
			pw.Pane = pane
		}
		return true, nil
	})
	if err != nil {
		return pane, "recording its pane", err
	}
	return pane, "", nil
}

// splitFrom is where a new worker's pane goes: the master keeps the left
// 60 %, the workers share one column on the right. The first one splits
// off the master; each next one halves the tallest worker pane, which
// keeps the column even as workers open and close. Without heights, the
// last worker pane.
func splitFrom(masterPane string, workerPanes []string, heights map[string]int) (pane, direction string, ratio float64) {
	if len(workerPanes) == 0 {
		return masterPane, "right", 0.6
	}
	tallest := workerPanes[len(workerPanes)-1]
	for _, p := range workerPanes {
		if heights[p] > heights[tallest] {
			tallest = p
		}
	}
	return tallest, "down", 0.5
}

// closeWorker tears down a worker the pool no longer needs: its pane and
// agent, its worktree and environment (its task branch stays, see
// teardown.Worktree), its files in the status dir but its system prompt,
// which a reopening needs. A worktree whose stack wtm no longer finds is
// kept, and the master told how to get the stack back.
func closeWorker(repo string, w poolWorker, why string) {
	plan, _, err := readPool(repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "closing a worker:", err)
		return
	}
	if w.Pane != "" {
		if err := herdr.PaneClose(w.Pane); err != nil {
			fmt.Fprintf(os.Stderr, "%s: herdr pane close: %v\n", w.label(), err)
		}
	}
	if w.Worktree != "" {
		if kept := teardown.Worktree(repo, w.Worktree); kept != "" {
			why += fmt.Sprintf(". Its worktree %s is KEPT: %s. Tell me", w.Worktree, kept)
		}
	}
	files, _ := filepath.Glob(filepath.Join(names.StatusDir(repo), w.label()+".*"))
	for _, f := range files {
		if !strings.HasSuffix(f, ".system.md") {
			_ = os.Remove(f)
		}
	}
	_ = withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		p.remove(w.Index)
		return true, nil
	})
	tell(plan.Plan, w.label()+" closed: "+why+".")
}

// tell sends the master a line about the pool.
func tell(plan provisionPlan, msg string) {
	if err := deliver(plan.Inbox, names.Master(names.Slug(plan.Repo)), msg); err != nil {
		fmt.Fprintln(os.Stderr, "message to the master:", err)
	}
}

// portClashes keeps, from a `wtm doctor` report, the port clashes branch
// takes part in, "" when it takes part in none: the report covers the
// whole machine, and the worker just opened only cares about its own. A
// section is its heading line and what follows it up to a blank line; a
// clash line names each side as "<branch> <service>", or
// "<project>/<branch> <service>" between projects. The section's other
// lines, its hints, go along with its clashes.
func portClashes(report, branch string) string {
	var kept, heading, clashes, hints []string
	flush := func() {
		if len(clashes) > 0 {
			kept = append(kept, slices.Concat(heading, clashes, hints)...)
		}
		heading, clashes, hints = nil, nil, nil
	}
	in := false
	for _, line := range strings.Split(report, "\n") {
		switch {
		case strings.HasPrefix(line, "port clashes"):
			flush()
			in, heading = true, []string{line}
		case strings.TrimSpace(line) == "":
			flush()
			in = false
		case !in:
		case !strings.Contains(line, " is claimed by "):
			hints = append(hints, line)
		case claims(line, branch):
			clashes = append(clashes, line)
		}
	}
	flush()
	return strings.Join(kept, "\n")
}

// claims reports whether a clash line names branch as one of its sides.
func claims(line, branch string) bool {
	for _, word := range strings.Fields(line) {
		if word == branch || strings.HasSuffix(word, "/"+branch) {
			return true
		}
	}
	return false
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
// recordUsage), in the same --settings. allowSwitch lets a worker with a
// stack run wtm switch without a prompt.
func workerArgs(w workerSpec, label, hook, statusLine string, allowSwitch bool) []string {
	args := modelArgs(w.Model)
	if w.Kind != "claude" {
		return args
	}
	if w.PromptPath != "" {
		args = append(args, "--append-system-prompt-file", w.PromptPath)
	}
	if allowSwitch {
		// The brief tells a worker with a stack to create its branch
		// with wtm switch; a prompt there would freeze it until someone
		// comes by. It only acts on the worktree it runs in.
		args = append(args, "--allowedTools", "Bash(wtm switch:*)")
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
