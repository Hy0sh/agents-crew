// Package gitutil wraps the handful of git operations agents-crew needs
// to give each worker a fresh, up-to-date worktree.
package gitutil

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
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

// DeleteBranch force-deletes branch in repo.
func DeleteBranch(repo, branch string) error {
	_, err := run(repo, "branch", "-D", branch)
	return err
}
