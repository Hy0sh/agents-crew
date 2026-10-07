package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/config"
)

// Flags set only the keys given, and a file key points at a copy next to
// the config, not at the file given.
func TestProjectCreateFromFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo, src := t.TempDir(), filepath.Join(t.TempDir(), "rules.md")
	os.WriteFile(src, []byte("no docker"), 0o644)

	cmd := projectWriteCommand()
	cmd.SetArgs([]string{repo, "--workers", "4", "--worker-model", "", "--pr-watch", "--notes", src})
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(repo)
	if err != nil || p == nil {
		t.Fatalf("Load = %v, %v", p, err)
	}
	if *p.Workers != 4 || *p.WorkerModel != "" || !*p.PRWatch || p.MasterKind != nil {
		t.Errorf("entry = %s", p.Summary())
	}
	if filepath.Dir(*p.Notes) == filepath.Dir(src) {
		t.Fatalf("notes = %s, the file given and not a copy", *p.Notes)
	}
	if got, _ := os.ReadFile(*p.Notes); string(got) != "no docker" {
		t.Errorf("copy holds %q", got)
	}
}

// Enter keeps, - removes, a wrong value is asked again, and the end of the
// input keeps the rest.
func TestStepThrough(t *testing.T) {
	two, opus := 2, "opus"
	p := &config.Project{Workers: &two, MasterModel: &opus}
	// workers, min-workers, then idle-close-minutes to worker-kind kept,
	// master-model removed.
	in := strings.NewReader("x\n5\n\n" + strings.Repeat("\n", 4) + "-\n")
	out := &bytes.Buffer{}
	if err := stepThrough(p, t.TempDir(), in, out); err != nil {
		t.Fatal(err)
	}
	if *p.Workers != 5 || p.MinWorkers != nil || p.MasterModel != nil {
		t.Errorf("entry = %s", p.Summary())
	}
	if !strings.Contains(out.String(), `"x" is not a number`) {
		t.Errorf("no complaint about x:\n%s", out)
	}
}
