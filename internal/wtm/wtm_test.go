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
