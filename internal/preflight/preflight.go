// Package preflight checks that agents-crew's dependencies are installed
// before it does anything, so a missing binary surfaces as one clear
// message instead of a cryptic "executable file not found" a few calls
// deep into the run.
package preflight

import (
	"fmt"
	"os/exec"
	"strings"
)

type dependency struct {
	bin     string
	purpose string
	install string
}

var (
	herdrDep = dependency{
		bin:     "herdr",
		purpose: "orchestre les panes/agents (indispensable, agents-crew ne fait rien sans lui)",
		install: "https://herdr.dev",
	}
	claudeDep = dependency{
		bin:     "claude",
		purpose: "le CLI Claude Code lui-même, lancé dans chaque pane master/worker",
		install: "npm install -g @anthropic-ai/claude-code",
	}
)

// CheckStart verifies herdr and claude are on PATH, returning one combined
// error naming every missing binary with how to install it. wtm is
// checked separately by WarnIfWtmMissing — it's optional, not a hard
// dependency.
func CheckStart() error {
	return check(herdrDep, claudeDep)
}

// CheckStop verifies herdr is on PATH — the only hard dependency of
// tearing a swarm down (it never starts a claude agent).
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

// WarnIfWtmMissing prints a non-fatal note to w when wtm is absent: workers
// still work, they just won't get an isolated environment provisioned
// automatically. Kept separate from CheckStart because not every project
// this tool runs against needs wtm.
func WarnIfWtmMissing(printf func(format string, a ...any)) {
	if _, err := exec.LookPath("wtm"); err != nil {
		printf("note: wtm introuvable — les workers n'auront pas d'environnement isolé provisionné automatiquement (pas bloquant, agents-crew fonctionne sans). Si ce projet en a besoin : https://github.com/Hy0sh/worktree-manager\n")
	}
}
