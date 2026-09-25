package config

import (
	"os"
	"path/filepath"
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

func TestLoadWorkerOverrides(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"worker-overrides": {
		"1": {"model": "opus", "prompt": "~/planner.md"},
		"3": {"kind": "codex", "model": ""}
	}}}}`)
	p, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if got := *p.WorkerOverrides["1"].Prompt; got != filepath.Join(home, "planner.md") {
		t.Errorf("prompt = %q, ~ not expanded", got)
	}
	three := p.WorkerOverrides["3"]
	if *three.Kind != "codex" || three.Model == nil || *three.Model != "" || three.Prompt != nil {
		t.Errorf(`override 3 = %+v; want kind codex, model set to "", no prompt`, three)
	}
}

func TestLoadRejectsUnknownKeyInWorkerOverride(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {"worker-overrides": {"1": {"modle": "opus"}}}}}`)
	_, err := Load("/repo")
	if err == nil || !strings.Contains(err.Error(), "modle") {
		t.Errorf("Load() error = %v; want one naming the unknown key", err)
	}
}

func TestWithPresetReplacesWholeKeys(t *testing.T) {
	writeConfig(t, `{"projects": {"/repo": {
		"workers": 3, "profile": "light",
		"worker-overrides": {"2": {"model": "haiku"}, "3": {"model": "haiku"}},
		"presets": {"feature": {"workers": 4, "worker-overrides": {"1": {"prompt": "~/planner.md"}}}}
	}}}`)
	p, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.WithPreset("feature")
	if err != nil {
		t.Fatal(err)
	}
	if *got.Workers != 4 || *got.Profile != "light" {
		t.Errorf("workers = %d, profile = %q; want the preset's workers and the entry's profile", *got.Workers, *got.Profile)
	}
	if len(got.WorkerOverrides) != 1 || got.WorkerOverrides["1"].Prompt == nil {
		t.Errorf("worker-overrides = %v; the preset's must replace the entry's, not merge index by index", got.WorkerOverrides)
	}
	home, _ := os.UserHomeDir()
	if *got.WorkerOverrides["1"].Prompt != filepath.Join(home, "planner.md") {
		t.Errorf("prompt = %q, ~ not expanded in a preset", *got.WorkerOverrides["1"].Prompt)
	}
	if *p.Workers != 3 {
		t.Error("WithPreset must not modify the entry it is called on")
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
		"worker-overrides": {"1": {"dir": "~/docs"}},
		"presets": {"p": {"master-dir": "~/other"}}
	}}}`)
	p, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if *p.MasterDir != filepath.Join(home, "studio") || *p.WorkerOverrides["1"].Dir != filepath.Join(home, "docs") {
		t.Errorf("master-dir = %q, dir = %q; ~ not expanded", *p.MasterDir, *p.WorkerOverrides["1"].Dir)
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

func TestSummaryListsWorkerOverrideIndexes(t *testing.T) {
	p := &Project{WorkerOverrides: map[string]WorkerOverride{"3": {}, "1": {}}}
	if got := p.Summary(); got != "worker-overrides=1,3" {
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
