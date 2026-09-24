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
		return "", fmt.Errorf("%q n'est pas un chemin absolu", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s n'est pas un dossier", dir)
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
		return "", fmt.Errorf("%s est dans le dépôt : un agent qui code travaille dans son worktree, pas dans la copie principale", dir)
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

// stackedWorkers says which workers get an environment: the first
// maxStacks coders, in order. A worker outside the code needs none.
func stackedWorkers(workers []workerSpec, maxStacks int) []bool {
	stacked := make([]bool, len(workers))
	for i, w := range workers {
		if w.Dir == "" && maxStacks > 0 {
			stacked[i] = true
			maxStacks--
		}
	}
	return stacked
}

// explainStart names the likely cause when an agent never became ready:
// Claude Code asks whether to trust a folder it has never opened, and an
// agent started in a new master-dir or dir sits on that prompt.
func explainStart(err error, dir string) error {
	if err == nil || !strings.Contains(err.Error(), "blocked during startup") {
		return err
	}
	return fmt.Errorf("%w — sans doute la demande de confiance de Claude Code pour %s : lance `claude` une fois dans ce dossier pour l'accepter, puis relance", err, dir)
}
