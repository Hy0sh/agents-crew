// Package preflight checks that agents-crew's dependencies are installed
// before it does anything, so a missing binary surfaces as one clear
// message instead of a cryptic "executable file not found" a few calls
// deep into the run.
package preflight

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/wtm"
)

type dependency struct {
	bin     string
	purpose string
	install string
}

var (
	herdrDep = dependency{
		bin:     "herdr",
		purpose: "orchestre les panes/agents (indispensable, acw ne fait rien sans lui)",
		install: "https://herdr.dev",
	}
	claudeDep = dependency{
		bin:     "claude",
		purpose: "le CLI Claude Code lui-même, lancé dans chaque pane master/worker",
		install: "npm install -g @anthropic-ai/claude-code",
	}
)

// CheckStart verifies herdr and the CLI of each distinct agent kind are on
// PATH, returning one combined error naming every missing binary with how
// to install it. wtm is checked separately by WarnIfWtmMissing — it's
// optional, not a hard dependency.
func CheckStart(kinds ...string) error {
	deps := []dependency{herdrDep}
	seen := map[string]bool{}
	for _, k := range kinds {
		if seen[k] {
			continue
		}
		seen[k] = true
		deps = append(deps, kindDep(k))
	}
	return check(deps...)
}

// kindDep describes the CLI herdr starts for an agent kind. A kind's name
// is its canonical executable, so that's the binary to look for.
func kindDep(kind string) dependency {
	if kind == "claude" {
		return claudeDep
	}
	return dependency{
		bin:     kind,
		purpose: fmt.Sprintf("le CLI de l'agent demandé (--master-kind/--worker-kind %s), lancé dans les panes", kind),
		install: fmt.Sprintf("aucun binaire %q dans le PATH — installe ce CLI, ou choisis un autre kind", kind),
	}
}

// CheckStop verifies herdr is on PATH — the only hard dependency of
// tearing a swarm down (it never starts an agent).
func CheckStop() error {
	return check(herdrDep)
}

func check(deps ...dependency) error {
	var missing []string
	for _, d := range deps {
		if _, err := exec.LookPath(d.bin); err != nil {
			missing = append(missing, fmt.Sprintf("  - %s : %s\n    installation : %s", d.bin, d.purpose, d.install))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("dépendance(s) manquante(s) :\n%s", strings.Join(missing, "\n"))
}

// WarnIfAgentsFileMissing prints a non-fatal note to w when the workers
// run on something other than Claude Code in a repo whose project
// instructions only exist as a CLAUDE.md. AGENTS.md is what every other
// agent CLI reads; Claude Code reads it too, but only when no CLAUDE.md
// shadows it. So a CLAUDE.md-only repo leaves a codex/gemini/... worker
// with *no* project instructions at all — no error, no signal, just code
// written outside the repo's conventions.
func WarnIfAgentsFileMissing(repo, workerKind string, printf func(format string, a ...any)) {
	if workerKind == "claude" {
		return
	}
	home, _ := os.UserHomeDir()
	claudeFile := ""
	// Same lookup as Claude Code: cwd and every directory above it. The
	// user-level ~/.claude/CLAUDE.md is deliberately excluded — it loads
	// *alongside* AGENTS.md instead of shadowing it, and it exists on most
	// machines, which would make this warning fire forever.
	for dir := repo; ; dir = filepath.Dir(dir) {
		candidates := []string{"CLAUDE.md", "CLAUDE.local.md"}
		if dir != home {
			candidates = append(candidates, filepath.Join(".claude", "CLAUDE.md"))
		}
		if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
			return
		}
		for _, name := range candidates {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil && claudeFile == "" {
				claudeFile = filepath.Join(dir, name)
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	if claudeFile == "" {
		return
	}
	printf("note: les workers tournent en --worker-kind %s, et les instructions projet ne vivent que dans %s (aucun AGENTS.md) — "+
		"ces workers ne liront AUCUNE instruction projet, sans la moindre erreur. Porte les conventions dans un AGENTS.md et laisse "+
		"un CLAUDE.md qui contient @AGENTS.md (Claude Code ne lit son AGENTS.md natif qu'en l'absence de CLAUDE.md).\n",
		workerKind, claudeFile)
}

// WarnIfWtmMissing prints a non-fatal note to w when wtm is absent: workers
// still work, they just won't get an isolated environment provisioned
// automatically. Kept separate from CheckStart because not every project
// this tool runs against needs wtm.
func WarnIfWtmMissing(printf func(format string, a ...any)) {
	if !wtm.Available() {
		printf("note: wtm introuvable — les workers n'auront pas d'environnement isolé provisionné automatiquement (pas bloquant, acw fonctionne sans). Si ce projet en a besoin : https://github.com/Hy0sh/worktree-manager\n")
	}
}
