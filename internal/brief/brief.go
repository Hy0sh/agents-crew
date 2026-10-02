// Package brief builds the initial prompt sent to the master agent. The
// prose itself lives in templates/*.md, not in this file, so it can be
// read and edited like the document it is (no Go string escaping, real
// diffs, syntax highlighting) instead of as embedded Sprintf calls.
package brief

import (
	"bytes"
	_ "embed"
	"fmt"
	"reflect"
	"strings"
	"text/template"

	"github.com/Hy0sh/agents-crew/internal/names"
)

//go:embed templates/master.md
var masterTemplateSource string

//go:embed templates/workers-ready.md
var workersReadyTemplateSource string

// MasterSource is the built-in brief's template source, for a brief-extra
// to be appended to it.
func MasterSource() string { return masterTemplateSource }

var (
	masterTemplate       = template.Must(template.New("master").Parse(masterTemplateSource))
	workersReadyTemplate = template.Must(template.New("workers-ready").Parse(workersReadyTemplateSource))
)

// MasterData is what the master brief template can reference. Exported so
// a custom template (see BuildFromSource) can use the same fields as the
// built-in one.
type MasterData struct {
	RepoPath         string
	N                int
	WorkerAgent      string
	WorkerNames      string
	EnvCapRule       string
	StackProfileRule string // empty when no stack profile is configured
	// RepoRules is the per-project notes file's content, empty when none
	// is configured. Injected verbatim, not summarized: the master
	// reciting the rules from memory into each worker brief is exactly
	// where one gets dropped, and the dropped one costs a force-push.
	RepoRules string
	// PingingWorkers names the workers that have the Stop hook (claude
	// ones), empty when none does.
	PingingWorkers string
	// WorkerOverrides describes the workers configured apart from the
	// others (kind, model, standing instructions), empty when none is.
	WorkerOverrides string
	// InboxWatch is the command the master arms a Monitor on to receive
	// pings, empty when they are typed into its input instead. Kept for
	// custom briefs; the built-in one uses InboxNext.
	InboxWatch string
	// InboxNext is the command the master runs in the background to read
	// its next messages, empty when they are typed into its input.
	InboxNext string
	// SilenceMinutes is how long a working worker may show no activity
	// before acw's watcher tells the master.
	SilenceMinutes int
	// StatusCommand shows every worker at a glance (acw status).
	StatusCommand string
	// ClearCommand, followed by a worker's label, resets its context and
	// confirms it took (acw clear).
	ClearCommand string
	// DispatchCommand, followed by a worker's label and a brief file,
	// hands the worker its next task (acw dispatch).
	DispatchCommand string
	// PRWatch is set when acw's watcher follows the user's open pull
	// requests and sends the master a line per PR that changed.
	PRWatch bool
}

// Params is what a brief is built from. N is len(Workers).
type Params struct {
	RepoPath  string
	Slug      string // names.Slug(RepoPath), to name workers as the caller started them
	MaxStacks int
	Profile   string // stack profile environments start on, "" for the whole stack
	Notes     string // content of the per-project notes file, "" when none
	Workers   []Worker
	// InboxWatch is the master's inbox watch command, "" for none.
	InboxWatch string
	// InboxNext is its background read command, "" for none.
	InboxNext      string
	SilenceMinutes int
	StatusCommand  string
	ClearCommand   string
	// DispatchCommand is acw dispatch, fully written, "" for none.
	DispatchCommand string
	PRWatch         bool
}

// Worker is one worker as it was actually started.
type Worker struct {
	Kind  string
	Model string
	// Prompt is the content of its standing instructions, "" when none.
	Prompt string
	// Overridden is set when the config gave this worker its own
	// settings, so the brief only lists workers that differ.
	Overridden bool
	// Dir is where a worker outside the code runs, "" for a coder.
	Dir string
}

