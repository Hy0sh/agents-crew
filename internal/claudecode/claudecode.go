// Package claudecode holds what acw does only because an agent of the
// swarm is Claude Code. The rest of acw stays agnostic of the agent it
// runs: it calls in here when the agent's capabilities (see harness) ask
// for it, and nowhere else.
package claudecode

import (
	"github.com/Hy0sh/agents-crew/internal/gitutil"
)

// ExcludeWorktrees keeps .claude/worktrees/, where acw keeps the workers'
// worktrees and the swarm's state, out of git's untracked files. Claude
// Code shows, under a Bash command, the diff of every file it changed that
// git does not ignore: every acw command the master runs rewrote pool.json
// or queue.json there, briefs included, all over the user's reading.
func ExcludeWorktrees(repo string) error {
	return gitutil.Exclude(repo, ".claude/worktrees/")
}
