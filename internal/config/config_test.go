package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeConfig points XDG_CONFIG_HOME at a temp dir holding content, and
// returns nothing to clean: t.Setenv and t.TempDir undo themselves.
func writeConfig(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "acw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acw", "config.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := Path(); got != "/xdg/acw/config.json" {
		t.Errorf("Path() = %q", got)
	}
}

func TestLoadWithoutFileIsNotAnError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := Load("/repo")
	if p != nil || err != nil {
		t.Errorf("Load() = %v, %v; want nil, nil", p, err)
	}
}

func TestLoadWithoutEntryForRepo(t *testing.T) {
	writeConfig(t, `{"projects": {"/other": {"workers": 2}}}`)
	p, err := Load("/repo")
	if p != nil || err != nil {
		t.Errorf("Load() = %v, %v; want nil, nil", p, err)
	}
}

func TestLoadFullEntry(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo/": {
		"workers": 4, "max-stacks": 3, "master-kind": "claude", "worker-kind": "codex",
		"master-model": "opus", "worker-model": "", "brief": "/b.md", "profile": "light", "notes": "~/n.md"
	}}}`)
	p, err := Load("/repo")
	if err != nil || p == nil {
		t.Fatalf("Load() = %v, %v", p, err)
	}
	if *p.Workers != 4 || *p.MaxStacks != 3 || *p.WorkerKind != "codex" || *p.Profile != "light" {
		t.Errorf("Load() = %+v", p)
	}
	if p.WorkerModel == nil || *p.WorkerModel != "" {
		t.Errorf(`"worker-model": "" must load as a set empty string, not absent`)
	}
	home, _ := os.UserHomeDir()
	if *p.Notes != filepath.Join(home, "n.md") {
		t.Errorf("notes = %q, ~ not expanded", *p.Notes)
	}
}

func TestLoadAbsentKeyStaysNil(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"profile": "light"}}}`)
	p, _ := Load("/repo")
	if p.WorkerModel != nil || p.Workers != nil {
		t.Errorf("absent keys must stay nil so the built-in default applies: %+v", p)
	}
}

func TestLoadExpandsHomeInKeys(t *testing.T) {
	home, _ := os.UserHomeDir()
	writeConfig(t, `{"projects": {"~/dev/repo": {"workers": 2}}}`)
	p, err := Load(filepath.Join(home, "dev", "repo"))
	if err != nil || p == nil {
		t.Fatalf("Load() = %v, %v; a ~ key should match the expanded path", p, err)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"worker_model": "haiku"}}}`)
	_, err := Load("/repo")
	if err == nil || !strings.Contains(err.Error(), "worker_model") {
		t.Errorf("Load() error = %v; want one naming the unknown key", err)
	}
}

func TestLoadRejectsInvalidJSON(t *testing.T) {
	writeConfig(t, `{"projects": `)
	if _, err := Load("/repo"); err == nil {
		t.Error("Load() on invalid JSON = nil error, want one")
	}
}

func TestLoadRoles(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"roles": {
		"worker": {"max": 3, "min": 1},
		"planner": {"max": 1, "model": "opus", "prompt": "~/planner.md"},
		"codexer": {"max": 2, "kind": "codex", "model": ""}
	}}}}`)
	p, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range p.Roles {
		order = append(order, r.Name)
	}
	if !slices.Equal(order, []string{"worker", "planner", "codexer"}) {
		t.Errorf("roles in order %v, want as written", order)
	}
	home, _ := os.UserHomeDir()
	if got := *p.Roles[1].Prompt; got != filepath.Join(home, "planner.md") {
		t.Errorf("prompt = %q, ~ not expanded", got)
	}
	codexer := p.Roles[2]
	if *codexer.Kind != "codex" || codexer.Model == nil || *codexer.Model != "" || codexer.Prompt != nil {
		t.Errorf(`codexer = %+v; want kind codex, model set to "", no prompt`, codexer)
	}
}

