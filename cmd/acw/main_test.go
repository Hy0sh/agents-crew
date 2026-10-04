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

// silence-minutes has no flag: the config sets it, the default stays
// otherwise.
func TestApplyConfigSilenceMinutes(t *testing.T) {
	minutes := 45
	opts := &startOptions{silenceMinutes: 30}
	applyConfig(opts, &config.Project{SilenceMinutes: &minutes}, func(string) bool { return false })
	if opts.silenceMinutes != 45 {
		t.Errorf("silenceMinutes = %d, want the config's 45", opts.silenceMinutes)
	}
	opts = &startOptions{silenceMinutes: 30}
	applyConfig(opts, &config.Project{}, func(string) bool { return false })
	if opts.silenceMinutes != 30 {
		t.Errorf("silenceMinutes = %d, want the default 30 with no key", opts.silenceMinutes)
	}
}

func TestCheckCounts(t *testing.T) {
	for _, c := range []struct {
		workers, min, idle int
		ok                 bool
	}{
		{3, 0, 10, true},
		{3, 3, 0, true},
		{0, 0, 10, false},
		{-1, 0, 10, false},
		{3, 4, 10, false},
		{3, -1, 10, false},
		{3, 0, -1, false},
	} {
		err := checkCounts(&startOptions{workers: c.workers, minWorkers: c.min, idleCloseMinutes: c.idle})
		if (err == nil) != c.ok {
			t.Errorf("checkCounts(workers %d, min %d, idle %d) = %v, want ok %t", c.workers, c.min, c.idle, err, c.ok)
		}
	}
}

func TestApplyConfigPoolKeys(t *testing.T) {
	min, idle := 2, 5
	opts := &startOptions{idleCloseMinutes: 10}
	applyConfig(opts, &config.Project{MinWorkers: &min, IdleCloseMinutes: &idle}, func(string) bool { return false })
	if opts.minWorkers != 2 || opts.idleCloseMinutes != 5 {
		t.Errorf("minWorkers, idleCloseMinutes = %d, %d; want the config's 2, 5", opts.minWorkers, opts.idleCloseMinutes)
	}
}

func TestApplyConfigPRWatch(t *testing.T) {
	on := true
	opts := &startOptions{}
	applyConfig(opts, &config.Project{PRWatch: &on}, func(string) bool { return false })
	if !opts.prWatch {
		t.Error("prWatch = false, want the config's true")
	}
	opts = &startOptions{}
	applyConfig(opts, &config.Project{PRWatch: &on}, func(name string) bool { return name == "pr-watch" })
	if opts.prWatch {
		t.Error("prWatch = true; --pr-watch=false given on the command line must win")
	}
}
