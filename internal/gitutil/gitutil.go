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
	"time"
)

func run(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %v (in %s): %w: %s", args, repo, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Fetch runs `git fetch origin` in repo.
func Fetch(repo string) error {
	_, err := run(repo, "fetch", "origin")
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
	out, err := exec.Command("git", "--no-optional-locks", "-C", dir, "status", "--porcelain", "-z", "--untracked-files=all").Output()
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

// DeleteBranch force-deletes branch in repo.
func DeleteBranch(repo, branch string) error {
	_, err := run(repo, "branch", "-D", branch)
	return err
}
