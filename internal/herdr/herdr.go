// Package herdr wraps the `herdr` CLI (https://herdr.dev): every command
// returns JSON on stdout, this package runs the command and decodes the
// pieces callers need. It never guesses IDs — they are always parsed from
// the tool's own output.
package herdr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type envelope struct {
	Result json.RawMessage `json:"result"`
}

// run executes `herdr <args...>` and returns its stdout, or an error
// wrapping stderr when the command exits non-zero.
func run(args ...string) ([]byte, error) {
	cmd := exec.Command("herdr", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("herdr %v: %w: %s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// runResult executes `herdr <args...>` and decodes its .result into v.
func runResult(v any, args ...string) error {
	out, err := run(args...)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("herdr %v: decoding envelope: %w", args, err)
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(env.Result, v)
}

// Agent is one entry from `herdr agent list`.
type Agent struct {
	Name        string `json:"name"`
	WorkspaceID string `json:"workspace_id"`
	// Status is herdr's own reading of the agent: idle, working, blocked,
	// done or unknown.
	Status string `json:"agent_status"`
	Cwd    string `json:"cwd"`
}

// AgentList returns every live agent across all workspaces.
func AgentList() ([]Agent, error) {
	var result struct {
		Agents []Agent `json:"agents"`
	}
	if err := runResult(&result, "agent", "list"); err != nil {
		return nil, err
	}
	return result.Agents, nil
}

// WorkspaceCreate creates a workspace rooted at cwd and returns its ID and
// the ID of its single root pane.
func WorkspaceCreate(cwd, label string, focus bool) (workspaceID, rootPaneID string, err error) {
	args := []string{"workspace", "create", "--cwd", cwd, "--label", label}
	if focus {
		args = append(args, "--focus")
	}
	var result struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	if err := runResult(&result, args...); err != nil {
		return "", "", err
	}
	return result.Workspace.WorkspaceID, result.RootPane.PaneID, nil
}

// WorkspaceClose closes a workspace. With group=true, its linked worktree
// workspaces close too.
func WorkspaceClose(workspaceID string, group bool) error {
	args := []string{"workspace", "close", workspaceID}
	if group {
		args = append(args, "--group")
	}
	_, err := run(args...)
	return err
}

// WorkspaceFocus brings a workspace to the front.
func WorkspaceFocus(workspaceID string) error {
	_, err := run("workspace", "focus", workspaceID)
	return err
}

// PaneRename sets a pane's display label.
func PaneRename(paneID, name string) error {
	_, err := run("pane", "rename", paneID, name)
	return err
}

// PaneSplit splits paneID in the given direction, giving it `ratio` of the
// space and setting the new pane's cwd. It returns the new pane's ID.
func PaneSplit(paneID, direction string, ratio float64, cwd string) (newPaneID string, err error) {
	args := []string{"pane", "split", paneID, "--direction", direction,
		"--ratio", fmt.Sprintf("%.4f", ratio), "--cwd", cwd}
	var result struct {
		Pane struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	}
	if err := runResult(&result, args...); err != nil {
		return "", err
	}
	return result.Pane.PaneID, nil
}

// AgentStart starts an agent of the given kind (herdr's `--kind`: claude,
// codex, gemini...) named `name` in paneID. extraArgs, if any, are
// forwarded to that kind's own CLI after `--`.
//
// A pane fresh off `workspace create`/`pane split` can still have its
// shell initializing (oh-my-zsh, profile scripts...) for a moment after
// the pane itself exists — herdr then refuses the start with
// "agent_pane_busy" even though nothing is actually wrong. That's
// retried with backoff here rather than surfaced as a real failure.
func AgentStart(name, kind, paneID string, extraArgs ...string) error {
	args := []string{"agent", "start", name, "--kind", kind, "--pane", paneID}
	if len(extraArgs) > 0 {
		args = append(args, "--")
		args = append(args, extraArgs...)
	}

	const maxAttempts = 10
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		if _, err := run(args...); err != nil {
			lastErr = err
			if strings.Contains(err.Error(), "agent_pane_busy") {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("pane %s never became available after %d attempts: %w", paneID, maxAttempts, lastErr)
}

// FindAgent returns the agent named name among agents.
func FindAgent(agents []Agent, name string) (Agent, bool) {
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// AgentPrompt submits text to a running agent, without waiting for it to
// settle (fire-and-forget, matching agents-crew's brief delivery).
func AgentPrompt(name, text string) error {
	_, err := run("agent", "prompt", name, text)
	return err
}

// AgentWait blocks until the agent reaches one of the states in until, or
// timeout passes (an error then).
func AgentWait(name string, until []string, timeout time.Duration) error {
	args := []string{"agent", "wait", name, "--timeout", fmt.Sprint(timeout.Milliseconds())}
	for _, s := range until {
		args = append(args, "--until", s)
	}
	_, err := run(args...)
	return err
}

// AgentRead returns the last lines of an agent's terminal, unwrapped, as
// herdr prints them: plain text, not JSON.
func AgentRead(name string, lines int) (string, error) {
	out, err := run("agent", "read", name, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines))
	return string(out), err
}