func TestLoadRejectsUnknownKeyInRole(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"roles": {"worker": {"max": 1, "modle": "opus"}}}}}`)
	_, err := Load("/repo")
	if err == nil || !strings.Contains(err.Error(), "modle") {
		t.Errorf("Load() error = %v; want one naming the unknown key", err)
	}
}

// What roles can't mean is refused, each with what to write instead.
// The defaults get every key acw has a value for, keep the user's own
// values, lose the keys acw doesn't know; the projects are left as they
// were. A second run has nothing to do.
func TestSyncDefaults(t *testing.T) {
	writeConfig(t, `{"defaults":{"worker-model":"haiku","old-key":1},"projects":{"/repo":{"workers":2}}}`)
	added, dropped, err := SyncDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(added, "master-model") || slices.Contains(added, "worker-model") || !slices.Equal(dropped, []string{"old-key"}) {
		t.Errorf("added %v, dropped %v; want the missing keys added, worker-model kept, old-key dropped", added, dropped)
	}
	p, err := Effective("/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if *p.WorkerModel != "haiku" || *p.MasterModel != "opus" || *p.Workers != 2 || *p.SilenceMinutes != 30 {
		t.Errorf("effective = worker-model %s, master-model %s, workers %d, silence %d", *p.WorkerModel, *p.MasterModel, *p.Workers, *p.SilenceMinutes)
	}
	if added, dropped, err := SyncDefaults(); err != nil || added != nil || dropped != nil {
		t.Errorf("second sync = %v, %v, %v; want nothing to do", added, dropped, err)
	}
	// A repo with no entry runs on the defaults alone.
	if p, err := Effective("/other", ""); err != nil || *p.Workers != 3 {
		t.Errorf("no entry = %+v, %v; want the defaults", p, err)
	}
}

// Roles declared in the defaults reach every repo, an entry's role of the
// same name replacing its default; an entry that counts workers with
// workers drops them.
func TestDefaultRolesMergeByName(t *testing.T) {
	writeConfig(t, `{"defaults":{"roles":{"worker":{"max":3},"chore":{"max":1,"model":"haiku","tasks":["chore"]}}},
	 "projects":{"/a":{"roles":{"chore":{"max":2,"model":"haiku","tasks":["chore"]},"reviewer":{"max":1,"tasks":["review"]}}},"/b":{"workers":2},"/c":{}}}`)
	if _, _, err := SyncDefaults(); err != nil {
		t.Fatal(err)
	}
	a, err := Effective("/a", "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range a.Roles {
		got = append(got, fmt.Sprintf("%s:%d", r.Name, *r.Max))
	}
	if !slices.Equal(got, []string{"worker:3", "chore:2", "reviewer:1"}) || a.Workers != nil {
		t.Errorf("/a roles = %v, workers %v; want worker:3 chore:2 reviewer:1 and no workers", got, a.Workers)
	}
	if b, err := Effective("/b", ""); err != nil || b.Roles != nil || *b.Workers != 2 {
		t.Errorf("/b = %+v, %v; want workers 2 and no roles", b, err)
	}
	if c, err := Effective("/c", ""); err != nil || len(c.Roles) != 2 || c.Workers != nil {
		t.Errorf("/c = %+v, %v; want the default roles", c, err)
	}
}

// A kind acw doesn't know is refused wherever it is written, with the key
// that holds it; a preset is checked once laid over the entry.
func TestUnknownKindRefused(t *testing.T) {
	for key, raw := range map[string]string{
		"master-kind":         `{"master-kind":"claud"}`,
		"worker-kind":         `{"worker-kind":"gpt"}`,
		"roles: review: kind": `{"roles":{"review":{"max":1,"kind":"x"}}}`,
	} {
		writeConfig(t, `{"projects": {"/repo": `+raw+`}}`)
		if _, err := Load("/repo"); err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "claude, pi, codex") {
			t.Errorf("%s: Load() error = %v; want it named, with the known kinds", key, err)
		}
	}
	writeConfig(t, `{"projects": {"/repo": {"worker-kind":"codex","presets":{"p":{"master-kind":"nope"}}}}}`)
	p, err := Load("/repo")
	if err != nil {
		t.Fatalf("Load() = %v; a known kind and an unused preset are fine", err)
	}
	if _, err := p.WithPreset("p"); err == nil {
		t.Error("a preset with an unknown master-kind was accepted")
	}
}

func TestRolesRefuseBadOnes(t *testing.T) {
	for name, raw := range map[string]string{
		"max absent":   `{"roles":{"worker":{}}}`,
		"max 0":        `{"roles":{"worker":{"max":0}}}`,
		"min > max":    `{"roles":{"worker":{"max":1,"min":2}}}`,
		"bad name":     `{"roles":{"rev1":{"max":1}}}`,
		"master":       `{"roles":{"master":{"max":1}}}`,
		"profile+dir":  `{"roles":{"a":{"max":1,"dir":"/d","profile":"api"}}}`,
		"with workers": `{"workers":3,"roles":{"worker":{"max":1}}}`,
		"with min":     `{"min-workers":1,"roles":{"worker":{"max":1}}}`,
		"overrides":    `{"worker-overrides":{"4":{"model":"opus","tasks":["need-review"]}}}`,
	} {
		writeConfig(t, `{"projects": {"/repo": `+raw+`}}`)
		if _, err := Load("/repo"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	writeConfig(t, `{"projects": {"/repo": {"roles":{"worker":{"max":1,"keep":true}}}}}`)
	if _, err := Load("/repo"); err == nil {
		t.Error("unknown role key accepted")
	}
}

// worker-overrides is gone; its refusal shows the roles to write.
func TestOverridesRefusalShowsRoles(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"workers": 7, "min-workers": 3, "worker-overrides": {"4": {"model": "opus", "tasks": ["need-review"], "keep": true}}}}}`)
	_, err := Load("/repo")
	if err == nil || !strings.Contains(err.Error(), `"roles"`) || !strings.Contains(err.Error(), "need-review") || !strings.Contains(err.Error(), `"max": 6`) {
		t.Errorf("err = %v, want the equivalent roles", err)
	}
}

