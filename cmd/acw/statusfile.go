package main

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
)

// writeAtomic writes content aside and renames it over path: acw's
// watcher and acw status read these files at any moment, and a
// truncate-then-write would show them an empty one.
func writeAtomic(path string, content []byte, perm fs.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// workerStatus is what acw reads back from a worker's status file.
type workerStatus struct {
	Tache       string `json:"tache"`
	State       string `json:"state"`
	Branch      string `json:"branch"`
	BaseBranch  string `json:"base_branch"`
	PRURL       string `json:"pr_url"`
	UpdatedAt   string `json:"updated_at"`
	LastTurnEnd string `json:"last_turn_end"`
}

// readWorkerStatus reads the status file once, content and mtime. A
// missing or unreadable file gives zero values: a worker that wrote
// nothing yet is not an error.
func readWorkerStatus(path string) (s workerStatus, mtime time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return s, mtime
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil {
		mtime = info.ModTime()
	}
	if content, err := io.ReadAll(f); err == nil {
		_ = json.Unmarshal(content, &s)
	}
	return s, mtime
}

// activity is the latest of what acw can read about a worker without
// trusting it: last_turn_end (stamped by acw), its status file's mtime,
// and worktreeActivity, the latest change in its worktree.
func activity(s workerStatus, mtime, worktreeActivity time.Time) time.Time {
	latest := mtime
	if t, err := time.Parse(time.RFC3339, s.LastTurnEnd); err == nil && t.After(latest) {
		latest = t
	}
	if worktreeActivity.After(latest) {
		latest = worktreeActivity
	}
	return latest
}

func worktreeActivity(worktree string) time.Time {
	if worktree == "" {
		return time.Time{}
	}
	return gitutil.LastActivity(worktree)
}
