package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/brief"
)

// workerSpec is one worker as it will be started: the global worker-kind
// and worker-model, unless the config overrides them for this worker.
type workerSpec struct {
	Kind  string `json:"kind"`
	Model string `json:"model"`
	// PromptPath is absolute: the worker runs from its own worktree, where
	// a path relative to the repo would point somewhere else.
	PromptPath string `json:"prompt_path,omitempty"`
	Prompt     string `json:"-"` // content, for the master's brief only
	Overridden bool   `json:"overridden,omitempty"`
	// Dir, when set, is where a worker outside the code starts: no
	// worktree, environment or branch (see validateAgentDir).
	Dir string `json:"dir,omitempty"`
	// Tasks are the kinds of task it takes (queue add --kind); Keep keeps
	// it open for the life of the swarm.
	Tasks []string `json:"tasks,omitempty"`
	Keep  bool     `json:"keep,omitempty"`
}

// takes says whether a worker of spec w may get t without being named by
// --worker: a task of a kind goes to the workers that list it, a task of
// no kind to the general-purpose ones.
func (w workerSpec) takes(t queuedTask) bool {
	if t.Kind != "" {
		return slices.Contains(w.Tasks, t.Kind)
	}
	return !w.Overridden
}

// resolveWorkers builds the spec of each of the opts.workers workers. An
// override for a worker that doesn't exist, or a prompt that can't be
// read, refuses to start: a worker meant to verify that silently becomes
// a generic one would skew every dispatch the master makes.
func resolveWorkers(opts *startOptions, repo string) ([]workerSpec, error) {
	workers := make([]workerSpec, opts.workers)
	for i := range workers {
		workers[i] = workerSpec{Kind: opts.workerKind, Model: opts.workerModel}
	}
	for key, o := range opts.overrides {
		i, err := strconv.Atoi(key)
		if err != nil || i < 1 || i > opts.workers {
			return nil, fmt.Errorf("worker-overrides: %q names no worker (1 to %d)", key, opts.workers)
		}
		w := &workers[i-1]
		w.Overridden = true
		if o.Kind != nil {
			w.Kind = *o.Kind
		}
		if o.Model != nil {
			w.Model = *o.Model
		}
		if o.Prompt != nil {
			path := *o.Prompt
			if !filepath.IsAbs(path) {
				path = filepath.Join(repo, path)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("worker-overrides %s: prompt unreadable: %w", key, err)
			}
			w.PromptPath, w.Prompt = path, string(content)
		}
		if o.Dir != nil {
			dir, err := validateAgentDir(repo, *o.Dir)
			if err != nil {
				return nil, fmt.Errorf("worker-overrides %s: dir: %w", key, err)
			}
			w.Dir = dir
		}
		for _, k := range o.Tasks {
			if k = strings.TrimSpace(k); k == "" || strings.HasPrefix(k, "-") || strings.ContainsAny(k, " \t") {
				return nil, fmt.Errorf("worker-overrides %s: tasks: %q is not a task kind (one word, like need-review)", key, k)
			}
			w.Tasks = append(w.Tasks, k)
		}
		w.Keep = o.Keep != nil && *o.Keep
	}
	return workers, nil
}

// distinctKinds lists each kind once, in worker order.
func distinctKinds(workers []workerSpec) []string {
	var kinds []string
	seen := map[string]bool{}
	for _, w := range workers {
		if !seen[w.Kind] {
			seen[w.Kind] = true
			kinds = append(kinds, w.Kind)
		}
	}
	return kinds
}

func briefWorkers(workers []workerSpec) []brief.Worker {
	out := make([]brief.Worker, len(workers))
	for i, w := range workers {
		out[i] = brief.Worker{Kind: w.Kind, Model: w.Model, Prompt: w.Prompt, Overridden: w.Overridden, Dir: w.Dir, Tasks: w.Tasks, Keep: w.Keep}
	}
	return out
}

