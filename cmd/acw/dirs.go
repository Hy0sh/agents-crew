package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// An agent given its own folder (master-dir, or a worker's dir) starts
// there and loads that folder's .claude instead of the repo's. A worker
// with a dir is one outside the code: no worktree, environment or branch.
// Whoever codes stays in a worktree of the repo — hence the refusal of a
// dir inside it, which would have an agent working on the main checkout.

// validateAgentDir returns dir if an agent may start there: an existing
// directory, given absolute, outside repo.
func validateAgentDir(repo, dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%q is not an absolute path", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	// Resolved on both sides: on macOS /tmp is /private/tmp, and a symlink
	// is exactly how a path inside the repo would slip through.
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(realRepo, realDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is inside the repo: an agent that codes works in its worktree, not in the main checkout", dir)
	}
	return dir, nil
}

// coderCount is how many workers code, i.e. have no dir of their own.
func coderCount(workers []workerSpec) int {
	n := 0
	for _, w := range workers {
		if w.Dir == "" {
			n++
		}
	}
	return n
}

// explainStart names the likely cause when an agent never became ready: a
// Claude Code dialog at startup, such as whether to trust a folder it has
// never opened, which an agent started in a new master-dir or dir sits on.
// A worker's screen, joined by its engine, shows which.
func explainStart(err error, dir string) error {
	if err == nil || !strings.Contains(err.Error(), "blocked during startup") {
		return err
	}
	return fmt.Errorf("%w (a Claude Code dialog holds it at startup, such as the trust prompt for a folder it never opened: run `claude` once in %s to answer it, then try again)", err, dir)
}
