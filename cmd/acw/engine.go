package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hy0sh/agents-crew/internal/claudecode"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// engine is how the workers run: what acw does to start one, list them,
// hand one a prompt, reset it between tasks, interrupt it, read its
// screen, wait for it. herdr runs them in panes; claude-bg as Claude Code
// background sessions, the engine a master in Claude Desktop implies.
type engine interface {
	// start runs worker name (label in the pool) in cwd, with args.
	start(repo string, plan provisionPlan, pw poolWorker, name, label, kind, cwd string, args []string) (step string, err error)
	agents() ([]herdr.Agent, error)
	prompt(statusDir, label, name, text string) error
	reset(t clearTarget, out io.Writer) error
	// interrupt stops name's turn; the message waiting in its .tell file
	// reaches it at once.
	interrupt(statusDir, label, name string) error
	screen(name string) (string, error)
	// terminal is name's screen as the user would see it, with its
	// colours, for the board to render.
	terminal(name string) (string, error)
	// read is the last lines of name's pane, for the master to see what
	// it waits on.
	read(name string, lines int) (string, error)
	wait(name string, until []string, timeout time.Duration) error
	// stop ends worker w, run as name, for good.
	stop(w poolWorker, name string) error
}

// Engine names, in the pool plan.
const (
	herdrEngine = ""
	bgEngine    = "claude-bg"
)

// engineOf is the engine of a swarm, as its pool plan names it.
func engineOf(plan provisionPlan) engine {
	if plan.Engine == bgEngine {
		return bgSessions{}
	}
	return panes{}
}

// engineFor is the engine of repo's swarm, herdr when no pool says.
func engineFor(repo string) engine {
	p, _, err := readPool(repo)
	if err != nil {
		return panes{}
	}
	return engineOf(p.Plan)
}

// panes runs the workers in herdr panes, next to the master's.
type panes struct{}

func (panes) start(repo string, plan provisionPlan, pw poolWorker, name, label, kind, cwd string, args []string) (string, error) {
	pane, step, err := placeWorker(repo, plan.MasterPane, pw, cwd)
	if err != nil {
		if pane != "" {
			_ = herdr.PaneClose(pane)
		}
		return step, err
	}
	if err := herdr.PaneRename(pane, label); err != nil {
		fmt.Fprintf(os.Stderr, "%s: herdr pane rename: %v\n", name, err)
	}
	if err := herdr.AgentStart(name, kind, pane, args...); err != nil {
		// Read before the close: the screen is the only trace of what
		// held the agent, which acw only guesses at.
		screen, _ := herdr.PaneRead(pane, 15)
		_ = herdr.PaneClose(pane)
		err = explainStart(err, cwd)
		if screen = strings.TrimSpace(screen); screen != "" {
			err = fmt.Errorf("%w. Its screen:\n%s", err, screen)
		}
		return "herdr agent start", err
	}
	return "", nil
}

func (panes) agents() ([]herdr.Agent, error) { return herdr.AgentList() }

func (panes) prompt(_, _, name, text string) error { return herdr.AgentPrompt(name, text) }

func (panes) reset(t clearTarget, out io.Writer) error { return resetContext(t, out) }

func (panes) interrupt(_, _, name string) error { return pressKeys(name, "esc") }

func (panes) screen(name string) (string, error) { return herdr.AgentScreen(name) }

func (panes) terminal(name string) (string, error) { return herdr.AgentScreen(name) }

func (panes) read(name string, lines int) (string, error) { return herdr.AgentRead(name, lines) }

func (panes) wait(name string, until []string, timeout time.Duration) error {
	return herdr.AgentWait(name, until, timeout)
}

func (panes) stop(w poolWorker, _ string) error {
	if w.Pane == "" {
		return nil
	}
	return herdr.PaneClose(w.Pane)
}

// bgSessions runs the workers as Claude Code background sessions. A
// running session takes nothing typed from outside, so a task starts a
// new session (that is its reset), and a message stops the session and
// resumes it with the message (see claudecode).
type bgSessions struct{}

// stopWait is how long a stopped session may take to leave the list.
const stopWait = 30 * time.Second

func argsFile(statusDir, label string) string { return filepath.Join(statusDir, label+".args.json") }

// freshFile marks a worker whose next prompt starts a new session.
func freshFile(statusDir, label string) string { return filepath.Join(statusDir, label+".fresh") }

