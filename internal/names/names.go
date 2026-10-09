// Package names derives the Herdr agent names for a run, scoped to the
// repo it targets so several swarms — one per project directory — can run
// at the same time without colliding. Herdr requires every live agent
// name to be globally unique and match [a-z][a-z0-9_-]{0,31}; a plain
// "master"/"worker1" only ever supported one swarm system-wide.
//
// It also holds where a run puts its worktrees, branches and status
// files: provisioning creates them and `acw stop` has to find exactly
// those again, so the convention lives in one place.
package names

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var invalidChar = regexp.MustCompile(`[^a-z0-9-]`)

// Slug derives a short, herdr-safe, collision-resistant identifier for
// repo: a hash of its full path, so two different directories sharing a
// basename never collide, and repeated runs in the same directory always
// agree on the same names.
//
// Just the hash, with no readable basename: agent names show up in
// Herdr's sidebar in a narrow column, and a "worker1-shop-frontend-3f9a1c"
// gets truncated there to exactly the part that is NOT discriminating.
// Which repo a swarm belongs to is carried by the workspace label (see
// Label) where it is displayed once, and in code by the agent's own cwd,
// which is what every lookup here actually matches on.
func Slug(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(sum[:])[:6]
}

// Label is the workspace label for a run in repo: the tool, so a swarm is
// recognizable among other Herdr workspaces, and the repo's directory
// name, so two swarms are tellable apart at a glance.
func Label(repo string) string {
	base := strings.ToLower(filepath.Base(repo))
	base = invalidChar.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		return "acw"
	}
	return "acw " + base
}

// Master is the master agent's name for a run identified by slug.
func Master(slug string) string {
	return "master-" + slug
}

// WorktreesDir is where a run in repo puts its workers' worktrees.
func WorktreesDir(repo string) string {
	return filepath.Join(repo, ".claude", "worktrees")
}

// StatusDir holds the workers' status files, one workerN.json each.
func StatusDir(repo string) string {
	return filepath.Join(WorktreesDir(repo), ".acw-status")
}

// Inbox is the file workers' pings are appended to, for the master to
// watch.
func Inbox(repo string) string {
	return filepath.Join(StatusDir(repo), "inbox")
}

// PoolFile holds the elastic pool: what a worker is opened with, and each
// open worker's state.
func PoolFile(repo string) string {
	return filepath.Join(StatusDir(repo), "pool.json")
}

// WatchLock is held by acw's watcher for as long as it runs, what it
// started included: acw stop waits for it before tearing anything down.
func WatchLock(repo string) string {
	return filepath.Join(StatusDir(repo), "watch.lock")
}

// WatchPlan is the watcher's plan, kept for acw watch to start it again.
func WatchPlan(repo string) string {
	return filepath.Join(StatusDir(repo), "watch.json")
}

// QueueFile holds the tasks waiting for a worker.
func QueueFile(repo string) string {
	return filepath.Join(StatusDir(repo), "queue.json")
}

// PoolLock is locked by whoever changes the pool or the queue: the
// watcher, and the master's acw queue and acw done.
func PoolLock(repo string) string {
	return filepath.Join(StatusDir(repo), "pool.lock")
}

// WorkerWorktree is worker i's worktree for the run started at stamp.
func WorkerWorktree(repo string, i int, stamp string) string {
	return filepath.Join(WorktreesDir(repo), fmt.Sprintf("worker%d-%s", i, stamp))
}

// WorkerBranch is the branch worker i's worktree starts on.
func WorkerBranch(i int, stamp string) string {
	return fmt.Sprintf("agents/worker%d-%s", i, stamp)
}

var workerWorktreeName = regexp.MustCompile(`^worker\d+-.+$`)

// IsWorkerWorktree reports whether a directory name under WorktreesDir is
// one WorkerWorktree made, of any run.
func IsWorkerWorktree(dirName string) bool {
	return workerWorktreeName.MatchString(dirName)
}

// WorkerIndex reads a worker's index from how it is named: its label
// (worker2) or its agent name for slug (Worker(slug, 2)). ok is false for
// anything else, a zero-padded or suffixed index included.
func WorkerIndex(name, slug string) (i int, ok bool) {
	if _, err := fmt.Sscanf(name, "worker%d", &i); err != nil || i < 1 {
		return 0, false
	}
	return i, name == fmt.Sprintf("worker%d", i) || name == Worker(slug, i)
}

// Worker is worker i's agent name for a run identified by slug.
func Worker(slug string, i int) string {
	return fmt.Sprintf("worker%d-%s", i, slug)
}
