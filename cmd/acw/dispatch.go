package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/herdr"
)

// acw dispatch hands a worker its next task in one call: wait until it is
// idle, reset its context, type the brief. The master used to run acw
// clear then herdr agent prompt, and a forgotten clear left the previous
// task in the worker's context.

// dispatchSteps are dispatch's calls, in order, replaced in tests. branch
// is nil when no branch was asked for.
type dispatchSteps struct {
	ready  func() (clearTarget, error)
	branch func(clearTarget) error
	reset  func(clearTarget) error
	prompt func(clearTarget) error
}

// run stops at the first step that fails: a worker that could not be put
// on its branch gets neither the reset nor the brief.
func (s dispatchSteps) run() error {
	t, err := s.ready()
	if err != nil {
		return err
	}
	if s.branch != nil {
		if err := s.branch(t); err != nil {
			return err
		}
	}
	if err := s.reset(t); err != nil {
		if s.branch != nil {
			return fmt.Errorf("%w (its branch was already switched)", err)
		}
		return err
	}
	return s.prompt(t)
}

// readBrief is the brief to type, refused when it says nothing.
func readBrief(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("brief unreadable: %w", err)
	}
	text := strings.TrimSpace(string(content))
	if text == "" {
		return "", fmt.Errorf("%s is empty: nothing to dispatch", path)
	}
	return text, nil
}

func dispatchWorker(repo, arg, briefPath string, out io.Writer) error {
	text, err := readBrief(briefPath)
	if err != nil {
		return err
	}
	return dispatchSteps{
		ready: func() (clearTarget, error) { return readyToClear(repo, arg, out) },
		reset: func(t clearTarget) error { return resetContext(t, out) },
		prompt: func(t clearTarget) error {
			if err := herdr.AgentPrompt(t.name, text); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: dispatched.\n", t.label)
			return nil
		},
	}.run()
}