func (bgSessions) start(repo string, _ provisionPlan, _ poolWorker, name, label, _, cwd string, args []string) (string, error) {
	// Kept for the session each task starts with the same options.
	content, err := json.Marshal(args)
	if err != nil {
		return "saving its options", err
	}
	statusDir := names.StatusDir(repo)
	if err := os.WriteFile(argsFile(statusDir, label), content, 0o600); err != nil {
		return "saving its options", err
	}
	if err := os.WriteFile(freshFile(statusDir, label), nil, 0o600); err != nil {
		return "saving its options", err
	}
	if err := claudecode.StartBackground(cwd, name, args, ""); err != nil {
		return "claude --bg", err
	}
	return "", nil
}

func (bgSessions) agents() ([]herdr.Agent, error) {
	sessions, err := claudecode.Sessions()
	if err != nil {
		return nil, err
	}
	agents := make([]herdr.Agent, len(sessions))
	for i, s := range sessions {
		agents[i] = herdr.Agent{Name: s.Name, Status: s.AgentStatus(), Cwd: s.Cwd}
	}
	return agents, nil
}

// prompt hands name text: the first prompt of a new session after a
// reset, else the running session stopped and resumed with it.
func (bgSessions) prompt(statusDir, label, name, text string) error {
	sessions, err := claudecode.Sessions()
	if err != nil {
		return err
	}
	s, ok := claudecode.FindSession(sessions, name)
	if !ok {
		return fmt.Errorf("%s has no running session", name)
	}
	if err := claudecode.StopSession(s, stopWait); err != nil {
		return err
	}
	if _, err := os.Stat(freshFile(statusDir, label)); err == nil {
		var args []string
		content, err := os.ReadFile(argsFile(statusDir, label))
		if err == nil {
			err = json.Unmarshal(content, &args)
		}
		if err != nil {
			return fmt.Errorf("%s: its options: %w", name, err)
		}
		if err := claudecode.StartBackground(s.Cwd, name, args, text); err != nil {
			return err
		}
		return os.Remove(freshFile(statusDir, label))
	}
	return claudecode.ResumeWith(s, text)
}

// reset marks the next prompt as a new session's: the brief that follows
// starts it, with nothing of the previous task.
func (bgSessions) reset(t clearTarget, out io.Writer) error {
	if err := os.WriteFile(freshFile(t.statusDir, t.label), nil, 0o600); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(t.statusDir, t.label+".blocks"))
	fmt.Fprintf(out, "%s: its next task starts a new session.\n", t.label)
	return nil
}

func (b bgSessions) interrupt(statusDir, label, name string) error {
	var held bytes.Buffer
	if err := drainOnce(filepath.Join(statusDir, label+".tell"), &held); err != nil {
		return err
	}
	if held.Len() == 0 {
		return errors.New("no message waiting for it")
	}
	if err := b.prompt(statusDir, label, name, masterMessage(held.String())); err != nil {
		return errors.Join(err, tellWorker(statusDir, label, held.String()))
	}
	markTold(statusDir, label)
	return nil
}

// screen: a background session has no screen to read; what acw read on
// one (a pending prompt, the last lines) it does without.
func (bgSessions) screen(string) (string, error) { return "", nil }

func (bgSessions) terminal(name string) (string, error) {
	sessions, err := claudecode.Sessions()
	if err != nil {
		return "", err
	}
	s, ok := claudecode.FindSession(sessions, name)
	if !ok {
		return "", fmt.Errorf("no background session named %s", name)
	}
	return claudecode.Logs(s)
}

func (bgSessions) read(name string, _ int) (string, error) {
	return "(a background session, no pane to read: the user answers it with `claude attach " + name + "` in a terminal)", nil
}

func (bgSessions) stop(_ poolWorker, name string) error {
	sessions, err := claudecode.Sessions()
	if err != nil {
		return err
	}
	s, ok := claudecode.FindSession(sessions, name)
	if !ok {
		return nil
	}
	return claudecode.StopSession(s, stopWait)
}

func (b bgSessions) wait(name string, until []string, timeout time.Duration) error {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Second) {
		agents, err := b.agents()
		if err != nil {
			return err
		}
		if a, ok := herdr.FindAgent(agents, name); ok {
			for _, s := range until {
				if a.Status == s {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("%s not %v within %s", name, until, timeout)
}
