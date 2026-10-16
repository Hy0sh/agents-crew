// Package claudecode holds what acw does only because an agent of the
// swarm is Claude Code. The rest of acw stays agnostic of the agent it
// runs: it calls in here when the agent's capabilities (see harness) ask
// for it, and nowhere else.
package claudecode

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

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

// BriefsDir is the one folder the master writes in, with its Write tool:
// the briefs and messages it hands acw. Claude Code shows a Write as a
// preview, where a heredoc in a Bash command showed in full. Edit and the
// other file tools stay denied, and a hook keeps Write there (see
// GuardWrite): the master still never edits the code.
func BriefsDir(statusDir string) string { return filepath.Join(statusDir, "briefs") }

// WriteRule lets the master write in dir without a prompt: Claude Code's
// Edit rules cover Write, and // starts an absolute path.
func WriteRule(dir string) string { return "Edit(/" + dir + "/**)" }

// GuardWrite answers the master's PreToolUse hook: nothing for a Write in
// dir, which goes on, a deny for any other tool or path.
func GuardWrite(dir string, in io.Reader, out io.Writer) error {
	var input toolUse
	if err := json.NewDecoder(in).Decode(&input); err != nil {
		return err
	}
	return guard(dir, input, out)
}

// toolUse is what a PreToolUse hook reads of the call.
type toolUse struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

func guard(dir string, t toolUse, out io.Writer) error {
	if t.ToolName == "Write" && inside(dir, t.ToolInput.FilePath) {
		return nil
	}
	return json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": fmt.Sprintf("acw: the master dispatches and supervises, it never edits files nor plans in plan mode; it writes its briefs and messages with Write in %s, and nowhere else", dir),
	}})
}

// inside reports whether path, absolute, is in dir.
func inside(dir, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
