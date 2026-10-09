package main

import (
	"errors"
	"os"
	"path/filepath"
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
	one := 1
	opts := &startOptions{workerKind: "claude", roles: config.Roles{
		{Name: "analyst", Role: config.Role{Max: &one, Dir: &outside}},
		{Name: "worker", Role: config.Role{Max: &one}},
	}}
	workers, err := buildSlots(opts, repo)
	if err != nil {
		t.Fatal(err)
	}
	if workers[0].Dir != outside || workers[1].Dir != "" {
		t.Errorf("dirs = %q, %q; want analyst1 outside, worker1 a coder", workers[0].Dir, workers[1].Dir)
	}

	inside := repo
	opts.roles = config.Roles{{Name: "analyst", Role: config.Role{Max: &one, Dir: &inside}}}
	if _, err := buildSlots(opts, repo); err == nil || !strings.Contains(err.Error(), "analyst") {
		t.Errorf("buildSlots() with dir = the repo: %v; want a refusal naming the role", err)
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
	if !strings.Contains(got.Error(), "/Users/me/studio") || !strings.Contains(got.Error(), "trust prompt") {
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
