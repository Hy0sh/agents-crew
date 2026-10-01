package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hy0sh/agents-crew/internal/brief"
)

// An extra is appended to whichever brief applies, and goes through the
// template like the rest: that's what lets a mode use {{.RepoPath}}.
func TestBriefExtraIsAppendedAndTemplated(t *testing.T) {
	if got := briefSource("", ""); got != "" {
		t.Errorf("briefSource(none) = %q, want the built-in brief untouched", got)
	}
	built, err := buildBrief(briefSource("", "TEST MODE in {{.RepoPath}}."), brief.Params{RepoPath: "/repo", Slug: "3f9a1c"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(built, brief.Build(brief.Params{RepoPath: "/repo", Slug: "3f9a1c"})[:200]) || !strings.HasSuffix(built, "TEST MODE in /repo.") {
		t.Errorf("built-in brief + extra = ...%q, want the built-in brief then the templated extra", built[len(built)-min(len(built), 120):])
	}
	if got := briefSource("custom\n", "extra"); got != "custom\n\nextra" {
		t.Errorf("briefSource(custom, extra) = %q", got)
	}
}

func TestReadNotesRelativePathIsReadFromTheRepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "rules.md"), []byte("- rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if got := readNotes(&out, repo, "rules.md"); got != "- rule" {
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