// Roles written back by Edit keep their order.
func TestRolesMarshalInOrder(t *testing.T) {
	var p Project
	if err := json.Unmarshal([]byte(`{"roles":{"worker":{"max":4},"reviewer":{"max":2},"analyst":{"max":1}}}`), &p); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(p.Roles)
	if err != nil || !strings.HasPrefix(string(out), `{"worker":`) || !strings.Contains(string(out), `"reviewer":{"max":2},"analyst"`) {
		t.Errorf("marshal = %s, %v", out, err)
	}
}

func TestWithPresetReplacesWholeKeys(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {
		"profile": "light",
		"roles": {"worker": {"max": 2}, "reviewer": {"max": 1, "model": "haiku"}},
		"presets": {"feature": {"roles": {"planner": {"max": 1, "prompt": "~/planner.md"}}}, "mixed": {"workers": 5}}
	}}}`)
	p, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.WithPreset("feature")
	if err != nil {
		t.Fatal(err)
	}
	if *got.Profile != "light" {
		t.Errorf("profile = %q; want the entry's", *got.Profile)
	}
	if len(got.Roles) != 1 || got.Roles[0].Name != "planner" {
		t.Errorf("roles = %v; the preset's must replace the entry's, not merge role by role", got.Roles)
	}
	home, _ := os.UserHomeDir()
	if *got.Roles[0].Prompt != filepath.Join(home, "planner.md") {
		t.Errorf("prompt = %q, ~ not expanded in a preset", *got.Roles[0].Prompt)
	}
	if len(p.Roles) != 2 {
		t.Error("WithPreset must not modify the entry it is called on")
	}
	// A preset's count replaces the entry's, whichever way it is written.
	mixed, err := p.WithPreset("mixed")
	if err != nil || mixed.Roles != nil || *mixed.Workers != 5 {
		t.Errorf("preset workers over entry roles = %+v, %v; want workers 5, no roles", mixed, err)
	}
	writeConfig(t, `{"projects": {"/repo": {"workers": 4, "min-workers": 2, "presets": {"r": {"roles": {"worker": {"max": 1}}}}}}}`)
	q, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if r, err := q.WithPreset("r"); err != nil || r.Workers != nil || r.MinWorkers != nil || len(r.Roles) != 1 {
		t.Errorf("preset roles over entry workers = %+v, %v; want the roles only", r, err)
	}
}

func TestWithPresetUnknownNamesTheAvailableOnes(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"presets": {"review": {}, "feature": {}}}}}`)
	p, _ := Load("/repo")
	_, err := p.WithPreset("featur")
	if err == nil || !strings.Contains(err.Error(), "feature, review") {
		t.Errorf("WithPreset() error = %v; want one listing feature, review", err)
	}
}

