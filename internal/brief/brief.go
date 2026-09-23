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
}

// Params is what a brief is built from. N is len(Workers).
type Params struct {
	RepoPath  string
	Slug      string // names.Slug(RepoPath), to name workers as the caller started them
	MaxStacks int
	Profile   string // stack profile environments start on, "" for the whole stack
	Notes     string // content of the per-project notes file, "" when none
	Workers   []Worker
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
}

// Variables lists what a custom template can reference, e.g.
// "{{.RepoPath}}, {{.N}}, ...", read off MasterData itself so --help can
// never list a variable that doesn't exist or miss a new one.
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
		EnvCapRule:       envCapRule(n, p.MaxStacks),
		StackProfileRule: stackProfileRule(p.Profile),
		RepoRules:        strings.TrimSpace(p.Notes),
		PingingWorkers:   pingingWorkers(p.Slug, p.Workers),
		WorkerOverrides:  workerOverrides(p.Slug, p.Workers),
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
	return "mixte : " + strings.Join(parts, ", ")
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
		fmt.Fprintf(&b, "- %s tourne sur %s", name, DescribeAgent(w.Kind, w.Model))
		if w.Prompt == "" {
			b.WriteString(", sans consignes propres.\n")
			continue
		}
		if w.Kind == "claude" {
			b.WriteString(". Ses consignes propres sont déjà dans son prompt système, elles survivent à ses réinitialisations : ne les recopie PAS dans ses briefs, tiens-en seulement compte pour lui attribuer des tâches.\n")
		} else {
			b.WriteString(". Son agent n'a pas de prompt système réglable par ce dispositif : recopie VERBATIM ses consignes propres dans CHACUN de ses briefs, après chaque réinitialisation, et tiens-en compte pour lui attribuer des tâches.\n")
		}
		fmt.Fprintf(&b, "<<<CONSIGNES DE %s\n%s\nFIN DES CONSIGNES DE %s>>>\n", name, strings.TrimSpace(w.Prompt), name)
	}
	// Said rather than left to inference: without it the master has to
	// guess that a worker it was told nothing about takes everything else.
	if b.Len() > 0 && len(generic) > 0 {
		fmt.Fprintf(&b, "- %s : aucune consigne propre, polyvalent(s), ils prennent les tâches qui ne relèvent d'aucun worker ci-dessus.\n", strings.Join(generic, ", "))
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
		return "", fmt.Errorf("brief personnalisé invalide: %w", err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, newMasterData(p)); err != nil {
		return "", fmt.Errorf("brief personnalisé: %w (variables disponibles : %s)", err, Variables())
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
	rule := fmt.Sprintf("il y a %d workers mais la machine ne supporte que %d environnements isolés (stacks) en même temps. ", n, maxStacks)
	if maxStacks < n {
		return rule + fmt.Sprintf(
			"Seuls les %d premiers workers ont un environnement au démarrage ; les autres ont leur worktree mais pas d'environnement monté. "+
				"C'est TOI qui arbitres : avant qu'un worker sans environnement en ait besoin, libère celui d'un worker inactif ou qui "+
				"vient de finir (jamais un worker actif), puis attribue-le à celui qui en a besoin — jamais l'inverse, jamais plus de "+
				"%d environnements montés en même temps tous workers confondus. C'est un pis-aller en attendant mieux (une vraie file "+
				"d'attente) — sois explicite avec moi sur qui attend quoi si ça devient confus.",
			maxStacks, maxStacks)
	}
	return rule + "Ici la capacité couvre tous les workers, pas d'arbitrage nécessaire."
}

// stackProfileRule states the intent, not the command: the brief never
// hardcodes the project's tooling (see the README's design notes), the
// master finds how to switch profiles in the project's own docs.
func stackProfileRule(profile string) string {
	if profile == "" {
		return ""
	}
	return fmt.Sprintf("les environnements des workers démarrent sur le profile de stack « %s », pas sur la stack complète. "+
		"Si une tâche a besoin de services absents de ce profile, fais basculer l'environnement de ce worker sur un autre profile du projet "+
		"AVANT qu'il ne commence (les profiles disponibles et la façon d'en changer sont dans l'outillage d'environnement du projet), "+
		"et ramène-le sur « %s » une fois la tâche finie : un profile plus lourd occupe plus de mémoire pour tous les autres", profile, profile)
}

func workerNamesList(slug string, n int) string {
	list := make([]string, n)
	for i := 1; i <= n; i++ {
		list[i-1] = names.Worker(slug, i)
	}
	return strings.Join(list, ", ")
}
