package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
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
