package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/names"
)

// fakeSwarm is a repo with one worker worktree on its own branch, a status
// dir and run.json, and a fake wtm first on PATH that records its calls.
// It returns the repo and the file the calls land in.
func fakeSwarm(t *testing.T, profile string) (repo, calls string) {
	t.Helper()
	repo = t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(repo, "init", "-q")
	git(repo, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init")
	git(repo, "worktree", "add", "-q", names.WorkerWorktree(repo, 1, "20260925140000"), "-b", names.WorkerBranch(1, "20260925140000"))
	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeRunInfo(repo, runInfo{Profile: profile}); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	calls = filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + shellWord(calls) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "wtm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return repo, calls
}

func TestPauseStopsEachWorkerStackAndTellsTheMaster(t *testing.T) {
	repo, calls := fakeSwarm(t, "light")
	if err := pauseStacks(repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); string(got) != "stop agents/worker1-20260925140000\n" {
		t.Errorf("wtm calls = %q", got)
	}
	if inbox, _ := os.ReadFile(names.Inbox(repo)); !strings.Contains(string(inbox), "acw pause") {
		t.Errorf("inbox = %q, the master should hear of the pause", inbox)
	}
}

// resume starts the stacks on the profile the swarm was launched with.
func TestResumeStartsEachWorkerStackOnTheRunProfile(t *testing.T) {
	repo, calls := fakeSwarm(t, "light")
	if err := resumeStacks(repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); string(got) != "start agents/worker1-20260925140000 --profile light\n" {
		t.Errorf("wtm calls = %q", got)
	}
	if inbox, _ := os.ReadFile(names.Inbox(repo)); !strings.Contains(string(inbox), "acw resume") {
		t.Errorf("inbox = %q, the master should hear of the resume", inbox)
	}
}

func TestPauseWithoutASwarmRefuses(t *testing.T) {
	err := pauseStacks(t.TempDir(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "aucun swarm acw") {
		t.Errorf("pauseStacks(no swarm) = %v, want a refusal naming it", err)
	}
}
