package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// twoWorkerSwarm is a clone of a fresh remote with two worker worktrees on
// their waiting branches, and a git runner.
func twoWorkerSwarm(t *testing.T) (repo string, wts [2]string, git func(dir string, args ...string)) {
	t.Helper()
	git = func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=a@b", "-c", "user.name=a"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote, repo := t.TempDir(), t.TempDir()
	git(remote, "init", "-q")
	git(remote, "commit", "-q", "--allow-empty", "-m", "init")
	git(repo, "clone", "-q", remote, ".")
	for i := range wts {
		wts[i] = names.WorkerWorktree(repo, i+1, "20261008170000")
		git(repo, "worktree", "add", "-q", wts[i], "-b", names.WorkerBranch(i+1, "20261008170000"))
	}
	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, wts, git
}

func onBranch(t *testing.T, wt string) string {
	t.Helper()
	b, err := gitutil.CurrentBranch(wt)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Parking puts a worker back on its waiting branch; a branch a free worker
// still holds is taken back for the worker given it; a busy worker, or
// uncommitted changes, keep theirs.
func TestParkAndReleaseBranch(t *testing.T) {
	repo, wts, git := twoWorkerSwarm(t)
	pool := func(state1 string) poolState {
		return poolState{Plan: provisionPlan{Repo: repo, Workers: []workerSpec{{Kind: "claude"}, {Kind: "claude"}}}, Workers: []poolWorker{
			{Index: 1, Worktree: wts[0], State: state1, Task: 3}, {Index: 2, Worktree: wts[1], State: workerBusy, Task: 4}}}
	}
	setPool := func(p poolState) {
		t.Helper()
		if err := writeJSON(names.PoolFile(repo), p); err != nil {
			t.Fatal(err)
		}
	}
	home1 := names.WorkerBranch(1, "20261008170000")

	git(wts[0], "switch", "-q", "-c", "feat/x")
	var out strings.Builder
	if err := parkWorker(pool(workerBusy), pool(workerBusy).Workers[0], &out); err != nil || onBranch(t, wts[0]) != home1 {
		t.Fatalf("park = %v, on %s, said %q", err, onBranch(t, wts[0]), out.String())
	}

	// Freed while still on feat/x: worker2 is given feat/x.
	git(wts[0], "switch", "-q", "feat/x")
	setPool(pool(workerFree))
	out.Reset()
	if err := switchWorkerBranch(repo, clearTarget{index: 2, label: "worker2"}, branchRequest{Branch: "feat/x"}, &out); err != nil {
		t.Fatalf("switch = %v (%s)", err, out.String())
	}
	if onBranch(t, wts[0]) != home1 || onBranch(t, wts[1]) != "feat/x" || !strings.Contains(out.String(), "worker1: off feat/x") {
		t.Errorf("worker1 on %s, worker2 on %s, said %q", onBranch(t, wts[0]), onBranch(t, wts[1]), out.String())
	}

	// Free with uncommitted work: left alone.
	git(wts[0], "switch", "-q", "-c", "feat/y")
	if err := os.WriteFile(filepath.Join(wts[0], "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := switchWorkerBranch(repo, clearTarget{index: 2, label: "worker2"}, branchRequest{Branch: "feat/y"}, &out); err == nil || !strings.Contains(err.Error(), "uncommitted") || onBranch(t, wts[0]) != "feat/y" {
		t.Errorf("dirty holder = %v, worker1 on %s", err, onBranch(t, wts[0]))
	}
	os.Remove(filepath.Join(wts[0], "wip.txt"))

	// Busy: keeps it.
	setPool(pool(workerBusy))
	if err := switchWorkerBranch(repo, clearTarget{index: 2, label: "worker2"}, branchRequest{Branch: "feat/y"}, &out); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("busy holder = %v", err)
	}
}

// The watcher parks a free worker: the poll sees it off its waiting
// branch, parkFree moves it back; one that can't be moved is polled as
// parking on that branch, not moved again, until it leaves it.
func TestParkFree(t *testing.T) {
	repo, wts, git := twoWorkerSwarm(t)
	inbox := filepath.Join(t.TempDir(), "inbox")
	p := poolState{Plan: provisionPlan{Repo: repo, Inbox: inbox, Workers: []workerSpec{{Kind: "claude"}, {Kind: "claude"}}}, IdleCloseMinutes: 10,
		Workers: []poolWorker{{Index: 1, Worktree: wts[0], State: workerFree, Since: t0}}}
	home1 := names.WorkerBranch(1, "20261008170000")

	git(wts[0], "switch", "-q", "-c", "feat/x")
	poll := pollWorkers(p, nil, names.StatusDir(repo), t0)[1]
	if poll.Branch != "feat/x" || poll.Home != home1 || !poll.Clean || poll.Parking {
		t.Fatalf("poll = %+v", poll)
	}
	parkFree(p, p.Workers[0], "feat/x")
	if onBranch(t, wts[0]) != home1 {
		t.Errorf("parked on %s, want %s", onBranch(t, wts[0]), home1)
	}

	git(wts[0], "switch", "-q", "feat/x")
	if err := os.WriteFile(filepath.Join(wts[0], "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	parkFree(p, p.Workers[0], "feat/x")
	if poll := pollWorkers(p, nil, names.StatusDir(repo), t0)[1]; !poll.Parking || onBranch(t, wts[0]) != "feat/x" {
		t.Errorf("after a failed park: poll = %+v, on %s", poll, onBranch(t, wts[0]))
	}
	if told, _ := os.ReadFile(inbox); !strings.Contains(string(told), "worker1 is free but stays on feat/x") {
		t.Errorf("master told %q", told)
	}
	os.Remove(filepath.Join(wts[0], "wip.txt"))
	git(wts[0], "switch", "-q", "-c", "feat/y")
	if poll := pollWorkers(p, nil, names.StatusDir(repo), t0)[1]; poll.Parking {
		t.Errorf("on another branch: poll = %+v, want it moved again", poll)
	}
	parkFree(p, p.Workers[0], "feat/y")
}

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
		{false, true, false, branchStep{create: true}}, // no stack: plain git
		{false, false, true, branchStep{}},             // no stack, no wtm: plain git
	} {
		if got, err := chooseBranchStep(c.stacked, c.switchOK, c.exists); err != nil || got != c.want {
			t.Errorf("chooseBranchStep(%v, %v, %v) = %+v, %v; want %+v", c.stacked, c.switchOK, c.exists, got, err, c.want)
		}
	}
	// A plain git switch would leave the stack under the old branch.
	if _, err := chooseBranchStep(true, false, true); err == nil || !strings.Contains(err.Error(), "wtm 0.26") {
		t.Errorf("chooseBranchStep(stacked, no switch) = %v, want a refusal naming wtm 0.26", err)
	}
}
