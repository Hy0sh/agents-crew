// Package teardown implements agents-crew's `stop`: releasing every
// worker's environment, the worktrees themselves, the shared status
// directory, and the Herdr workspace.
package teardown

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// Run tears down the swarm running in the current directory, if any,
// printing progress to stdout and non-fatal errors to stderr. It returns
// nil even when there was nothing to tear down. Scoped to the current
// directory, not global: a swarm running for a different repo is left
// alone, so several can run at once.
func Run() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	agents, err := herdr.AgentList()
	if err != nil {
		return fmt.Errorf("herdr agent list: %w", err)
	}

	// By name, not by the master's pane cwd: with master-dir it runs
	// elsewhere, and the name already carries this directory's hash.
	repo := cwd
	slug := names.Slug(repo)
	statusDir := names.StatusDir(repo)
	var workspaceID string
	if master, ok := herdr.FindAgent(agents, names.Master(slug)); ok {
		workspaceID = master.WorkspaceID
		fmt.Printf("Stopping the swarm of %s (workspace %s).\n", repo, workspaceID)
	} else {
		// A Herdr crash or a master closed by hand ends the watcher with no
		// teardown: the workers, their worktrees and stacks are still this
		// command's to release.
		if worker, ok := anyWorker(agents, slug); ok {
			workspaceID = worker.WorkspaceID
		}
		if _, err := os.Stat(statusDir); err != nil && workspaceID == "" && len(WorkerWorktrees(repo)) == 0 {
			fmt.Println("No acw master running for this directory.")
			return nil
		}
		fmt.Println("No acw master running for this directory: releasing what its last run left.")
	}

	// Discovered by scanning .claude/worktrees/ for the workerN-* naming
	// convention, not by asking Herdr which agents are named "workerN":
	// a stack can be up (wtm adopt already ran) before the pane for it
	// even exists, let alone before `herdr agent start` names it — a stop
	// run during that window found nothing to clean up otherwise, leaving
	// real Docker stacks orphaned despite reporting success.
	stopWatcher(repo)
	cleanupWorkerWorktrees(repo)

	if workspaceID != "" {
		fmt.Print("Closing the Herdr workspace and its agents... ")
		if err := herdr.WorkspaceClose(workspaceID, true); err != nil {
			fmt.Println("failed.")
			return fmt.Errorf("herdr workspace close: %w", err)
		}
		fmt.Println("done.")
	}

	if err := os.RemoveAll(statusDir); err != nil {
		fmt.Fprintf(os.Stderr, "removing %s: %v\n", statusDir, err)
	}

	fmt.Println("Swarm stopped.")
	return nil
}

// anyWorker finds a worker of the run, of any role, by its full agent
// name: a bare reviewer2 label could be another directory's.
func anyWorker(agents []herdr.Agent, slug string) (herdr.Agent, bool) {
	for _, a := range agents {
		if names.IsWorkerAgent(a.Name, slug) {
			return a, true
		}
	}
	return herdr.Agent{}, false
}

// cleanupWorkerWorktrees is the slow half of a teardown: each worker's
// environment goes down one after another, and a Docker stack takes its
// time. It narrates every step for that reason — a silent minute reads as
// a hang, and the step that is running is the one worth naming when it
// does hang for real.
// WorkerWorktrees lists the worker worktrees of any run in repo, found on
// disk by their naming convention (see cleanupWorkerWorktrees for why not
// through Herdr). A worker outside the code has none, and is not listed.
func WorkerWorktrees(repo string) []string {
	matches, err := filepath.Glob(filepath.Join(names.WorktreesDir(repo), "*"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "looking for worker worktrees: %v\n", err)
		return nil
	}
	var dirs []string
	for _, dir := range matches {
		if !names.IsWorkerWorktree(filepath.Base(dir)) {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// watcherWait bounds how long acw stop waits for the watcher: an opening
// it started may be bringing a stack up.
const watcherWait = 15 * time.Minute

// stopWatcher stops acw's watcher before anything is torn down: an opening
// or a close it runs next to the teardown could leave a stack behind, or
// see a worktree being removed as one with changes. Without pool.json the
// watcher takes its run for gone at its next poll, finishes what it
// started, then lets go of its lock. A watcher that is not running holds
// no lock.
func stopWatcher(repo string) {
	if err := os.Remove(names.PoolFile(repo)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "removing the pool: %v\n", err)
	}
	lock, err := os.OpenFile(names.WatchLock(repo), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	defer lock.Close()
	told := false
	for deadline := time.Now().Add(watcherWait); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			return
		}
		if !told {
			fmt.Println("Waiting for acw's watcher to finish what it started...")
			told = true
		}
	}
	fmt.Fprintln(os.Stderr, "acw's watcher still runs after", watcherWait, "- tearing down anyway")
}

func cleanupWorkerWorktrees(repo string) {
	for _, dir := range WorkerWorktrees(repo) {
		Worktree(repo, dir)
	}
}

// stackedMark is the file, in a worktree's private git dir, that says acw
// got it a wtm stack. Not in pool.json, which acw stop removes and every
// start rewrites: a worktree kept for its stack must still be known as
// stacked by the next run's acw stop. It goes with the worktree.
const stackedMark = "acw-stacked"

// MarkStacked records that the worktree at dir got a wtm stack.
func MarkStacked(dir string) error {
	gitDir, err := gitutil.GitDir(dir)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(gitDir, stackedMark), nil, 0o644)
}

