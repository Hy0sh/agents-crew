package main

import (
	"bytes"
	"fmt"
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

func TestWriteSystemPrompts(t *testing.T) {
	dir := t.TempDir()
	workers := []workerSpec{
		{Kind: "claude"},
		{Kind: "claude", Prompt: "You verify.", PromptPath: "/cfg/verifier.md"},
		{Kind: "codex", Prompt: "You verify.", PromptPath: "/cfg/verifier.md"},
	}
	if err := writeSystemPrompts(dir, "- rule one\n", workers); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"- rule one", "- rule one\n\nYou verify."} {
		path := filepath.Join(dir, fmt.Sprintf("worker%d.system.md", i+1))
		content, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(content), want) {
			t.Errorf("worker%d system prompt = %q, %v; want it to contain %q", i+1, content, err, want)
		}
		if workers[i].PromptPath != path {
			t.Errorf("worker%d PromptPath = %q, want %q", i+1, workers[i].PromptPath, path)
		}
	}
	// A codex worker has no system prompt acw can set: left as configured.
	if workers[2].PromptPath != "/cfg/verifier.md" {
		t.Errorf("codex PromptPath = %q, want it untouched", workers[2].PromptPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "worker3.system.md")); err == nil {
		t.Error("a codex worker got a system prompt file")
	}
}

func TestWriteSystemPromptsWithNothingToSay(t *testing.T) {
	dir := t.TempDir()
	workers := []workerSpec{{Kind: "claude"}}
	if err := writeSystemPrompts(dir, "  \n", workers); err != nil {
		t.Fatal(err)
	}
	if workers[0].PromptPath != "" {
		t.Errorf("PromptPath = %q with no notes and no prompt, want none", workers[0].PromptPath)
	}
}
