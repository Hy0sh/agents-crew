package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/brief"
)

// workerSpec is one worker as it will be started: the global worker-kind
// and worker-model, unless the config overrides them for this worker.
type workerSpec struct {
	Kind  string `json:"kind"`
	Model string `json:"model"`
	// PromptPath is absolute: the worker runs from its own worktree, where
	// a path relative to the repo would point somewhere else.
	PromptPath string `json:"prompt_path,omitempty"`
	Prompt     string `json:"-"` // content, for the master's brief only
	Overridden bool   `json:"overridden,omitempty"`
}

// resolveWorkers builds the spec of each of the opts.workers workers. An
// override for a worker that doesn't exist, or a prompt that can't be
// read, refuses to start: a worker meant to verify that silently becomes
// a generic one would skew every dispatch the master makes.
func resolveWorkers(opts *startOptions, repo string) ([]workerSpec, error) {
	workers := make([]workerSpec, opts.workers)
	for i := range workers {
		workers[i] = workerSpec{Kind: opts.workerKind, Model: opts.workerModel}
	}
	for key, o := range opts.overrides {
		i, err := strconv.Atoi(key)
		if err != nil || i < 1 || i > opts.workers {
			return nil, fmt.Errorf("worker-overrides: %q ne désigne aucun worker (de 1 à %d)", key, opts.workers)
		}
		w := &workers[i-1]
		w.Overridden = true
		if o.Kind != nil {
			w.Kind = *o.Kind
		}
		if o.Model != nil {
			w.Model = *o.Model
		}
		if o.Prompt != nil {
			path := *o.Prompt
			if !filepath.IsAbs(path) {
				path = filepath.Join(repo, path)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("worker-overrides %s: prompt illisible: %w", key, err)
			}
			w.PromptPath, w.Prompt = path, string(content)
		}
	}
	return workers, nil
}

// distinctKinds lists each kind once, in worker order.
func distinctKinds(workers []workerSpec) []string {
	var kinds []string
	seen := map[string]bool{}
	for _, w := range workers {
		if !seen[w.Kind] {
			seen[w.Kind] = true
			kinds = append(kinds, w.Kind)
		}
	}
	return kinds
}

func briefWorkers(workers []workerSpec) []brief.Worker {
	out := make([]brief.Worker, len(workers))
	for i, w := range workers {
		out[i] = brief.Worker{Kind: w.Kind, Model: w.Model, Prompt: w.Prompt, Overridden: w.Overridden}
	}
	return out
}

// describeWorkers is the launch line's view of the workers: one kind and
// model when they all share them, each worker otherwise.
func describeWorkers(workers []workerSpec) string {
	if len(workers) == 0 {
		return ""
	}
	same := true
	for _, w := range workers {
		if w.Kind != workers[0].Kind || w.Model != workers[0].Model {
			same = false
			break
		}
	}
	if same {
		return brief.DescribeAgent(workers[0].Kind, workers[0].Model)
	}
	parts := make([]string, len(workers))
	for i, w := range workers {
		parts[i] = fmt.Sprintf("worker%d %s", i+1, brief.DescribeAgent(w.Kind, w.Model))
	}
	return strings.Join(parts, ", ")
}

// provisionPlan is everything the detached provisioner needs, passed as
// one JSON argument: a list per worker doesn't fit positional arguments.
type provisionPlan struct {
	Repo       string       `json:"repo"`
	MasterPane string       `json:"master_pane"`
	Stamp      string       `json:"stamp"`
	MaxStacks  int          `json:"max_stacks"`
	Profile    string       `json:"profile"`
	Workers    []workerSpec `json:"workers"`
}
