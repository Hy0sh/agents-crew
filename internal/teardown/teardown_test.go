package teardown

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
	wt := names.WorkerWorktree(repo, "worker1", "20261002")
	git(t, repo, "worktree", "add", "-q", "-b", names.WorkerBranch("worker1", "20261002"), wt)
	git(t, wt, "switch", "-q", "-c", "feat/task")
	git(t, wt, "commit", "-q", "--allow-empty", "-m", "work not pushed")

	cleanupWorkerWorktrees(repo)

	if _, err := exec.Command("test", "-e", filepath.Join(wt, ".git")).CombinedOutput(); err == nil {
		t.Error("the worktree should be removed")
	}
	if got := git(t, repo, "log", "-1", "--format=%s", "feat/task"); got != "work not pushed" {
		t.Errorf("feat/task head = %q, want the task's commit kept", got)
	}
	if got := git(t, repo, "branch", "--list", names.WorkerBranch("worker1", "20261002")); got != "" {
		t.Errorf("acw's own branch is still there: %q", got)
	}
}

// A worktree wtm gave a stack to, moved to another branch without wtm
// switch: wtm finds no stack under the current branch. Removing the
// worktree would leave that stack running out of anyone's reach.
func TestWorktreeKeepsAStrandedStack(t *testing.T) {
	repo, wt := workerWorktree(t)
	git(t, wt, "switch", "-q", "-c", "feat/task")
	fakeWtm(t, "echo 'Error: no worktree for branch \"'$2'\"' >&2\nexit 1")

	if err := MarkStacked(wt); err != nil {
		t.Fatal(err)
	}

	kept := Worktree(repo, wt)
	if !strings.Contains(kept, "wtm list") || !strings.Contains(kept, "feat/task") {
		t.Errorf("Worktree() = %q, want the repair named", kept)
	}
	if !exists(wt) {
		t.Error("the worktree of a stranded stack must be kept")
	}
	// Still known as stacked by the next run: the mark is on disk.
	if !Stacked(wt) {
		t.Error("the kept worktree lost its mark")
	}
}

// A worktree acw never got a stack: wtm finding none is not a failure.
func TestWorktreeWithoutAStackGoes(t *testing.T) {
	repo, wt := workerWorktree(t)
	fakeWtm(t, "echo 'Error: no worktree for branch \"'$2'\"' >&2\nexit 1")
	if kept := Worktree(repo, wt); kept != "" || exists(wt) {
		t.Errorf("Worktree(never stacked) = %q, want it removed", kept)
	}
}

// Moved off the branch acw adopted it under: that index is stale to wtm,
// which takes its stack down when asked by that branch.
func TestWorktreeRetriesUnderTheAdoptedBranch(t *testing.T) {
	repo, wt := workerWorktree(t)
	git(t, wt, "switch", "-q", "-c", "feat/task")
	calls := fakeWtm(t, `[ "$2" = "feat/task" ] && { echo 'Error: no worktree for branch "feat/task"' >&2; exit 1; }; exit 0`)
	if err := MarkStacked(wt); err != nil {
		t.Fatal(err)
	}
	if kept := Worktree(repo, wt); kept != "" || exists(wt) {
		t.Errorf("Worktree() = %q, want the stack found under the adopted branch and the worktree removed", kept)
	}
	want := "remove feat/task\nremove " + names.WorkerBranch("worker1", "20261002") + "\n"
	if got, _ := os.ReadFile(calls); string(got) != want {
		t.Errorf("wtm calls = %q, want %q", got, want)
	}
}

// A stack is removed whether it runs or was stopped (acw pause, a
// reboot): stopped, wtm still lists it. One remove, no stop first.
func TestWorktreeRemovesTheStackRunningOrNot(t *testing.T) {
	repo, wt := workerWorktree(t)
	calls := fakeWtm(t, "exit 0")
	if err := MarkStacked(wt); err != nil {
		t.Fatal(err)
	}
	if kept := Worktree(repo, wt); kept != "" || exists(wt) {
		t.Fatalf("Worktree() = %q, want the stack and the worktree removed", kept)
	}
	if got, _ := os.ReadFile(calls); string(got) != "remove "+names.WorkerBranch("worker1", "20261002")+"\n" {
		t.Errorf("wtm calls = %q, want one remove and no stop first", got)
	}
}