// Variables lists what a custom template can reference, e.g.
// "{{.RepoPath}}, {{.N}}, ...", read off MasterData itself so the error
// on a bad custom template can never list a variable that doesn't exist
// or miss a new one.
func Variables() string {
	t := reflect.TypeOf(MasterData{})
	vars := make([]string, t.NumField())
	for i := range vars {
		vars[i] = "{{." + t.Field(i).Name + "}}"
	}
	return strings.Join(vars, ", ")
}

func newMasterData(p Params) MasterData {
	n := len(p.Workers)
	return MasterData{
		RepoPath:         p.RepoPath,
		N:                n,
		WorkerAgent:      workerAgent(p.Slug, p.Workers),
		WorkerNames:      workerNamesList(p.Slug, n),
		EnvCapRule:       envCapRule(coders(p.Workers), p.MaxStacks),
		StackProfileRule: stackProfileRule(p.Profile),
		RepoRules:        strings.TrimSpace(p.Notes),
		PingingWorkers:   pingingWorkers(p.Slug, p.Workers),
		WorkerOverrides:  workerOverrides(p.Slug, p.Workers),
		InboxWatch:       p.InboxWatch,
		InboxNext:        p.InboxNext,
		SilenceMinutes:   p.SilenceMinutes,
		StatusCommand:    p.StatusCommand,
		ClearCommand:     p.ClearCommand,
		DispatchCommand:  p.DispatchCommand,
		PRWatch:          p.PRWatch,
	}
}

// workerAgent is the workers' kind when they all share one — the value
// custom templates compared against "claude" before kinds could differ —
// and a per-worker description otherwise.
func workerAgent(slug string, workers []Worker) string {
	if len(workers) == 0 {
		return ""
	}
	same := true
	for _, w := range workers {
		if w.Kind != workers[0].Kind {
			same = false
			break
		}
	}
	if same {
		return workers[0].Kind
	}
	parts := make([]string, len(workers))
	for i, w := range workers {
		parts[i] = names.Worker(slug, i+1) + " " + w.Kind
	}
	return "mixed: " + strings.Join(parts, ", ")
}

// coders counts the workers in the code: only they need an environment.
func coders(workers []Worker) int {
	n := 0
	for _, w := range workers {
		if w.Dir == "" {
			n++
		}
	}
	return n
}

// pingingWorkers names the claude workers: only they get the Stop hook
// (see workerArgs in cmd/acw).
func pingingWorkers(slug string, workers []Worker) string {
	var list []string
	for i, w := range workers {
		if w.Kind == "claude" {
			list = append(list, names.Worker(slug, i+1))
		}
	}
	return strings.Join(list, ", ")
}

