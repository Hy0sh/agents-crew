package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// acw clear resets a worker's context before a new task and says when it
// has taken: the master used to send /clear and read the pane, which once
// showed a render from before the clear (22 % of context) and was taken
// for a clear that failed. What proves it here is the worker's status line
// coming back with a new session_id.

const (
	clearIdleTimeout = 10 * time.Minute
	clearTimeout     = 60 * time.Second
)

// cleared reports whether u comes from the session a /clear started. With
// no session read before, nothing tells an old render from a new one.
func cleared(before string, u usage) bool {
	return before != "" && u.SessionID != "" && u.SessionID != before
}

// clearLabel turns what the master names a worker by, workerN or its herdr
// name workerN-<slug> (the one the brief lists), into workerN and N.
// Anything else is refused: the label becomes a file name.
func clearLabel(arg, slug string) (string, int, error) {
	if index, ok := names.WorkerIndex(arg, slug); ok {
		return fmt.Sprintf("worker%d", index), index, nil
	}
	return "", 0, fmt.Errorf("%q is not a worker of this swarm (worker1, or its herdr name worker1-%s); the worker and what follows are separate arguments", arg, slug)
}

// clearRefusal is why a worker must not be sent /clear, "" when it can.
// Sent to a blocked worker, /clear queues behind the prompt; sent to one
// without acw's status line, nothing could confirm it.
func clearRefusal(label, status string, hasUsage bool) string {
	switch {
	case status == "blocked":
		return label + " is blocked: resolve its pending request first, then run acw clear again."
	case !hasUsage:
		return label + " doesn't have acw's statusline (not a claude worker, or started by an older acw): reset it by hand and check its pane."
	}
	return ""
}

// clearTarget is a worker acw clear or acw dispatch acts on, resolved and
// checked by readyToClear.
type clearTarget struct {
	label, name, statusDir, usagePath string
	index                             int
}

func clearWorker(repo, arg string, out io.Writer) error {
	t, err := readyToClear(repo, arg, out)
	if err != nil {
		return err
	}
	return resetContext(t, out)
}

// readyToClear resolves arg and waits until the worker can take a /clear:
// idle, not blocked, with acw's status line to confirm it.
func readyToClear(repo, arg string, out io.Writer) (clearTarget, error) {
	slug := names.Slug(repo)
	label, index, err := clearLabel(arg, slug)
	if err != nil {
		return clearTarget{}, err
	}
	statusDir := names.StatusDir(repo)
	t := clearTarget{label: label, index: index, name: names.Worker(slug, index), statusDir: statusDir,
		usagePath: filepath.Join(statusDir, label+".usage.json")}

	status, err := agentStatus(t.name)
	if err != nil {
		return clearTarget{}, err
	}
	_, statErr := os.Stat(t.usagePath)
	if why := clearRefusal(label, status, statErr == nil); why != "" {
		return clearTarget{}, fmt.Errorf("%s", why)
	}
	if status == "working" {
		fmt.Fprintf(out, "%s is still working, waiting for it to go idle…\n", label)
		if err := herdr.AgentWait(t.name, []string{"idle", "done", "blocked"}, clearIdleTimeout); err != nil {
			return clearTarget{}, fmt.Errorf("%s didn't go idle within %s: %w", label, clearIdleTimeout, err)
		}
		if status, err = agentStatus(t.name); err != nil {
			return clearTarget{}, err
		}
		if why := clearRefusal(label, status, true); why != "" {
			return clearTarget{}, fmt.Errorf("%s", why)
		}
	}
	return t, nil
}

// resetContext sends /clear and returns once the worker's status line
// shows a new session.
func resetContext(t clearTarget, out io.Writer) error {
	// Workers start without a prompt, so no workerN.turn means no turn
	// yet: nothing to reset, and a /clear there keeps its session_id,
	// which the wait below would take for a failed clear.
	if _, err := os.Stat(filepath.Join(t.statusDir, t.label+".turn")); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(out, "%s hasn't had a turn yet: context already empty.\n", t.label)
		return nil
	}

	before, err := readUsage(t.usagePath)
	if err != nil || before.SessionID == "" {
		return fmt.Errorf("%s: current session unreadable in %s, nothing could have confirmed the reset", t.label, t.usagePath)
	}
	if err := herdr.AgentPrompt(t.name, "/clear"); err != nil {
		return err
	}
	for deadline := time.Now().Add(clearTimeout); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if u, err := readUsage(t.usagePath); err == nil && cleared(before.SessionID, u) {
			// A new task starts: the watcher's block count starts over.
			_ = os.Remove(filepath.Join(t.statusDir, t.label+".blocks"))
			fmt.Fprintf(out, "%s: context reset.\n", t.label)
			return nil
		}
	}
	return fmt.Errorf("%s: no new session within %s after /clear, check its pane", t.label, clearTimeout)
}

func agentStatus(name string) (string, error) {
	agents, err := herdr.AgentList()
	if err != nil {
		return "", fmt.Errorf("herdr agent list: %w", err)
	}
	a, ok := herdr.FindAgent(agents, name)
	if !ok {
		return "", fmt.Errorf("agent %s not found in herdr", name)
	}
	return a.Status, nil
}
