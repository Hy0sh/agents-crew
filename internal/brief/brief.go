// Package brief builds the initial prompt sent to the master agent. The
// prose itself lives in templates/*.md, not in this file, so it can be
// read and edited like the document it is (no Go string escaping, real
// diffs, syntax highlighting) instead of as embedded Sprintf calls.
package brief

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
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
	RepoPath    string
	N           int
	WorkerNames string
	EnvCapRule  string
}

func newMasterData(repoPath string, n, maxStacks int) MasterData {
	return MasterData{
		RepoPath:    repoPath,
		N:           n,
		WorkerNames: workerNamesList(n),
		EnvCapRule:  envCapRule(n, maxStacks),
	}
}

// Build returns the master's initial brief for a repo at repoPath, with n
// workers and maxStacks concurrent isolated environments allowed, using
// the built-in template.
func Build(repoPath string, n, maxStacks int) string {
	var b bytes.Buffer
	if err := masterTemplate.Execute(&b, newMasterData(repoPath, n, maxStacks)); err != nil {
		// templates/master.md is embedded and parsed at init time (template.Must
		// above already panics on a syntax error), so a failure here can only
		// mean a field referenced in the template no longer exists on MasterData.
		panic(fmt.Sprintf("brief: executing master template: %v", err))
	}
	return strings.TrimRight(b.String(), "\n")
}

// BuildFromSource renders a custom master brief template (e.g. read from a
// file passed via --brief) instead of the built-in one. It gets the same
// MasterData fields — {{.RepoPath}}, {{.N}}, {{.WorkerNames}},
// {{.EnvCapRule}} — and any template syntax error is returned rather than
// panicking, since the source comes from the user, not from what's baked
// into the binary.
func BuildFromSource(source, repoPath string, n, maxStacks int) (string, error) {
	tmpl, err := template.New("custom-master").Parse(source)
	if err != nil {
		return "", fmt.Errorf("parsing custom brief template: %w", err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, newMasterData(repoPath, n, maxStacks)); err != nil {
		return "", fmt.Errorf("executing custom brief template: %w", err)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// WorkersReadyMessage is sent to the master once background provisioning
// finishes.
func WorkersReadyMessage(n int) string {
	var b bytes.Buffer
	data := struct{ WorkerNames string }{WorkerNames: workerNamesList(n)}
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

func workerNamesList(n int) string {
	names := make([]string, n)
	for i := 1; i <= n; i++ {
		names[i-1] = fmt.Sprintf("worker%d", i)
	}
	return strings.Join(names, ", ")
}
