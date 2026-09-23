package main

import (
	"testing"

	"github.com/Hy0sh/agents-crew/internal/config"
)

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