// workerRole is what every claude worker knows of its place in the
// setup, in its system prompt: a master used to copy it into every brief,
// because the reset before each task wiped it, and the copies drifted.
func workerRole(label, statusFile string) string {
	return fmt.Sprintf(`You are %[1]s, a worker of an acw setup. A master session hands you your tasks and relays the user's decisions: you report to the master only, through your status file and the end of your turn, never by expecting the user to read your pane. A message that starts with "Message from the master" comes from it. Your context is reset before each task: this prompt survives the reset, the task's brief brings the rest.

Your status file is %[2]s, a JSON object. Write it after every significant step (taking the task, milestone reached, blocked on an arbitration, PR opened) with at least: tache, state, summary, base_branch, ports, branch, decision, pr_url, proof_path, blocked_on. Put the value itself in the field, and leave it empty as long as it does not exist: "approach chosen" without naming the choice, or "PR opened" without its link, is worse than empty. decision lists, before you write any code, the files you will create or modify. base_branch is the branch your PR is based on. blocked_on says what you wait for, while state says you are blocked. Whenever you hand a task over (proof ready, review asked, PR opened), everything is committed and git status is clean: once the master ends your task, acw puts you back on your waiting branch, and your branch goes to whoever takes the next task on it, a reviewer or another worker fixing what the review found. When you stop to have something approved by the master (a plan, a verdict, a review draft to read), state ends in _ready: plan_ready, verdict_ready, review_ready, and summary says what is to approve; the user's page lists these as waiting on them. A message from the master sets state back to working: if you still wait on an approval after it, write your _ready state again. Leave updated_at, last_turn_end and state_since out: acw writes them at the end of each turn. If you delegate to a subagent or touch a shared code area, say so there plainly. If you hand control back while a subagent you started still runs, set state to waiting_subagent and say in summary what you wait for.

Temporary files go in /tmp, outside the repo, and you never run rm -rf in your worktree: a recursive deletion triggers an approval prompt nobody may be there to answer.

A pull request stacked on another one stays based on that branch until it is merged: retargeted to the default branch earlier, its diff takes in every commit of the PR below.`, label, statusFile)
}

// systemPrompt is a claude worker's system prompt: its role, the repo's
// notes, then its own standing instructions.
func systemPrompt(role, notes, prompt string) string {
	parts := []string{role}
	if s := strings.TrimSpace(notes); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(prompt); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n")
}

// writeSystemPrompts gives each claude worker one system prompt file in
// statusDir and points its PromptPath at it. A system prompt survives
// /clear: its role and the repo rules no longer have to travel in every
// brief, where they drifted from one brief to the next. Other kinds have
// no system prompt acw can set and are left as configured.
func writeSystemPrompts(statusDir, notes string, workers []workerSpec) error {
	for i := range workers {
		w := &workers[i]
		if w.Kind != "claude" {
			continue
		}
		label := fmt.Sprintf("worker%d", i+1)
		content := systemPrompt(workerRole(label, filepath.Join(statusDir, label+".json")), notes, w.Prompt)
		path := filepath.Join(statusDir, label+".system.md")
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			return err
		}
		w.PromptPath = path
	}
	return nil
}

// describeWorkers is the launch line's view of the workers: one kind and
// model when they all share them, each worker otherwise.
func describeWorkers(workers []workerSpec) string {
	if len(workers) == 0 {
		return ""
	}
	same := true
	for _, w := range workers {
		if w.Kind != workers[0].Kind || w.Model != workers[0].Model {
			same = false
			break
		}
	}
	if same {
		return brief.DescribeAgent(workers[0].Kind, workers[0].Model)
	}
	parts := make([]string, len(workers))
	for i, w := range workers {
		parts[i] = fmt.Sprintf("worker%d %s", i+1, brief.DescribeAgent(w.Kind, w.Model))
	}
	return strings.Join(parts, ", ")
}

// provisionPlan is everything opening a worker needs, kept in pool.json
// for the life of the swarm.
type provisionPlan struct {
	Repo       string `json:"repo"`
	MasterPane string `json:"master_pane"`
	Stamp      string `json:"stamp"`
	// Stacks is set when a worker in the code gets a wtm stack: wtm is
	// there and knows the repo. MaxStacks then caps how many are open.
	Stacks    bool   `json:"stacks,omitempty"`
	MaxStacks int    `json:"max_stacks"`
	Profile   string `json:"profile"`
	// Workers is every worker that may be opened, worker1 first: its
	// length is the configured count.
	Workers []workerSpec `json:"workers"`
	// Inbox is where pings go for a master that watches one, "" when they
	// are typed into it (see inboxWatchCommand).
	Inbox string `json:"inbox,omitempty"`
	// SwitchAllowed is set when wtm has switch: the workers with a stack
	// may run it without a prompt.
	SwitchAllowed bool `json:"switch_allowed,omitempty"`
}
