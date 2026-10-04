package teardown

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/names"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=a@b", "-c", "user.name=a"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A worker takes each task on a new branch in its worktree: acw stop must
// not force-delete that branch with the worktree, pushed or not. Only the
// branch acw cut for the worker goes.
func TestCleanupKeepsTheTaskBranch(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := names.WorkerWorktree(repo, 1, "20261002")
	git(t, repo, "worktree", "add", "-q", "-b", names.WorkerBranch(1, "20261002"), wt)
	git(t, wt, "switch", "-q", "-c", "feat/task")
	git(t, wt, "commit", "-q", "--allow-empty", "-m", "work not pushed")

	cleanupWorkerWorktrees(repo, func(string) bool { return false })

	if _, err := exec.Command("test", "-e", filepath.Join(wt, ".git")).CombinedOutput(); err == nil {
		t.Error("the worktree should be removed")
	}
	if got := git(t, repo, "log", "-1", "--format=%s", "feat/task"); got != "work not pushed" {
		t.Errorf("feat/task head = %q, want the task's commit kept", got)
	}
	if got := git(t, repo, "branch", "--list", names.WorkerBranch(1, "20261002")); got != "" {
		t.Errorf("acw's own branch is still there: %q", got)
	}
}

// A worktree wtm gave a stack to, moved to another branch without wtm
// switch: wtm finds no stack under the current branch. Removing the
// worktree would leave that stack running out of anyone's reach.
func TestWorktreeKeepsAStrandedStack(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := names.WorkerWorktree(repo, 1, "20261002")
	git(t, repo, "worktree", "add", "-q", "-b", names.WorkerBranch(1, "20261002"), wt)
	git(t, wt, "switch", "-q", "-c", "feat/task")
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'Error: no worktree for branch \"'$2'\"' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "wtm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	kept := Worktree(repo, wt, true)
	if !strings.Contains(kept, "wtm switch feat/task") {
		t.Errorf("Worktree() = %q, want the repair named", kept)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Error("the worktree of a stranded stack must be kept")
	}

	// Never given a stack: nothing stranded, the worktree goes.
	if kept := Worktree(repo, wt, false); kept != "" {
		t.Errorf("Worktree(never stacked) = %q, want it removed", kept)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err == nil {
		t.Error("a worktree wtm never gave a stack to should be removed")
	}
}
