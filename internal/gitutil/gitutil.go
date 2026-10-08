// Package gitutil wraps the handful of git operations agents-crew needs
// to give each worker a fresh, up-to-date worktree.
package gitutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func run(repo string, args ...string) (string, error) {
	out, err := output(append([]string{"-C", repo}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// output runs git with args as given, global flags included, and returns
// its stdout untouched.
func output(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %v: %w: %s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Fetch runs `git fetch origin` in repo.
func Fetch(repo string) error {
	// Two workers taking a task at once fetched into the same refs and
	// both failed on "cannot lock ref": acw's fetches take turns, and one
	// colliding with a fetch of the worker's own is tried again once.
	common, err := run(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(common, "acw-fetch.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	_, err = run(repo, "fetch", "origin")
	if err != nil && strings.Contains(err.Error(), "cannot lock ref") {
		time.Sleep(2 * time.Second)
		_, err = run(repo, "fetch", "origin")
	}
	return err
}

// Divergence counts, after a fetch, the commits of the local branch that
// origin's lacks (ahead) and those it lacks from origin's (behind). Zero
// both when either side doesn't exist: nothing to compare.
func Divergence(dir, branch string) (ahead, behind int, err error) {
	local, remote := "refs/heads/"+branch, "refs/remotes/origin/"+branch
	for _, ref := range []string{local, remote} {
		if _, err := run(dir, "rev-parse", "--verify", "--quiet", ref); err != nil {
			return 0, 0, nil
		}
	}
	out, err := run(dir, "rev-list", "--left-right", "--count", local+"..."+remote)
	if err != nil {
		return 0, 0, err
	}
	_, err = fmt.Sscan(out, &ahead, &behind)
	return ahead, behind, err
}

// FastForward brings the branch checked out in dir up to origin's.
func FastForward(dir, branch string) error {
	_, err := run(dir, "merge", "--ff-only", "refs/remotes/origin/"+branch)
	return err
}

// WorktreeBranches maps each worktree of repo to the branch checked out
// there, "" for a detached HEAD.
func WorktreeBranches(repo string) (map[string]string, error) {
	out, err := run(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktreeBranches(out), nil
}

func parseWorktreeBranches(porcelain string) map[string]string {
	branches := map[string]string{}
	var path string
	for _, line := range strings.Split(porcelain, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
			branches[path] = ""
		case strings.HasPrefix(line, "branch refs/heads/"):
			branches[path] = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}
	return branches
}

// HasBranch reports whether branch exists in dir's repo, locally or on
// origin.
func HasBranch(dir, branch string) bool {
	for _, ref := range []string{"refs/heads/" + branch, "refs/remotes/origin/" + branch} {
		if _, err := run(dir, "rev-parse", "--verify", "--quiet", ref); err == nil {
			return true
		}
	}
	return false
}

// Switch puts dir on branch, cut from base when create is set. A branch
// only on origin is checked out tracking it. git refuses when local
// changes would be lost, and nothing is stashed.
func Switch(dir, branch, base string, create bool) error {
	args := []string{"switch", branch}
	if create {
		args = []string{"switch", "-c", branch, base}
	}
	_, err := run(dir, args...)
	return err
}

// DefaultBaseRef returns the remote's default branch as "origin/<branch>"
// (e.g. "origin/develop"), or "HEAD" if it cannot be determined — never a
// hardcoded branch name, so this works across projects regardless of
// convention.
func DefaultBaseRef(repo string) string {
	out, err := run(repo, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil || out == "" {
		return "HEAD"
	}
	branch := strings.TrimPrefix(out, "refs/remotes/origin/")
	return "origin/" + branch
}

// WorktreeAdd creates a new worktree at path on a new branch, based on
// baseRef.
func WorktreeAdd(repo, path, branch, baseRef string) error {
	_, err := run(repo, "worktree", "add", path, "-b", branch, baseRef)
	return err
}

// GitDir is the private git directory of the worktree at dir (for a linked
// worktree, .git/worktrees/<name> of its repo): it goes with the worktree
// and is never committed.
func GitDir(dir string) (string, error) {
	return run(dir, "rev-parse", "--absolute-git-dir")
}

// CurrentBranch returns the branch checked out in dir.
func CurrentBranch(dir string) (string, error) {
	return run(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// WorktreeRemove removes the worktree at path. wtm's own `remove` only
// deletes a worktree it created itself — one agents-crew made via
// WorktreeAdd (plain `git worktree add`, not `wtm create`) is left on disk
// otherwise, so this is still needed after a wtm stack is torn down.
func WorktreeRemove(repo, path string) error {
	_, err := run(repo, "worktree", "remove", path, "--force")
	return err
}

// LastActivity is the latest sign of work in the worktree at dir, read
// from the disk rather than from what its agent says: the mtime of its
// index (a stage, a commit, a checkout) and of every file git status
// lists. Zero when dir is not a git worktree.
func LastActivity(dir string) time.Time {
	var latest time.Time
	see := func(path string) {
		if info, err := os.Stat(path); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	index, err := run(dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return time.Time{}
	}
	see(index)
	// Not through run: its TrimSpace would eat the leading space of the
	// first " M path" entry. --no-optional-locks: a plain status refreshes
	// the index under index.lock, and this runs every few seconds next to
	// a worker's own git add.
	out, err := output("--no-optional-locks", "-C", dir, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return latest
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		// "XY path"; a rename's source comes as its own entry after it and
		// is skipped by os.Stat failing on it.
		if len(entry) > 3 {
			see(filepath.Join(dir, entry[3:]))
		}
	}
	return latest
}

// Clean reports whether the worktree at dir has no change, untracked
// files included: closing it would lose them. A dir git cannot read is
// not clean.
func Clean(dir string) bool {
	out, err := output("--no-optional-locks", "-C", dir, "status", "--porcelain", "--untracked-files=all")
	return err == nil && len(bytes.TrimSpace(out)) == 0
}

// Unpushed counts the commits of branch that no remote branch holds: work
// that only lives in this clone.
func Unpushed(repo, branch string) (int, error) {
	out, err := run(repo, "rev-list", "--count", branch, "--not", "--remotes")
	if err != nil {
		return 0, err
	}
	var n int
	_, err = fmt.Sscan(out, &n)
	return n, err
}

// DeleteBranch force-deletes branch in repo.
func DeleteBranch(repo, branch string) error {
	_, err := run(repo, "branch", "-D", branch)
	return err
}

// DeleteMergedBranch deletes branch only when git sees nothing on it that
// would be lost (`git branch -d`).
func DeleteMergedBranch(repo, branch string) error {
	_, err := run(repo, "branch", "-d", branch)
	return err
}
