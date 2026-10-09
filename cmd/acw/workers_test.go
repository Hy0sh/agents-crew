package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/config"
	"github.com/Hy0sh/agents-crew/internal/names"
)

func ptr(s string) *string { return &s }

func intp(n int) *int { return &n }

// Each role becomes max slots, in config order, named role+rank; a role's
// keys lay over the swarm's own, "" included.
func TestBuildSlotsFromRoles(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "verifier.md"), []byte("You verify."), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := &startOptions{workers: 9, workerKind: "claude", workerModel: "sonnet", profile: "light", roles: config.Roles{
		{Name: "worker", Role: config.Role{Max: intp(2), Min: intp(1), Model: ptr("")}},
		{Name: "reviewer", Role: config.Role{Max: intp(2), Model: ptr("opus"), Tasks: []string{"need-review", " analysis "}, Profile: ptr("api")}},
		{Name: "verifier", Role: config.Role{Max: intp(1), Kind: ptr("codex"), Prompt: ptr("verifier.md")}},
	}}
	got, err := buildSlots(opts, repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []workerSpec{
		{Kind: "claude", Model: "", Role: "worker", Rank: 1, Min: 1}, // "" is set, not absent
		{Kind: "claude", Model: "", Role: "worker", Rank: 2, Min: 1},
		{Kind: "claude", Model: "opus", Role: "reviewer", Rank: 1, Tasks: []string{"need-review", "analysis"}, Profile: "api", Overridden: true},
		{Kind: "claude", Model: "opus", Role: "reviewer", Rank: 2, Tasks: []string{"need-review", "analysis"}, Profile: "api", Overridden: true},
		{Kind: "codex", Model: "sonnet", Role: "verifier", Rank: 1, PromptPath: filepath.Join(repo, "verifier.md"), Prompt: "You verify."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildSlots() =\n%+v\nwant\n%+v", got, want)
	}
	var labels []string
	for _, s := range got {
		labels = append(labels, s.label())
	}
	if !slices.Equal(labels, []string{"worker1", "worker2", "reviewer1", "reviewer2", "verifier1"}) {
		t.Errorf("labels = %v", labels)
	}
	if !got[2].takes(queuedTask{Kind: "need-review"}) || got[2].takes(queuedTask{}) || !got[0].takes(queuedTask{}) || !got[4].takes(queuedTask{}) {
		t.Error("takes: a role with tasks takes those kinds only, one without takes the tasks with no kind")
	}
	plan := provisionPlan{Profile: "light", Workers: got}
	if plan.profileOf(1) != "light" || plan.profileOf(3) != "api" {
		t.Errorf("profiles = %q, %q", plan.profileOf(1), plan.profileOf(3))
	}
	for _, bad := range []string{"", "need review", "--all"} {
		opts.roles = config.Roles{{Name: "reviewer", Role: config.Role{Max: intp(1), Tasks: []string{bad}}}}
		if _, err := buildSlots(opts, t.TempDir()); err == nil {
			t.Errorf("tasks [%q] accepted", bad)
		}
	}
	opts.roles = config.Roles{{Name: "worker", Role: config.Role{Max: intp(1), Prompt: ptr("absent.md")}}}
	if _, err := buildSlots(opts, t.TempDir()); err == nil {
		t.Error("buildSlots() with a missing prompt file = nil error; a worker must not silently lose its instructions")
	}
}

// Without roles, workers and min-workers make one role, worker.
func TestBuildSlotsWithoutRoles(t *testing.T) {
	got, err := buildSlots(&startOptions{workers: 3, minWorkers: 2, workerKind: "claude", workerModel: "sonnet"}, t.TempDir())
	if err != nil || len(got) != 3 || got[2].label() != "worker3" || got[0].Min != 2 || got[0].Model != "sonnet" || got[0].Overridden {
		t.Errorf("implicit role = %+v, %v", got, err)
	}
}

// A name resolves to the one slot it names exactly, by label or herdr
// name, even when one role's name starts another's.
func TestSlotOf(t *testing.T) {
	plan := provisionPlan{Workers: []workerSpec{{Role: "review", Rank: 1}, {Role: "reviewer", Rank: 1}, {Role: "reviewer", Rank: 12}}}
	for arg, want := range map[string]int{"review1": 1, "reviewer1": 2, "reviewer12": 3, "reviewer12-aa2ce4": 3} {
		if i, label, err := plan.slotOf(arg, "aa2ce4"); err != nil || i != want || label != plan.Workers[want-1].label() {
			t.Errorf("slotOf(%q) = %d, %q, %v, want %d", arg, i, label, err, want)
		}
	}
	if _, _, err := plan.slotOf("reviewer2", "aa2ce4"); err == nil || !strings.Contains(err.Error(), "reviewer12") {
		t.Errorf("unknown name: %v, want the list of names", err)
	}
}

// A pool.json written before roles keeps its workerN names.
func TestLegacyPoolKeepsNames(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"plan":{"repo":"` + repo + `","workers":[{"kind":"claude"},{"kind":"claude"},{"kind":"claude","overridden":true,"tasks":["need-review"],"keep":true}]},"min_workers":1,"workers":[{"index":3,"state":"free"}]}`
	if err := os.WriteFile(names.PoolFile(repo), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _, err := readPool(repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.Workers[0].label() != "worker3" || p.Plan.Workers[2].label() != "worker3" {
		t.Errorf("labels = %q, %q", p.Workers[0].label(), p.Plan.Workers[2].label())
	}
	if i, _, err := p.Plan.slotOf("worker3", names.Slug(repo)); err != nil || i != 3 {
		t.Errorf("slotOf(worker3) = %d, %v", i, err)
	}
}

func TestDescribeWorkers(t *testing.T) {
	same := []workerSpec{{Kind: "claude", Model: "sonnet"}, {Kind: "claude", Model: "sonnet"}}
	if got := describeWorkers(same); got != "claude sonnet" {
		t.Errorf("describeWorkers(same) = %q", got)
	}
	mixed := []workerSpec{{Kind: "claude", Model: "opus", Role: "planner", Rank: 1}, {Kind: "codex", Role: "worker", Rank: 1}}
	if got := describeWorkers(mixed); got != "planner1 claude opus, worker1 codex" {
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
