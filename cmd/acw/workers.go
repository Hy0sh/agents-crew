package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/brief"
	"github.com/Hy0sh/agents-crew/internal/config"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// workerSpec is one slot of the swarm: a worker as it will be started,
// of its role (see buildSlots). Its name is its role and its rank in it.
type workerSpec struct {
	Kind  string `json:"kind"`
	Model string `json:"model"`
	// Role and Rank name the worker (reviewer2); Min is its role's min,
	// how many of the role are kept open. Group, when set, is the key its
	// floor is counted by instead of Role (a legacy kept worker).
	Role  string `json:"role,omitempty"`
	Rank  int    `json:"rank,omitempty"`
	Min   int    `json:"min,omitempty"`
	Group string `json:"group,omitempty"`
	// PromptPath is absolute: the worker runs from its own worktree, where
	// a path relative to the repo would point somewhere else.
	PromptPath string `json:"prompt_path,omitempty"`
	Prompt     string `json:"-"` // content, for the master's brief only
	// Overridden is set for a role with tasks: it takes only those kinds.
	Overridden bool `json:"overridden,omitempty"`
	// Dir, when set, is where a worker outside the code starts: no
	// worktree, environment or branch (see validateAgentDir).
	Dir string `json:"dir,omitempty"`
	// Tasks are the kinds of task it takes (queue add --kind). Keep is only
	// read from a pool written before roles (see normalizeLegacy).
	Tasks []string `json:"tasks,omitempty"`
	Keep  bool     `json:"keep,omitempty"`
	// Profile is its own wtm stack profile, "" for the swarm's (see
	// provisionPlan.profileOf).
	Profile string `json:"profile,omitempty"`
}

// profileOf is the wtm stack profile worker index starts on: its own, or
// the swarm's.
func (p provisionPlan) profileOf(index int) string {
	if index >= 1 && index <= len(p.Workers) && p.Workers[index-1].Profile != "" {
		return p.Workers[index-1].Profile
	}
	return p.Profile
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

// label is the worker's name everywhere: its role and its rank, reviewer2.
func (w workerSpec) label() string { return w.Role + strconv.Itoa(w.Rank) }

// group is the key the floor of its role is counted by.
func (w workerSpec) group() string {
	if w.Group != "" {
		return w.Group
	}
	return w.Role
}

// buildSlots lays the roles out as the swarm's slots, role by role in
// config order, max slots each, the swarm's kind, model and profile
// under each role's own keys. Without roles, workers and min-workers
// make one role, worker. A prompt that can't be read, or a dir that isn't
// one, refuses to start: a worker meant to verify that silently becomes a
// generic one would skew every dispatch the master makes.
func buildSlots(opts *startOptions, repo string) ([]workerSpec, error) {
	roles := opts.roles
	if len(roles) == 0 {
		max, min := opts.workers, opts.minWorkers
		roles = config.Roles{{Name: "worker", Role: config.Role{Max: &max, Min: &min}}}
	}
	var slots []workerSpec
	for _, r := range roles {
		w := workerSpec{Kind: opts.workerKind, Model: opts.workerModel, Role: r.Name}
		if r.Min != nil {
			w.Min = *r.Min
		}
		if r.Kind != nil {
			w.Kind = *r.Kind
		}
		if r.Model != nil {
			w.Model = *r.Model
		}
		if r.Prompt != nil {
			path := *r.Prompt
			if !filepath.IsAbs(path) {
				path = filepath.Join(repo, path)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("roles %s: prompt unreadable: %w", r.Name, err)
			}
			w.PromptPath, w.Prompt = path, string(content)
		}
		if r.Dir != nil {
			dir, err := validateAgentDir(repo, *r.Dir)
			if err != nil {
				return nil, fmt.Errorf("roles %s: dir: %w", r.Name, err)
			}
			w.Dir = dir
		}
		for _, k := range r.Tasks {
			if k = strings.TrimSpace(k); k == "" || strings.HasPrefix(k, "-") || strings.ContainsAny(k, " \t") {
				return nil, fmt.Errorf("roles %s: tasks: %q is not a task kind (one word, like need-review)", r.Name, k)
			}
			w.Tasks = append(w.Tasks, k)
		}
		w.Overridden = len(w.Tasks) > 0
		if r.Profile != nil {
			w.Profile = *r.Profile
		}
		for rank := 1; rank <= *r.Max; rank++ {
			slot := w
			slot.Rank = rank
			slot.Tasks = slices.Clone(w.Tasks)
			slots = append(slots, slot)
		}
	}
	return slots, nil
}

// slotOf is the slot arg names, by its label (reviewer2) or its herdr
// name (reviewer2-<slug>): an exact match, so review1 never reads as
// reviewer1. An unknown name is refused with the ones there are.
func (p provisionPlan) slotOf(arg, slug string) (index int, label string, err error) {
	for i, w := range p.Workers {
		if l := w.label(); arg == l || arg == names.Agent(slug, l) {
			return i + 1, l, nil
		}
	}
	return 0, "", fmt.Errorf("%q is not a worker of this swarm (%s, or its herdr name <name>-%s); the worker and what follows are separate arguments", arg, strings.Join(p.labels(), ", "), slug)
}

// minOpen is how many workers the roles keep open with nothing queued:
// each role's min, once.
func minOpen(slots []workerSpec) int {
	n := 0
	seen := map[string]bool{}
	for _, s := range slots {
		if !seen[s.group()] {
			seen[s.group()] = true
			n += s.Min
		}
	}
	return n
}

// labelOf is slot index's name, or the index's legacy one outside the
// slots.
func (p provisionPlan) labelOf(index int) string {
	if index >= 1 && index <= len(p.Workers) {
		return p.Workers[index-1].label()
	}
	return fmt.Sprintf("worker%d", index)
}

// labels are the names of the swarm's workers, in slot order.
func (p provisionPlan) labels() []string {
	out := make([]string, len(p.Workers))
	for i, w := range p.Workers {
		out[i] = w.label()
	}
	return out
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
		out[i] = brief.Worker{Kind: w.Kind, Model: w.Model, Label: w.label(), Role: w.Role, Min: w.Min, Prompt: w.Prompt, Dir: w.Dir, Tasks: w.Tasks, Profile: w.Profile}
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
		label := w.label()
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
		parts[i] = w.label() + " " + brief.DescribeAgent(w.Kind, w.Model)
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
	// Workers is every slot that may be opened, in role order (see
	// buildSlots); slot i is pool index i+1.
	Workers []workerSpec `json:"workers"`
	// Inbox is where pings go for a master that watches one, "" when they
	// are typed into it (see inboxWatchCommand).
	Inbox string `json:"inbox,omitempty"`
	// SwitchAllowed is set when wtm has switch: the workers with a stack
	// may run it without a prompt.
	SwitchAllowed bool `json:"switch_allowed,omitempty"`
	// PRWatch is set when the watcher follows the PRs: it is what tells
	// acw a task's PR was merged (see queuedTask.AfterMerge).
	PRWatch bool `json:"pr_watch,omitempty"`
}
