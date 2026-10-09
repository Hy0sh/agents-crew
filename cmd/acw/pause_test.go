package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/teardown"
)

// fakeSwarm is a repo with one worker worktree on its own branch, a status
// dir and its pool, and a fake wtm first on PATH that records its calls.
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
	if err := writeJSON(names.PoolFile(repo), poolState{Plan: provisionPlan{Repo: repo, Profile: profile, Inbox: names.Inbox(repo)}}); err != nil {
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

// resume starts the stacks on the profile the swarm was launched with,
// except a worker's on its waiting branch: it has no task, and no stack
// there (see parkWorker).
func TestResumeStartsEachWorkerStackOnTheRunProfile(t *testing.T) {
	repo, calls := fakeSwarm(t, "light")
	wt := names.WorkerWorktree(repo, 1, "20260925140000")
	if out, err := exec.Command("git", "-C", wt, "switch", "-q", "-c", "feat/x").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if err := resumeStacks(repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); string(got) != "start feat/x --profile light\n" {
		t.Errorf("wtm calls = %q", got)
	}
	if inbox, _ := os.ReadFile(names.Inbox(repo)); !strings.Contains(string(inbox), "acw resume") {
		t.Errorf("inbox = %q, the master should hear of the resume", inbox)
	}

	os.Remove(calls)
	if out, err := exec.Command("git", "-C", wt, "switch", "-q", names.WorkerBranch(1, "20260925140000")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var out bytes.Buffer
	if err := resumeStacks(repo, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); len(got) != 0 || !strings.Contains(out.String(), "left without a stack") {
		t.Errorf("on its waiting branch: wtm calls = %q, said %q", got, out.String())
	}
}

// A free worker with a stack goes back to its waiting branch without
// one: wtm switch --no-start.
func TestParkWorkerStartsNoStack(t *testing.T) {
	repo, calls := fakeSwarm(t, "light")
	wt := names.WorkerWorktree(repo, 1, "20260925140000")
	if out, err := exec.Command("git", "-C", wt, "switch", "-q", "-c", "feat/x").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	p, _, _ := readPool(repo)
	if err := parkWorker(p, poolWorker{Index: 1, Worktree: wt, Stacked: true, State: workerFree}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); !strings.Contains(string(got), "switch agents/worker1-20260925140000 --no-start\n") {
		t.Errorf("wtm calls = %q", got)
	}
}

// A worker beyond max-stacks, or whose adopt failed, has a worktree but no
// stack: wtm says so, and that is not a failure of pause.
func TestPauseSkipsAWorktreeWithoutAStack(t *testing.T) {
	repo, calls := fakeSwarm(t, "")
	script := "#!/bin/sh\necho \"$@\" >> " + shellWord(calls) + "\necho 'Error: no worktree for branch \"'$2'\" (no linked worktree)' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(calls), "wtm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := pauseStacks(repo, &out); err != nil {
		t.Errorf("pauseStacks() = %v, a worktree with no stack is skipped, not failed", err)
	}
	if !strings.Contains(out.String(), "no stack") {
		t.Errorf("output = %q, want the skip said", out.String())
	}
}

// The same answer for a worktree acw got a stack: the stack is up under
// another branch, so pause says so and fails.
func TestPauseFailsOnAStrandedStack(t *testing.T) {
	repo, calls := fakeSwarm(t, "")
	script := "#!/bin/sh\necho 'Error: no worktree for branch \"'$2'\"' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(calls), "wtm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := teardown.MarkStacked(names.WorkerWorktree(repo, 1, "20260925140000")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := pauseStacks(repo, &out); err == nil {
		t.Error("pauseStacks() = nil, want a failure for a stack out of reach")
	}
	if !strings.Contains(out.String(), "wtm list") {
		t.Errorf("output = %q, want the repair named", out.String())
	}
}

func TestPauseWithoutASwarmRefuses(t *testing.T) {
	err := pauseStacks(t.TempDir(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no acw swarm") {
		t.Errorf("pauseStacks(no swarm) = %v, want a refusal naming it", err)
	}
}

// A kept worktree's stack takes room until wtm lists the worktree with
// none (a wtm remove by hand): then it stops counting, for good.
func TestHeldStacksForgetsAStackWtmNoLongerHas(t *testing.T) {
	repo, calls := fakeSwarm(t, "")
	dir := names.WorkerWorktree(repo, 1, "20260925140000")
	if err := teardown.MarkStacked(dir); err != nil {
		t.Fatal(err)
	}
	list := func(status string) {
		t.Helper()
		script := "#!/bin/sh\necho \"$@\" >> " + shellWord(calls) + "\necho '5  agents/worker1  -  " + status + "  " + realPath(dir) + "'\n"
		if err := os.WriteFile(filepath.Join(filepath.Dir(calls), "wtm"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	list("down")
	if n := heldStacks(repo, poolState{}); n != 1 {
		t.Errorf("heldStacks(stack still listed) = %d, want 1", n)
	}
	list("adoptable")
	if n := heldStacks(repo, poolState{}); n != 0 {
		t.Errorf("heldStacks(stack gone) = %d, want 0", n)
	}
	if teardown.Stacked(dir) {
		t.Error("the mark of a stack gone is still there")
	}
	_ = os.Remove(calls)
	if n := heldStacks(repo, poolState{}); n != 0 {
		t.Errorf("heldStacks(after) = %d, want 0", n)
	}
	if got, _ := os.ReadFile(calls); len(got) != 0 {
		t.Errorf("wtm calls = %q, want none once no worktree is marked", got)
	}
}
