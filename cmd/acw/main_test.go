package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Hy0sh/agents-crew/internal/config"
)

func TestLoadProjectPresetWithoutEntryFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := loadProject("/repo", "feature"); err == nil {
		t.Error("loadProject() with --preset and no entry = nil error, want one")
	}
}

// Cobra lists flags only once a "-" is typed; a bare Tab on acw should
// offer them too, minus those already on the command line.
func TestCompleteFlagsOnBareTab(t *testing.T) {
	cmd := &cobra.Command{Use: "acw"}
	cmd.Flags().IntP("workers", "n", 3, "number of worker agents")
	cmd.Flags().String("preset", "", "named preset")
	if err := cmd.Flags().Set("workers", "2"); err != nil {
		t.Fatal(err)
	}

	got, _ := completeFlags(cmd, nil, "")
	if !slices.Contains(got, cobra.CompletionWithDesc("--preset", "named preset")) {
		t.Errorf("completeFlags() = %v, want --preset offered", got)
	}
	for _, c := range got {
		if strings.HasPrefix(c, "--workers") {
			t.Errorf("completeFlags() = %v, offered --workers already given", got)
		}
	}

	if got, _ := completeFlags(cmd, nil, "st"); len(got) != 0 {
		t.Errorf("completeFlags(\"st\") = %v; a started word is a subcommand, cobra completes it", got)
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
