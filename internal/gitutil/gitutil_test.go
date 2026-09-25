package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// A worker coding for 35 minutes without touching its status file is
// still working: its edits are what say so.
func TestLastActivityFollowsEditedFiles(t *testing.T) {
	dir := gitInit(t)
	later := time.Now().Add(time.Hour).Truncate(time.Second)
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if got := LastActivity(dir); !got.Equal(later) {
		t.Errorf("LastActivity() = %v, want the edited file's %v", got, later)
	}
}

// A tracked file modified in the work tree is listed as " M path", with a
// leading space that must not be trimmed away with the output's.
func TestLastActivityFollowsAModifiedTrackedFile(t *testing.T) {
	dir := gitInit(t)
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.go"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "-m", "a"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	later := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := os.WriteFile(file, []byte("package a // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if got := LastActivity(dir); !got.Equal(later) {
		t.Errorf("LastActivity() = %v, want the modified file's %v", got, later)
	}
}

func TestLastActivityCountsTheIndex(t *testing.T) {
	dir := gitInit(t)
	if got := LastActivity(dir); got.IsZero() {
		t.Error("LastActivity() of a clean repo = zero, want its index's mtime")
	}
}

func TestLastActivityOutsideGitIsZero(t *testing.T) {
	if got := LastActivity(t.TempDir()); !got.IsZero() {
		t.Errorf("LastActivity(not a repo) = %v, want zero", got)
	}
}
