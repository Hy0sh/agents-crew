package gitutil

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// A worker coding for 35 minutes without touching its status file is
// still working: its edits are what say so.
func TestLastActivityFollowsEditedFiles(t *testing.T) {
	dir := gitInit(t)
	later := time.Now().Add(time.Hour).Truncate(time.Second)
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if got := LastActivity(dir); !got.Equal(later) {
		t.Errorf("LastActivity() = %v, want the edited file's %v", got, later)
	}
}

// A tracked file modified in the work tree is listed as " M path", with a
// leading space that must not be trimmed away with the output's.
func TestLastActivityFollowsAModifiedTrackedFile(t *testing.T) {
	dir := gitInit(t)
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.go"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "-m", "a"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	later := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := os.WriteFile(file, []byte("package a // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if got := LastActivity(dir); !got.Equal(later) {
		t.Errorf("LastActivity() = %v, want the modified file's %v", got, later)
	}
}

func TestLastActivityCountsTheIndex(t *testing.T) {
	dir := gitInit(t)
	if got := LastActivity(dir); got.IsZero() {
		t.Error("LastActivity() of a clean repo = zero, want its index's mtime")
	}
}

func TestLastActivityOutsideGitIsZero(t *testing.T) {
	if got := LastActivity(t.TempDir()); !got.IsZero() {
		t.Errorf("LastActivity(not a repo) = %v, want zero", got)
	}
}

// Polled every few seconds next to a worker that commits: it must never
// take the index lock, which a plain git status does to refresh the index
// after a touched file (and the worker's own git add then fails on
// index.lock).
func TestLastActivityLeavesTheIndexAlone(t *testing.T) {
	dir := gitInit(t)
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.go"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "-m", "a"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	index := filepath.Join(dir, ".git", "index")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(index, old, old); err != nil {
		t.Fatal(err)
	}
	// Same content, new mtime: git status would refresh the index for it.
	later := time.Now().Truncate(time.Second)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	LastActivity(dir)
	if info, err := os.Stat(index); err != nil || !info.ModTime().Equal(old) {
		t.Errorf("index mtime = %v, want it untouched at %v", info.ModTime(), old)
	}
}

func TestOriginURL(t *testing.T) {
	dir := gitInit(t)
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:some-org/some-repo.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	if got, err := OriginURL(dir); err != nil || got != "git@github.com:some-org/some-repo.git" {
		t.Errorf("OriginURL() = %q, %v", got, err)
	}
}

func TestParseWorktreeBranches(t *testing.T) {
	porcelain := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\n" +
		"worktree /repo/.claude/worktrees/worker1-1\nHEAD def\nbranch refs/heads/feat/x\n\n" +
		"worktree /repo/.claude/worktrees/worker2-1\nHEAD 123\ndetached\n"
	got := parseWorktreeBranches(porcelain)
	want := map[string]string{"/repo": "main", "/repo/.claude/worktrees/worker1-1": "feat/x", "/repo/.claude/worktrees/worker2-1": ""}
	if !maps.Equal(got, want) {
		t.Errorf("parseWorktreeBranches() = %v, want %v", got, want)
	}
}

func TestSwitchAndHasBranch(t *testing.T) {
	dir := gitInit(t)
	base, err := CurrentBranch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if HasBranch(dir, "feat/x") {
		t.Fatal("HasBranch(feat/x) before it exists")
	}
	if err := Switch(dir, "feat/x", base, true); err != nil {
		t.Fatalf("Switch(create) = %v", err)
	}
	if got, _ := CurrentBranch(dir); got != "feat/x" || !HasBranch(dir, "feat/x") {
		t.Errorf("after Switch(create): on %q, HasBranch = %v", got, HasBranch(dir, "feat/x"))
	}
	if err := Switch(dir, base, "", false); err != nil {
		t.Fatalf("Switch(existing) = %v", err)
	}
	if got, _ := CurrentBranch(dir); got != base {
		t.Errorf("after Switch(existing): on %q, want %q", got, base)
	}
}

// A WIP commit left on a closed worker's branch is what the master must
// hear about; once pushed, there is nothing left to say.
func TestUnpushed(t *testing.T) {
	remote := gitInit(t)
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=a@b", "-c", "user.name=a"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("clone", "-q", remote, ".")
	git("switch", "-q", "-c", "feat/x")
	git("commit", "-q", "--allow-empty", "-m", "wip")
	if n, err := Unpushed(dir, "feat/x"); err != nil || n != 1 {
		t.Fatalf("Unpushed() = %d, %v; want 1", n, err)
	}
	git("push", "-q", "origin", "feat/x")
	if n, err := Unpushed(dir, "feat/x"); err != nil || n != 0 {
		t.Errorf("Unpushed() after push = %d, %v; want 0", n, err)
	}
}