func TestLoadAgentDirs(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {
		"master-dir": "~/studio",
		"roles": {"analyst": {"max": 1, "dir": "~/docs"}},
		"presets": {"p": {"master-dir": "~/other"}}
	}}}`)
	p, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if *p.MasterDir != filepath.Join(home, "studio") || *p.Roles[0].Dir != filepath.Join(home, "docs") {
		t.Errorf("master-dir = %q, dir = %q; ~ not expanded", *p.MasterDir, *p.Roles[0].Dir)
	}
	if got := *p.Presets["p"].MasterDir; got != filepath.Join(home, "other") {
		t.Errorf("preset master-dir = %q, ~ not expanded", got)
	}
	if !strings.Contains(p.Summary(), `master-dir="`+filepath.Join(home, "studio")+`"`) {
		t.Errorf("Summary() = %q, missing master-dir", p.Summary())
	}
}

func TestLoadRejectsNestedPreset(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"presets": {"a": {"presets": {"b": {}}}}}}}`)
	if _, err := Load("/repo"); err == nil {
		t.Error("Load() with a preset inside a preset = nil error, want one")
	}
}

func TestSummaryListsRoles(t *testing.T) {
	four, two := 4, 2
	p := &Project{Roles: Roles{{Name: "worker", Role: Role{Max: &four}}, {Name: "reviewer", Role: Role{Max: &two}}}}
	if got := p.Summary(); got != "roles=worker:4,reviewer:2" {
		t.Errorf("Summary() = %q", got)
	}
}

func TestSummaryListsOnlySetKeys(t *testing.T) {
	workers, profile := 4, "light"
	got := (&Project{Workers: &workers, Profile: &profile}).Summary()
	if got != `workers=4, profile="light"` {
		t.Errorf("Summary() = %q", got)
	}
}

func TestLoadSilenceMinutesAndItsPreset(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"silence-minutes": 45, "presets": {"night": {"silence-minutes": 90}}}}}`)
	p, err := Load("/repo")
	if err != nil || p == nil || p.SilenceMinutes == nil || *p.SilenceMinutes != 45 {
		t.Fatalf("Load() = %+v, %v; want silence-minutes 45", p, err)
	}
	if got := p.Summary(); !strings.Contains(got, "silence-minutes=45") {
		t.Errorf("Summary() = %q, want silence-minutes=45", got)
	}
	night, err := p.WithPreset("night")
	if err != nil || *night.SilenceMinutes != 90 {
		t.Errorf("WithPreset(night) = %+v, %v; want silence-minutes 90", night, err)
	}
}

// A preset turns pr-watch off for one mode, e.g. a test campaign.
func TestPRWatchPresetOverridesTheEntry(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"pr-watch": true, "presets": {"test-campaign": {"pr-watch": false}}}}}`)
	p, err := Load("/repo")
	if err != nil || p == nil || p.PRWatch == nil || !*p.PRWatch {
		t.Fatalf("Load() = %+v, %v; want pr-watch true", p, err)
	}
	if !strings.Contains(p.Summary(), "pr-watch=true") {
		t.Errorf("Summary() = %q, missing pr-watch", p.Summary())
	}
	got, err := p.WithPreset("test-campaign")
	if err != nil || got.PRWatch == nil || *got.PRWatch {
		t.Errorf("WithPreset(test-campaign).PRWatch = %v, %v; want false", got.PRWatch, err)
	}
}

