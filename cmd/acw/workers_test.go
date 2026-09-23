package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/config"
)

func ptr(s string) *string { return &s }

func TestResolveWorkersAppliesOverridesOnTopOfDefaults(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "verifier.md"), []byte("Tu vérifies."), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := &startOptions{workers: 3, workerKind: "claude", workerModel: "sonnet", overrides: map[string]config.WorkerOverride{
		"1": {Model: ptr("")},
		"3": {Kind: ptr("codex"), Prompt: ptr("verifier.md")},
	}}

	got, err := resolveWorkers(opts, repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []workerSpec{
		{Kind: "claude", Model: "", Overridden: true}, // "" is set, not absent
		{Kind: "claude", Model: "sonnet"},
		{Kind: "codex", Model: "sonnet", PromptPath: filepath.Join(repo, "verifier.md"), Prompt: "Tu vérifies.", Overridden: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveWorkers() =\n%+v\nwant\n%+v", got, want)
	}
}

func TestResolveWorkersRejectsIndexesThatNameNoWorker(t *testing.T) {
	for _, key := range []string{"0", "3", "a", "-1"} {
		opts := &startOptions{workers: 2, workerKind: "claude", overrides: map[string]config.WorkerOverride{key: {}}}
		_, err := resolveWorkers(opts, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("resolveWorkers() with override %q for 2 workers: error = %v, want one naming it", key, err)
		}
	}
}

func TestResolveWorkersRejectsAnUnreadablePrompt(t *testing.T) {
	opts := &startOptions{workers: 1, workerKind: "claude", overrides: map[string]config.WorkerOverride{"1": {Prompt: ptr("absent.md")}}}
	if _, err := resolveWorkers(opts, t.TempDir()); err == nil {
		t.Error("resolveWorkers() with a missing prompt file = nil error; a worker must not silently lose its instructions")
	}
}

func TestDescribeWorkers(t *testing.T) {
	same := []workerSpec{{Kind: "claude", Model: "sonnet"}, {Kind: "claude", Model: "sonnet"}}
	if got := describeWorkers(same); got != "claude sonnet" {
		t.Errorf("describeWorkers(same) = %q", got)
	}
	mixed := []workerSpec{{Kind: "claude", Model: "opus"}, {Kind: "codex"}}
	if got := describeWorkers(mixed); got != "worker1 claude opus, worker2 codex" {
		t.Errorf("describeWorkers(mixed) = %q", got)
	}
}

// The plan crosses a process boundary as one JSON argument: anything that
// doesn't survive the round trip reaches the provisioner as a zero value.
func TestProvisionPlanRoundTrip(t *testing.T) {
	plan := provisionPlan{Repo: "/repo", MasterPane: "p1", Stamp: "20260923", MaxStacks: 2, Profile: "light", Workers: []workerSpec{
		{Kind: "claude", Model: "opus", PromptPath: "/cfg/a.md", Overridden: true},
		{Kind: "codex"},
	}}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var got provisionPlan
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, plan) {
		t.Errorf("round trip = %+v, want %+v", got, plan)
	}
}
