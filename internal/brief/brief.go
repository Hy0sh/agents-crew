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

func newMasterData(repoPath, slug, workerAgent string, n, maxStacks int, profile, notes string) MasterData {
	return MasterData{
		RepoPath:         repoPath,
		N:                n,
		WorkerAgent:      workerAgent,
		WorkerNames:      workerNamesList(slug, n),
		EnvCapRule:       envCapRule(n, maxStacks),
		StackProfileRule: stackProfileRule(profile),
		RepoRules:        strings.TrimSpace(notes),
	}
}

// Build returns the master's initial brief for a repo at repoPath, with n
// workers of kind workerAgent and maxStacks concurrent isolated
// environments allowed, using the built-in template. slug is the run's
// names.Slug(repoPath), used to name the workers the same way the caller
// actually started them. profile is the stack profile worker environments
// start on ("" for the whole stack) and notes the content of the
// per-project notes file ("" when none), both from internal/config.
func Build(repoPath, slug, workerAgent string, n, maxStacks int, profile, notes string) string {
	var b bytes.Buffer
	if err := masterTemplate.Execute(&b, newMasterData(repoPath, slug, workerAgent, n, maxStacks, profile, notes)); err != nil {
		// templates/master.md is embedded and parsed at init time (template.Must
		// above already panics on a syntax error), so a failure here can only
		// mean a field referenced in the template no longer exists on MasterData.
		panic(fmt.Sprintf("brief: executing master template: %v", err))
	}
	return strings.TrimRight(b.String(), "\n")
}

// BuildFromSource renders a custom master brief template (e.g. read from a
// file passed via --brief) instead of the built-in one. It gets the same
// MasterData fields — {{.RepoPath}}, {{.N}}, {{.WorkerAgent}},
// {{.WorkerNames}}, {{.EnvCapRule}}, {{.StackProfileRule}}, {{.RepoRules}}
// — and any template syntax error is returned rather than panicking,
// since the source comes from the user, not from what's baked into the
// binary.
func BuildFromSource(source, repoPath, slug, workerAgent string, n, maxStacks int, profile, notes string) (string, error) {
	tmpl, err := template.New("custom-master").Parse(source)
	if err != nil {
		return "", fmt.Errorf("parsing custom brief template: %w", err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, newMasterData(repoPath, slug, workerAgent, n, maxStacks, profile, notes)); err != nil {
		return "", fmt.Errorf("executing custom brief template: %w (available variables: %s)", err, Variables())
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