// workerOverrides tells the master which workers are set apart and how,
// with their instructions in full: it needs them to dispatch (a worker
// told to verify must not get a feature to write), and must copy them
// into every brief of a worker whose kind has no system prompt acw can
// set — a context reset wipes them otherwise.
func workerOverrides(slug string, workers []Worker) string {
	var b strings.Builder
	var generic []string
	for i, w := range workers {
		if !w.Overridden {
			generic = append(generic, names.Worker(slug, i+1))
			continue
		}
		name := names.Worker(slug, i+1)
		fmt.Fprintf(&b, "- %s runs on %s", name, DescribeAgent(w.Kind, w.Model))
		if w.Dir != "" {
			fmt.Fprintf(&b, ", outside the code, in %s: no worktree, no environment, no branch. NEVER give it code, "+
				"and the worktree, branch, environment and PR rules above do not apply to it; "+
				"its status file is at the same absolute path as the others', under the repo", w.Dir)
		}
		if w.Prompt == "" {
			b.WriteString(", with no instructions of its own.\n")
			continue
		}
		if w.Kind == "claude" {
			b.WriteString(". Its own instructions are already in its system prompt, they survive its resets: do NOT copy them into its briefs, only take them into account when assigning it tasks.\n")
		} else {
			b.WriteString(". Its agent has no system prompt this setup can set: copy its own instructions VERBATIM into EACH of its briefs, after every reset, and take them into account when assigning it tasks.\n")
		}
		fmt.Fprintf(&b, "<<<INSTRUCTIONS FOR %s\n%s\nEND OF INSTRUCTIONS FOR %s>>>\n", name, strings.TrimSpace(w.Prompt), name)
	}
	// Said rather than left to inference: without it the master has to
	// guess that a worker it was told nothing about takes everything else.
	if b.Len() > 0 && len(generic) > 0 {
		fmt.Fprintf(&b, "- %s: no instructions of their own, general-purpose, they take the tasks that belong to none of the workers above.\n", strings.Join(generic, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// DescribeAgent names an agent by its kind and model, the kind alone when
// no model was asked for — in the brief and on acw's launch line alike.
func DescribeAgent(kind, model string) string {
	if model == "" {
		return kind
	}
	return kind + " " + model
}

// Build returns the master's initial brief, using the built-in template.
func Build(p Params) string {
	var b bytes.Buffer
	if err := masterTemplate.Execute(&b, newMasterData(p)); err != nil {
		// templates/master.md is embedded and parsed at init time (template.Must
		// above already panics on a syntax error), so a failure here can only
		// mean a field referenced in the template no longer exists on MasterData.
		panic(fmt.Sprintf("brief: executing master template: %v", err))
	}
	return strings.TrimRight(b.String(), "\n")
}

// BuildFromSource renders a custom master brief template (e.g. read from a
// file passed via --brief) instead of the built-in one. It gets the same
// MasterData fields (see Variables), and any template error is returned
// rather than panicking, since the source comes from the user, not from
// what's baked into the binary.
func BuildFromSource(source string, p Params) (string, error) {
	tmpl, err := template.New("custom-master").Parse(source)
	if err != nil {
		return "", fmt.Errorf("invalid custom brief: %w", err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, newMasterData(p)); err != nil {
		return "", fmt.Errorf("custom brief: %w (available variables: %s)", err, Variables())
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// WorkersReadyMessage is sent to the master once background provisioning
// finishes.
func WorkersReadyMessage(slug string, n int) string {
	var b bytes.Buffer
	data := struct{ WorkerNames string }{WorkerNames: workerNamesList(slug, n)}
	if err := workersReadyTemplate.Execute(&b, data); err != nil {
		panic(fmt.Sprintf("brief: executing workers-ready template: %v", err))
	}
	return strings.TrimRight(b.String(), "\n")
}

func envCapRule(n, maxStacks int) string {
	rule := fmt.Sprintf("there are %d workers in the code but the machine only supports %d isolated environments (stacks) at the same time. ", n, maxStacks)
	if maxStacks < n {
		return rule + fmt.Sprintf(
			"Only the first %d workers have an environment at startup; the others have their worktree but no environment mounted. "+
				"YOU arbitrate: before a worker without an environment needs one, release the one of a worker that is idle or has "+
				"just finished (never an active worker), then assign it to the one that needs it. Never the other way round, never more than "+
				"%d environments mounted at the same time across all workers. This is a stopgap until something better (a real "+
				"queue): be explicit with me about who is waiting for what if it gets confusing.",
			maxStacks, maxStacks)
	}
	return rule + "Here capacity covers all workers, no arbitration needed."
}

// stackProfileRule states the intent, not the command: the brief never
// hardcodes the project's tooling (see the README's design notes), the
// master finds how to switch profiles in the project's own docs.
func stackProfileRule(profile string) string {
	if profile == "" {
		return ""
	}
	return fmt.Sprintf("the workers' environments start on the stack profile \"%s\", not on the full stack. "+
		"If a task needs services missing from this profile, have that worker's environment switched to another profile of the project "+
		"BEFORE it starts (the available profiles and how to switch are in the project's environment tooling), "+
		"and bring it back to \"%s\" once the task is done: a heavier profile takes more memory from all the others", profile, profile)
}

func workerNamesList(slug string, n int) string {
	list := make([]string, n)
	for i := 1; i <= n; i++ {
		list[i-1] = names.Worker(slug, i)
	}
	return strings.Join(list, ", ")
}