// What wtm cannot reach for another reason than a branch change keeps
// the worktree with its own repair, never the branch-change one.
func TestWorktreeKeepsAStackWtmCannotReach(t *testing.T) {
	for _, c := range []struct {
		name, script, want string
		detach, noWtm      bool
	}{
		{"unregistered project", "echo 'Error: project \"x\" is not registered' >&2\nexit 1", "no longer in wtm's registry", false, false},
		{"detached HEAD", "echo 'Error: no worktree for branch \"'$2'\"' >&2\nexit 1", "detached HEAD", true, false},
		{"wtm not on PATH", "", "wtm is not on PATH", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, wt := workerWorktree(t)
			if c.detach {
				git(t, wt, "switch", "-q", "--detach")
			}
			if c.noWtm {
				onlyGit(t)
			} else {
				fakeWtm(t, c.script)
			}
			if err := MarkStacked(wt); err != nil {
				t.Fatal(err)
			}
			kept := Worktree(repo, wt)
			if !strings.Contains(kept, c.want) || strings.Contains(kept, "without wtm switch") {
				t.Errorf("Worktree() = %q, want %q and not the branch-change repair", kept, c.want)
			}
			if !exists(wt) {
				t.Error("the worktree must be kept")
			}
		})
	}
}

// acw stop tears nothing down while the watcher may still open or close a
// worker: it takes its pool away and waits for its lock.
func TestStopWatcherWaitsForTheWatcher(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(names.PoolFile(repo), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(names.WatchLock(repo), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { stopWatcher(repo); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stopWatcher returned while the watcher held its lock")
	case <-time.After(time.Second):
	}
	if exists(names.PoolFile(repo)) {
		t.Error("the pool is still there: the watcher would go on")
	}
	held.Close()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopWatcher did not return once the lock was free")
	}
}

// A master gone without acw stop (Herdr crashed, its pane closed by hand)
// took the watcher with it: acw stop still releases the workers it left,
// their worktrees and the workspace they run in.
func TestStopWithoutAMasterReleasesWhatItsRunLeft(t *testing.T) {
	t.Chdir(t.TempDir())
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := names.WorkerWorktree(repo, "worker1", "20261002")
	git(t, repo, "worktree", "add", "-q", "-b", names.WorkerBranch("worker1", "20261002"), wt)
	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeWtm(t, "exit 0")
	calls := fakeHerdr(t, `{"result":{"agents":[`+
		`{"name":"worker1","workspace_id":"elsewhere"},`+
		`{"name":"`+names.Worker(names.Slug(repo), 1)+`","workspace_id":"ws-7"}]}}`)

	if err := Run(); err != nil {
		t.Fatal(err)
	}

	if exists(wt) {
		t.Error("the worker's worktree is still there")
	}
	if exists(names.StatusDir(repo)) {
		t.Error("the status directory is still there")
	}
	if log, _ := os.ReadFile(calls); !strings.Contains(string(log), "workspace close ws-7 --group") {
		t.Errorf("herdr calls = %q, want the workers' workspace closed, not another directory's", log)
	}
}

// fakeHerdr puts first on PATH a herdr that logs its arguments and answers
// agent list with agents; it returns the log.
func fakeHerdr(t *testing.T, agents string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	body := "#!/bin/sh\necho \"$@\" >> '" + calls + "'\n" +
		"[ \"$1 $2\" = 'agent list' ] && echo '" + agents + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func workerWorktree(t *testing.T) (repo, wt string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt = names.WorkerWorktree(repo, "worker1", "20261002")
	git(t, repo, "worktree", "add", "-q", "-b", names.WorkerBranch("worker1", "20261002"), wt)
	return repo, wt
}

// fakeWtm puts first on PATH a wtm that logs its arguments, then runs
// script; it returns the log.
func fakeWtm(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	body := "#!/bin/sh\necho \"$@\" >> '" + calls + "'\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(bin, "wtm"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// onlyGit leaves git alone on PATH: no wtm.
func onlyGit(t *testing.T) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