// Unmark forgets that the worktree at dir got a wtm stack: its stack went
// without acw.
func Unmark(dir string) error {
	gitDir, err := gitutil.GitDir(dir)
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(gitDir, stackedMark))
}

// Stacked reports whether acw got the worktree at dir a wtm stack.
func Stacked(dir string) bool {
	gitDir, err := gitutil.GitDir(dir)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(gitDir, stackedMark))
	return err == nil
}

// errNoBranch stands for a worktree whose branch cannot be named to wtm:
// unreadable, or a detached HEAD.
var errNoBranch = errors.New("no branch to name")

// Repair says why wtm could not reach the stack of a stacked worktree on
// branch, from what it answered (err), and what to do about it.
func Repair(dir, branch string, err error) string {
	switch {
	case branch == "HEAD":
		return fmt.Sprintf("%s is on a detached HEAD (a rebase in progress?): finish or abort it, then run this again", dir)
	case errors.Is(err, errNoBranch):
		return fmt.Sprintf("the branch of %s could not be read: fix the worktree, then run this again", dir)
	case errors.Is(err, wtm.ErrUnregistered):
		return "its project is no longer in wtm's registry, so wtm can no longer reach its stack: register the project again, then run this again"
	case errors.Is(err, wtm.ErrNoStack):
		return wtm.StrandedHint(dir, branch)
	case err != nil:
		return fmt.Sprintf("wtm failed (%v): run this again once fixed", err)
	}
	return ""
}

// Worktree releases one worker worktree: its environment, the worktree,
// and the branch acw cut for it. The task branch it was left on stays.
// acw stop runs it on every worker; the elastic pool on a worker it
// closes. When acw got it a stack and wtm could not remove that stack,
// the worktree is kept and Worktree returns why: removed, it would leave
// the stack running with nothing left to find it by.
func Worktree(repo, dir string) (kept string) {
	name := filepath.Base(dir)
	stacked := Stacked(dir)
	branch, err := gitutil.CurrentBranch(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: resolving the branch: %v\n", name, err)
		if !stacked {
			return ""
		}
		branch = ""
	}
	fmt.Printf("%s (%s):\n", name, branch)

	if kept = removeStack(dir, name, branch, stacked); kept != "" {
		fmt.Printf("  stack not removed, worktree kept: %s.\n", kept)
		return kept
	}

	if err := gitutil.WorktreeRemove(repo, dir); err != nil {
		fmt.Fprintf(os.Stderr, "%s: git worktree remove: %v\n", name, err)
		return
	}
	// A worker takes each task on a new branch in its worktree: that
	// branch is its work, pushed or not, and stays. Only the branch acw
	// cut for it goes, and only if nothing was committed on it.
	if own := "agents/" + name; branch != own {
		fmt.Printf("  worktree removed, task branch %s kept.\n", branch)
		if err := gitutil.DeleteMergedBranch(repo, own); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s kept: %v\n", name, own, err)
		}
		return
	}
	if err := gitutil.DeleteBranch(repo, branch); err != nil {
		fmt.Printf("  worktree removed, branch %s kept (see below).\n", branch)
		fmt.Fprintf(os.Stderr, "%s: git branch -D %s: %v\n", name, branch, err)
		return
	}
	fmt.Printf("  worktree and branch %s removed.\n", branch)
	return ""
}

// removeStack removes the worktree's stack, whether it runs or was stopped
// (acw pause, a reboot): a stopped stack wtm still lists is one nobody will
// clean up. For a stacked worktree it returns why the stack is still
// there, "" once it is gone or when there never was one.
func removeStack(dir, name, branch string, stacked bool) string {
	if !wtm.Available() {
		if stacked {
			return fmt.Sprintf("wtm is not on PATH, so its stack could not be removed: run `wtm remove %s` from %s", branch, dir)
		}
		return ""
	}
	fmt.Print("  removing the environment (containers, volumes, images)... ")
	err := errNoBranch
	if branch != "" && branch != "HEAD" {
		err = wtm.Remove(dir, branch)
	}
	if err != nil && stacked && !errors.Is(err, wtm.ErrUnregistered) {
		// The branch acw adopted the worktree under: moved away from it
		// without wtm switch, the worktree left that index behind, which
		// wtm takes as stale and takes down.
		if wtm.Remove(dir, "agents/"+name) == nil {
			err = nil
		}
	}
	switch {
	case err == nil:
		fmt.Println("done.")
	case stacked:
		fmt.Println("failed.")
		return Repair(dir, branch, err)
	case errors.Is(err, wtm.ErrNoStack), errors.Is(err, wtm.ErrUnregistered), errors.Is(err, errNoBranch):
		// A worktree acw never got a stack (beyond max-stacks, a failed
		// adopt, a repo wtm doesn't know): nothing to remove.
		fmt.Println("none.")
	default:
		fmt.Println("failed, see below.")
		fmt.Fprintf(os.Stderr, "%s: wtm remove: %v\n", name, err)
	}
	return ""
}
