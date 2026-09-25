package main

import (
	"fmt"
	"io"
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
	var index int
	if _, err := fmt.Sscanf(arg, "worker%d", &index); err == nil && index >= 1 {
		label := fmt.Sprintf("worker%d", index)
		if arg == label || arg == names.Worker(slug, index) {
			return label, index, nil
		}
	}
	return "", 0, fmt.Errorf("%q n'est pas un worker de ce swarm (worker1, ou son nom herdr worker1-%s)", arg, slug)
}

// clearRefusal is why a worker must not be sent /clear, "" when it can.
// Sent to a blocked worker, /clear queues behind the prompt; sent to one
// without acw's status line, nothing could confirm it.
func clearRefusal(label, status string, hasUsage bool) string {
	switch {
	case status == "blocked":
		return label + " est bloqué : résous d'abord sa demande en attente, puis relance acw clear."
	case !hasUsage:
		return label + " n'a pas la statusline d'acw (worker non claude, ou lancé par un acw plus ancien) : réinitialise-le à la main et vérifie son pane."
	}
	return ""
}

func clearWorker(repo, arg string, out io.Writer) error {
	slug := names.Slug(repo)
	label, index, err := clearLabel(arg, slug)
	if err != nil {
		return err
	}
	statusDir := names.StatusDir(repo)
	usagePath := filepath.Join(statusDir, label+".usage.json")
	name := names.Worker(slug, index)

	status, err := agentStatus(name)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(usagePath)
	if why := clearRefusal(label, status, statErr == nil); why != "" {
		return fmt.Errorf("%s", why)
	}
	if status == "working" {
		fmt.Fprintf(out, "%s travaille encore, attente de son repos…\n", label)
		if err := herdr.AgentWait(name, []string{"idle", "done", "blocked"}, clearIdleTimeout); err != nil {
			return fmt.Errorf("%s n'est pas passé au repos en %s: %w", label, clearIdleTimeout, err)
		}
		if status, err = agentStatus(name); err != nil {
			return err
		}
		if why := clearRefusal(label, status, true); why != "" {
			return fmt.Errorf("%s", why)
		}
	}

	before, err := readUsage(usagePath)
	if err != nil || before.SessionID == "" {
		return fmt.Errorf("%s : session actuelle illisible dans %s, rien n'aurait pu confirmer la réinitialisation", label, usagePath)
	}
	if err := herdr.AgentPrompt(name, "/clear"); err != nil {
		return err
	}
	for deadline := time.Now().Add(clearTimeout); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if u, err := readUsage(usagePath); err == nil && cleared(before.SessionID, u) {
			// A new task starts: the watcher's block count starts over.
			_ = os.Remove(filepath.Join(statusDir, label+".blocks"))
			fmt.Fprintf(out, "%s : contexte réinitialisé.\n", label)
			return nil
		}
	}
	return fmt.Errorf("%s : pas de nouvelle session en %s après /clear, vérifie son pane", label, clearTimeout)
}

func agentStatus(name string) (string, error) {
	agents, err := herdr.AgentList()
	if err != nil {
		return "", fmt.Errorf("herdr agent list: %w", err)
	}
	a, ok := herdr.FindAgent(agents, name)
	if !ok {
		return "", fmt.Errorf("agent %s introuvable dans herdr", name)
	}
	return a.Status, nil
}
