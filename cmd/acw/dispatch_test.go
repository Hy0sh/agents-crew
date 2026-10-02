package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDispatchRefusesAnEmptyBrief(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(path, []byte(" \n\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readBrief(path); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("readBrief(blank) = %v, want an empty-brief refusal", err)
	}
	if _, err := readBrief(filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Error("readBrief(missing) = nil error")
	}
}

// recorder builds dispatch steps that log their calls, failing at the one
// named fail.
func recorder(calls *[]string, fail string, withBranch bool) dispatchSteps {
	step := func(name string) func(clearTarget) error {
		return func(clearTarget) error {
			*calls = append(*calls, name)
			if name == fail {
				return errors.New(name + " failed")
			}
			return nil
		}
	}
	s := dispatchSteps{
		ready: func() (clearTarget, error) {
			*calls = append(*calls, "ready")
			if fail == "ready" {
				return clearTarget{}, errors.New("ready failed")
			}
			return clearTarget{label: "worker1"}, nil
		},
		reset:  step("reset"),
		prompt: step("prompt"),
	}
	if withBranch {
		s.branch = step("branch")
	}
	return s
}

func TestDispatchRunsTheStepsInOrder(t *testing.T) {
	var calls []string
	if err := recorder(&calls, "", true).run(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ready", "branch", "reset", "prompt"}; !slices.Equal(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	calls = nil
	if err := recorder(&calls, "", false).run(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ready", "reset", "prompt"}; !slices.Equal(calls, want) {
		t.Errorf("without --branch: calls = %v, want %v", calls, want)
	}
}

func TestDispatchStopsAtTheFirstFailure(t *testing.T) {
	for fail, want := range map[string][]string{
		"ready":  {"ready"},
		"branch": {"ready", "branch"},
		"reset":  {"ready", "branch", "reset"},
	} {
		var calls []string
		err := recorder(&calls, fail, true).run()
		if err == nil || !slices.Equal(calls, want) {
			t.Errorf("fail at %s: calls = %v, err = %v; want %v and an error", fail, calls, err, want)
		}
	}
	// The branch already moved when the reset fails: the error says so.
	var calls []string
	if err := recorder(&calls, "reset", true).run(); err == nil || !strings.Contains(err.Error(), "branch") {
		t.Errorf("reset failure after a branch switch = %v, want it to mention the branch", err)
	}
}

func TestBranchHolder(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "worker1")
	other := filepath.Join(dir, "worker2")
	for _, d := range []string{self, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	worktrees := map[string]string{"/repo": "main", self: "feat/x", other: "feat/y"}
	if got := branchHolder(worktrees, "feat/y", self); got != other {
		t.Errorf("branchHolder(feat/y) = %q, want %q", got, other)
	}
	if got := branchHolder(worktrees, "main", self); got != "/repo" {
		t.Errorf("branchHolder(main) = %q, want the main checkout", got)
	}
	// Already on it, even named through a symlink: not held by another.
	if got := branchHolder(worktrees, "feat/x", filepath.Join(link, "worker1")); got != "" {
		t.Errorf("branchHolder(own branch via a symlink) = %q, want none", got)
	}
	if got := branchHolder(worktrees, "feat/z", self); got != "" {
		t.Errorf("branchHolder(free branch) = %q, want none", got)
	}
}

func TestChooseBranchStep(t *testing.T) {
	for _, c := range []struct {
		stacked, switchOK, exists bool
		want                      branchStep
	}{
		{true, true, true, branchStep{wtm: true}},
		{true, true, false, branchStep{wtm: true, create: true}},
		{true, false, true, branchStep{}},              // wtm older than switch: plain git
		{false, true, false, branchStep{create: true}}, // no stack: plain git
	} {
		if got := chooseBranchStep(c.stacked, c.switchOK, c.exists); got != c.want {
			t.Errorf("chooseBranchStep(%v, %v, %v) = %+v, want %+v", c.stacked, c.switchOK, c.exists, got, c.want)
		}
	}
}
