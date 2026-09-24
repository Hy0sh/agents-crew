package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/config"
)

func TestValidateAgentDir(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	outside := filepath.Join(root, "studio")
	for _, d := range []string{filepath.Join(repo, "apps"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link-into-repo")
	if err := os.Symlink(filepath.Join(repo, "apps"), link); err != nil {
		t.Fatal(err)
	}

	if got, err := validateAgentDir(repo, outside); err != nil || got != outside {
		t.Errorf("validateAgentDir(outside) = %q, %v; want it accepted", got, err)
	}
	for name, dir := range map[string]string{
		"relative":        "studio",
		"missing":         filepath.Join(root, "nope"),
		"file":            file,
		"the repo":        repo,
		"inside the repo": filepath.Join(repo, "apps"),
		"symlink inside":  link,
	} {
		if _, err := validateAgentDir(repo, dir); err == nil {
			t.Errorf("validateAgentDir(%s: %q) = nil error, want a refusal", name, dir)
		}
	}
}

func TestResolveWorkersOutsideDir(t *testing.T) {
	root := t.TempDir()
	repo, outside := filepath.Join(root, "repo"), filepath.Join(root, "docs")
	for _, d := range []string{repo, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	opts := &startOptions{workers: 2, workerKind: "claude", overrides: map[string]config.WorkerOverride{"1": {Dir: &outside}}}
	workers, err := resolveWorkers(opts, repo)
	if err != nil {
		t.Fatal(err)
	}
	if workers[0].Dir != outside || workers[1].Dir != "" {
		t.Errorf("dirs = %q, %q; want worker 1 outside, worker 2 a coder", workers[0].Dir, workers[1].Dir)
	}

	inside := repo
	opts.overrides = map[string]config.WorkerOverride{"2": {Dir: &inside}}
	if _, err := resolveWorkers(opts, repo); err == nil || !strings.Contains(err.Error(), "2") {
		t.Errorf("resolveWorkers() with dir = the repo: %v; want a refusal naming worker 2", err)
	}
}

// Environments go to the workers that code, in order: one outside the
// code must not take a slot a coder needs.
func TestStackedWorkersSkipsOutsideWorkers(t *testing.T) {
	workers := []workerSpec{{Dir: "/studio"}, {}, {}}
	if got := stackedWorkers(workers, 2); !slices.Equal(got, []bool{false, true, true}) {
		t.Errorf("stackedWorkers() = %v, want [false true true]", got)
	}
	if got := stackedWorkers(workers, 1); !slices.Equal(got, []bool{false, true, false}) {
		t.Errorf("stackedWorkers(max 1) = %v, want [false true false]", got)
	}
}

func TestCoderCount(t *testing.T) {
	if got := coderCount([]workerSpec{{Dir: "/studio"}, {}, {}}); got != 2 {
		t.Errorf("coderCount() = %d, want 2", got)
	}
}

func TestExplainStartNamesTheTrustPrompt(t *testing.T) {
	blocked := errors.New(`herdr [agent start]: {"error":{"code":"agent_not_ready","message":"agent x is blocked during startup and is not ready for prompts"}}`)
	got := explainStart(blocked, "/Users/me/studio")
	if !strings.Contains(got.Error(), "/Users/me/studio") || !strings.Contains(got.Error(), "confiance") {
		t.Errorf("explainStart() = %v; want the trust prompt and the folder named", got)
	}
	if !errors.Is(got, blocked) {
		t.Error("explainStart() must wrap the original error")
	}

	other := errors.New("agent_pane_busy")
	if got := explainStart(other, "/x"); got != other {
		t.Errorf("explainStart(other) = %v; want it unchanged", got)
	}
}
