package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// acw tell is how the master speaks to a worker in the middle of a task:
// a decision to relay, a go on a plan. It used to type into the worker's
// pane with herdr agent prompt, under a herdr name it had to look up, and
// the text got lost on a worker held by an approval popup, or mixed with
// what the user was typing there. The message now waits in workerN.tell,
// and goes out at the worker's next end of turn (see turnEnd), or through
// the watcher when the worker is already idle (see tellIdle).

// tellWorker leaves text for the worker labelled label.
func tellWorker(statusDir, label, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("nothing to tell: give the message as arguments or on stdin")
	}
	return appendLine(filepath.Join(statusDir, label+".tell"), text+"\n")
}

// openWorkerNamed is the open worker arg names in repo's swarm.
func openWorkerNamed(repo, arg string) (*poolWorker, error) {
	index, label, err := workerName(repo, arg)
	if err != nil {
		return nil, err
	}
	pool, _, err := readPool(repo)
	if err != nil {
		return nil, err
	}
	w := pool.worker(index)
	if w == nil {
		return nil, fmt.Errorf("%s is not open", label)
	}
	return w, nil
}

// tellByHand is acw tell: the worker must be open, the message is its
// arguments or, without any, stdin (a heredoc expands nothing).
// With interrupt, Escape then stops its current turn: the worker goes idle
// and acw types the message at once instead of at the end of a turn that
// may not come (a worker stuck on a loop or a hung tool).
func tellByHand(repo, arg string, words []string, interrupt bool, stdin io.Reader, out io.Writer) error {
	w, err := openWorkerNamed(repo, arg)
	if err != nil {
		return err
	}
	text := strings.Join(words, " ")
	if len(words) == 0 {
		content, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		text = string(content)
	}
	if err := tellWorker(names.StatusDir(repo), w.label(), text); err != nil {
		return err
	}
	if interrupt {
		if err := engineFor(repo).interrupt(names.StatusDir(repo), w.label(), names.Agent(names.Slug(repo), w.label())); err != nil {
			return fmt.Errorf("the message waits for its end of turn, but the interruption failed: %w", err)
		}
		fmt.Fprintf(out, "%s: current turn interrupted, it gets the message within seconds.\n", w.label())
		return nil
	}
	fmt.Fprintf(out, "%s gets it at the end of its current turn, or within seconds if it is idle.\n", w.label())
	return nil
}

// pressKeys is herdr's send-keys, a variable for the tests.
var pressKeys = herdr.AgentSendKeys

// slashCommand checks what acw tell --command types: one line starting
// with /. /clear is acw clear's, which confirms the reset took.
func slashCommand(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case !strings.HasPrefix(s, "/") || strings.Contains(s, "\n"):
		return "", fmt.Errorf("%q is not a slash command: one line starting with /", s)
	case s == "/clear" || strings.HasPrefix(s, "/clear "):
		return "", errors.New("use acw clear, which checks that the reset took")
	}
	return s, nil
}

// commandWorker types a slash command into a worker's input, which acw
// tell's messages can't carry: they reach the worker as text, at its end
// of turn. It waits for the worker to be idle, and types nothing over a
// prompt or over what stands on its input line.
func commandWorker(repo, arg, command string, out io.Writer) error {
	command, err := slashCommand(command)
	if err != nil {
		return err
	}
	w, err := openWorkerNamed(repo, arg)
	if err != nil {
		return err
	}
	eng := engineFor(repo)
	if _, bg := eng.(bgSessions); bg {
		return fmt.Errorf("%s runs as a background session, which takes no command typed into it; each task starts a new session, plugins reloaded", w.label())
	}
	name := names.Agent(names.Slug(repo), w.label())
	status, err := agentStatus(eng, name)
	if err != nil {
		return err
	}
	if status == "working" {
		fmt.Fprintf(out, "%s is still working, waiting for it to go idle…\n", w.label())
		if err := eng.wait(name, []string{"idle", "done", "blocked"}, clearIdleTimeout); err != nil {
			return fmt.Errorf("%s didn't go idle within %s: %w", w.label(), clearIdleTimeout, err)
		}
		if status, err = agentStatus(eng, name); err != nil {
			return err
		}
	}
	if status == "blocked" {
		return fmt.Errorf("%s is blocked: resolve its pending request first, then run the command again", w.label())
	}
	screen, err := eng.screen(name)
	if err != nil {
		return err
	}
	if typed := typedInput(screen); typed != "" {
		return fmt.Errorf("%s's input line holds “%s”: nothing typed; it goes once that line is empty", w.label(), oneLine(typed))
	}
	if err := eng.prompt(names.StatusDir(repo), w.label(), name, command); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %s typed.\n", w.label(), command)
	return nil
}

// tellIdle hands an idle worker the messages waiting for it, typed into
// its input: no end of turn will come to carry them. Never over something
// on its input line, the user's half-typed text or a choice on screen:
// then it types nothing and returns that line, for the master to hear.
func tellIdle(eng engine, statusDir, label, agent string) (blocking string, err error) {
	path := filepath.Join(statusDir, label+".tell")
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return "", nil
	}
	screen, err := eng.screen(agent)
	if err != nil {
		return "", err
	}
	if typed := typedInput(screen); typed != "" {
		return typed, nil
	}
	var held bytes.Buffer
	if err := drainOnce(path, &held); err != nil || held.Len() == 0 {
		return "", err
	}
	if err := eng.prompt(statusDir, label, agent, masterMessage(held.String())); err != nil {
		// Back where it was, for the next poll.
		return "", errors.Join(err, tellWorker(statusDir, label, held.String()))
	}
	markTold(statusDir, label)
	return "", nil
}

var (
	ansiCode = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	// dimmed is a span in faint style: the placeholder Claude Code shows
	// in an empty input ("Try ...").
	dimmed = regexp.MustCompile(`\x1b\[2m[^\x1b]*`)
)

// typedInput is what stands on the input line of an agent's screen, read
// with its ANSI styling, "" when it is empty. The input line is the last
// one that starts with ❯: an empty one shows a dimmed placeholder, a
// choice prompt puts its ❯ on the selected option, plain text.
// ponytail: matches Claude Code's rendering as of 2026-10; another agent,
// or a new rendering, reads as empty and gets the message typed.
func typedInput(screen string) string {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		plain := strings.TrimLeft(ansiCode.ReplaceAllString(lines[i], ""), " │")
		if !strings.HasPrefix(plain, "❯") {
			continue
		}
		_, after, _ := strings.Cut(lines[i], "❯")
		return strings.TrimSpace(ansiCode.ReplaceAllString(dimmed.ReplaceAllString(after, ""), ""))
	}
	return ""
}
