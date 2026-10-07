package wtm

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAdoptArgs(t *testing.T) {
	if got := adoptArgs(""); !slices.Equal(got, []string{"adopt", "-y"}) {
		t.Errorf(`adoptArgs("") = %v, want the whole stack`, got)
	}
	if got := adoptArgs("light"); !slices.Equal(got, []string{"adopt", "-y", "--profile", "light"}) {
		t.Errorf(`adoptArgs("light") = %v`, got)
	}
}

func TestStartArgs(t *testing.T) {
	if got := startArgs("agents/worker1-x", ""); !slices.Equal(got, []string{"start", "agents/worker1-x"}) {
		t.Errorf(`startArgs(no profile) = %v, want the whole stack`, got)
	}
	if got := startArgs("agents/worker1-x", "light"); !slices.Equal(got, []string{"start", "agents/worker1-x", "--profile", "light"}) {
		t.Errorf(`startArgs("light") = %v`, got)
	}
}

func TestSwitchArgs(t *testing.T) {
	for _, c := range []struct {
		branch, from, profile string
		want                  []string
	}{
		{"feat/x", "", "", []string{"switch", "feat/x"}},
		{"feat/x", "origin/main", "", []string{"switch", "feat/x", "--from", "origin/main"}},
		{"feat/x", "origin/main", "light", []string{"switch", "feat/x", "--from", "origin/main", "--profile", "light"}},
	} {
		if got := switchArgs(c.branch, c.from, c.profile); !slices.Equal(got, c.want) {
			t.Errorf("switchArgs(%q, %q, %q) = %v, want %v", c.branch, c.from, c.profile, got, c.want)
		}
	}
}

// A repo wtm doesn't know gets no stack: its workers must not be told
// about wtm switch, which would refuse every time.
func TestListedProject(t *testing.T) {
	list := "NAME     DIRECTORY                     BASE     DUMP\n" +
		"shop     /Users/me/dev/some-repo       main     yes\n" +
		"other    /Users/me/dev/some-repo-2     develop  no\n"
	if !listedProject(list, "/Users/me/dev/some-repo") {
		t.Error("listedProject(registered dir) = false")
	}
	for _, dir := range []string{"/Users/me/dev/some", "/Users/me/dev/some-repo/.claude/worktrees/x", "/elsewhere"} {
		if listedProject(list, dir) {
			t.Errorf("listedProject(%q) = true, want false", dir)
		}
	}
}

// wtm compares paths resolved: a repo reached through a symlink, or
// written with a trailing slash, is still the registered one.
func TestListedProjectComparesResolvedPaths(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	list := "shop     " + real + "       main     yes\n"
	for _, dir := range []string{link, real + "/"} {
		if !listedProject(list, dir) {
			t.Errorf("listedProject(%q) = false, want the project registered as %s", dir, real)
		}
	}
}

// A worktree wtm lists as adoptable has no stack; any other status (up,
// down, or - when docker did not answer) is one with a stack, and so is a
// worktree that changed branch outside wtm, listed under its stack's.
func TestAdoptablePaths(t *testing.T) {
	list := "INDEX  BRANCH                       COMPOSE PROJECT          STATUS     PATH\n" +
		"-      feat/one                     -                        adoptable  /r/.claude/worktrees/worker1-a\n" +
		"5      agents/worker2-b             r-wt-5-agents-worker2-b  up         /r/.claude/worktrees/worker2-b\n" +
		"6      agents/worker3-c (now on x)  r-wt-6-agents-worker3-c  down       /r/.claude/worktrees/worker3-c\n" +
		"7      agents/worker4-d             r-wt-7-agents-worker4-d  -          /r/.claude/worktrees/worker4-d\n" +
		"1 left to adopt: `wtm adopt <branch>` gives one a stack where it stands\n"
	got := adoptablePaths(list)
	if len(got) != 1 || !got["/r/.claude/worktrees/worker1-a"] {
		t.Errorf("adoptablePaths() = %v, want only worker1-a", got)
	}
}