// Edit rewrites only the repo's entry: another one, its ~ key and its
// presets come back as written, and an unset key is not written as null.
func TestEditKeepsOtherEntries(t *testing.T) {
	writeConfig(t, `{"projects": {"~/other": {"workers": 2, "presets": {"p": {"worker-model": ""}}}, "/repo": {"notes": "~/n.md"}}}`)
	err := Edit("/repo", func(p *Project) error {
		four := 4
		p.Workers = &four
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(Path())
	got := string(content)
	for _, want := range []string{`"~/other"`, `"worker-model": ""`, `"notes": "~/n.md"`, `"workers": 4`} {
		if !strings.Contains(got, want) {
			t.Errorf("config lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "null") {
		t.Errorf("unset keys written as null:\n%s", got)
	}
}

// Edit never writes an entry acw would then refuse: workers next to
// roles is refused, and the file is left as it was.
func TestEditRefusesAnEntryLoadWouldRefuse(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"roles": {"worker": {"max": 2}}}}}`)
	before, _ := os.ReadFile(Path())
	err := Edit("/repo", func(p *Project) error {
		three := 3
		p.Workers = &three
		return nil
	})
	if err == nil {
		t.Error("Edit wrote workers next to roles")
	}
	if after, _ := os.ReadFile(Path()); string(after) != string(before) {
		t.Errorf("config changed:\n%s", after)
	}
}

// Edit makes the entry when there is none and changes it when there is
// one: the user needn't know which.
func TestEditCreatesOrChanges(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	set := func(n int) func(*Project) error {
		return func(p *Project) error { p.Workers = &n; return nil }
	}
	if err := Edit("/repo", set(2)); err != nil {
		t.Fatalf("Edit with no file: %v", err)
	}
	if err := Edit("/repo", set(5)); err != nil {
		t.Fatalf("Edit over an entry: %v", err)
	}
	if p, err := Load("/repo"); err != nil || p == nil || *p.Workers != 5 {
		t.Errorf("Load = %v, %v; want workers=5", p, err)
	}
}

// kinds merge by name with the defaults, like roles: a kind written once
// for every repo stays, an entry's kind of the same name replaces it.
func TestKindsMergeByNameWithTheDefaults(t *testing.T) {
	defaults := Project{Kinds: map[string]Kind{
		"ci-triage": {When: "a CI turns red", Prompt: "/k/ci.md"},
		"rebase":    {When: "a branch conflicts", Prompt: "/k/rebase.md"},
	}}
	entry := Project{Kinds: map[string]Kind{"rebase": {Prompt: "/repo/rebase.md"}}}
	got := withDefaults(defaults, entry).Kinds
	want := map[string]Kind{
		"ci-triage": {When: "a CI turns red", Prompt: "/k/ci.md"},
		"rebase":    {Prompt: "/repo/rebase.md"},
	}
	if !maps.Equal(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}
	if defaults.Kinds["rebase"].Prompt != "/k/rebase.md" {
		t.Error("withDefaults changed the defaults' own map")
	}
}

func TestCheckRefusesABadKindName(t *testing.T) {
	for _, name := range []string{"", "-x", "two words", "tab\there"} {
		p := Project{Kinds: map[string]Kind{name: {When: "w"}}}
		if err := p.Check(); err == nil {
			t.Errorf("Check(kind %q) = nil, want a refusal", name)
		}
	}
	p := Project{Kinds: map[string]Kind{"ci-triage": {When: "w"}}}
	if err := p.Check(); err != nil {
		t.Errorf("Check(ci-triage) = %v", err)
	}
}
