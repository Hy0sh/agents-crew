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

func TestSummaryListsOnlySetKeys(t *testing.T) {
	workers, profile := 4, "light"
	got := (&Project{Workers: &workers, Profile: &profile}).Summary()
	if got != `workers=4, profile="light"` {
		t.Errorf("Summary() = %q", got)
	}
}
