// Package harness declares the agents acw runs, and what it can do with
// each beyond typing into its pane. Every call site asks for a capability
// by name instead of comparing a kind to "claude": the default is what
// acw does with any agent, and an agent's entry declares what it does
// better. A kind not declared here is refused when the config is read.
package harness

import (
	"fmt"
	"slices"
	"strings"
)

// Agent is what acw knows of one kind of agent. The zero value is the
// generic agent: a brief typed into its pane, and nothing else.
type Agent struct {
	Kind string
	// SystemPrompt: it takes standing instructions as a system prompt
	// file, which survives a context reset, so they leave the briefs.
	SystemPrompt bool
	// Settings: it takes, at start, the settings acw installs through it:
	// the end-of-turn hook that pings the master, the status line that
	// records its usage, the tools it may or may not use.
	Settings bool
	// ResetsContext: acw can clear its context between two tasks, so a
	// free worker is kept for the next one instead of closed.
	ResetsContext bool
	// InboxMonitor: as master, it watches its inbox with a command in the
	// background, instead of having every message typed into its input.
	InboxMonitor bool
	// ReadsClaudeMD: it reads CLAUDE.md, so a repo without AGENTS.md
	// still gives it the project's instructions.
	ReadsClaudeMD bool
	// ShowsUntrackedDiffs: it shows the diff of every untracked file a
	// command changed, so acw's state files must be ignored by git.
	ShowsUntrackedDiffs bool
	// Install says how to install its CLI, "" for the generic hint.
	Install string
}

// claude is Claude Code, the agent acw was built on.
var claude = Agent{
	Kind:                "claude",
	SystemPrompt:        true,
	Settings:            true,
	ResetsContext:       true,
	InboxMonitor:        true,
	ReadsClaudeMD:       true,
	ShowsUntrackedDiffs: true,
	Install:             "npm install -g @anthropic-ai/claude-code",
}

// generic are the other kinds herdr starts (herdr agent start --kind):
// acw runs them through their pane only.
var generic = []string{"pi", "codex", "gemini", "cursor", "devin", "agy", "cline", "omp", "mastracode",
	"opencode", "copilot", "kimi", "kiro", "droid", "amp", "grok", "hermes", "kilo", "qodercli", "qwen", "letta", "maki", "muse"}

// Lookup returns what acw knows of kind, or an error naming the kinds it
// knows when it knows none by that name.
func Lookup(kind string) (Agent, error) {
	if kind == claude.Kind {
		return claude, nil
	}
	if slices.Contains(generic, kind) {
		return Agent{Kind: kind}, nil
	}
	return Agent{}, fmt.Errorf("%q is not an agent kind acw knows: %s", kind, strings.Join(Kinds(), ", "))
}

// Of is Lookup for a kind already checked: an unknown one gets the
// generic agent.
func Of(kind string) Agent {
	a, err := Lookup(kind)
	if err != nil {
		return Agent{Kind: kind}
	}
	return a
}

// Kinds lists the known kinds, claude first.
func Kinds() []string {
	return append([]string{claude.Kind}, generic...)
}
