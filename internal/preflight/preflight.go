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
		purpose: "orchestrates the panes/agents (required, acw does nothing without it)",
		install: "https://herdr.dev",
	}
	claudeDep = dependency{
		bin:     "claude",
		purpose: "the Claude Code CLI itself, started in every master/worker pane",
		install: "npm install -g @anthropic-ai/claude-code",
	}
	ghDep = dependency{
		bin:     "gh",
		purpose: "the GitHub CLI, which pr-watch polls your open pull requests with",
		install: "https://cli.github.com, then gh auth login",
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
		purpose: fmt.Sprintf("the CLI of the requested agent (--master-kind/--worker-kind %s), started in the panes", kind),
		install: fmt.Sprintf("no %q binary in PATH: install that CLI, or pick another kind", kind),
	}
}

// CheckStop verifies herdr is on PATH — the only hard dependency of
// tearing a swarm down (it never starts an agent).
func CheckStop() error {
	return check(herdrDep)
}

// CheckPRWatch verifies gh is installed, for pr-watch: a watch that cannot
// reach GitHub would stay silent, and the master would take that silence
// for "nothing changed". Whether gh is logged in to the repo's host is
// gh's own answer when acw asks it for the repo.
func CheckPRWatch() error {
	return check(ghDep)
}

func check(deps ...dependency) error {
	var missing []string
	for _, d := range deps {
		if _, err := exec.LookPath(d.bin); err != nil {
			missing = append(missing, fmt.Sprintf("  - %s: %s\n    install: %s", d.bin, d.purpose, d.install))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing dependenc(ies):\n%s", strings.Join(missing, "\n"))
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
	printf("note: the workers run with --worker-kind %s, and the project instructions only live in %s (no AGENTS.md): "+
		"these workers will read NO project instructions, without any error. Move the conventions into an AGENTS.md and keep "+
		"a CLAUDE.md that contains @AGENTS.md (Claude Code only reads its native AGENTS.md when there is no CLAUDE.md).\n",
		workerKind, claudeFile)
}

// WarnIfWtmMissing prints a non-fatal note to w when wtm is absent: workers
// still work, they just won't get an isolated environment provisioned
// automatically. Kept separate from CheckStart because not every project
// this tool runs against needs wtm.
func WarnIfWtmMissing(printf func(format string, a ...any)) {
	if !wtm.Available() {
		printf("note: wtm not found: the workers won't get an isolated environment provisioned automatically (not blocking, acw works without it). If this project needs one: https://github.com/Hy0sh/worktree-manager\n")
	}
}
