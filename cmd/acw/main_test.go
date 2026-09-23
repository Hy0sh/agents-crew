package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/config"
)

func TestReadNotesRelativePathIsReadFromTheRepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "rules.md"), []byte("- règle"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if got := readNotes(&out, repo, "rules.md"); got != "- règle" {
		t.Errorf("readNotes() = %q; a relative path must be read from the repo, whatever the process cwd", got)
	}
}

func TestReadNotesMissingFileWarnsAndGoesOn(t *testing.T) {
	var out bytes.Buffer
	if got := readNotes(&out, t.TempDir(), "absent.md"); got != "" {
		t.Errorf("readNotes() = %q, want empty", got)
	}
	if out.Len() == 0 {
		t.Error("a configured notes file that can't be read must warn")
	}
}

func TestApplyConfigFlagWinsOverConfig(t *testing.T) {
	workers, workerModel, masterModel, profile := 5, "haiku", "", "light"
	project := &config.Project{Workers: &workers, WorkerModel: &workerModel, MasterModel: &masterModel, Profile: &profile}
	// --worker-model given explicitly, with the built-in default's value.
	opts := &startOptions{workers: 3, workerModel: "sonnet", masterModel: "opus", workerKind: "claude"}
	given := map[string]bool{"worker-model": true}

	applyConfig(opts, project, func(name string) bool { return given[name] })

	if opts.workerModel != "sonnet" {
		t.Errorf("workerModel = %q; a flag given on the command line must win, even at its default value", opts.workerModel)
	}
	if opts.workers != 5 || opts.profile != "light" {
		t.Errorf("workers = %d, profile = %q; config should apply where no flag was given", opts.workers, opts.profile)
	}
	if opts.masterModel != "" {
		t.Errorf(`masterModel = %q; a config "" must override the built-in default`, opts.masterModel)
	}
	if opts.workerKind != "claude" {
		t.Errorf("workerKind = %q; a key absent from the config must leave the default alone", opts.workerKind)
	}
}
