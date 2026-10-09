package names

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var herdrNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// A role names its instances: role + rank. Lowercase letters and dashes,
// no final digit (the rank follows), not master, short enough that
// <role><rank>-<slug> stays within herdr's 32 characters.
func TestRoleNames(t *testing.T) {
	for _, ok := range []string{"worker", "reviewer", "front-end", "a", "abcdefghijklmnopqrst"} {
		if !ValidRole(ok) {
			t.Errorf("ValidRole(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "master", "Reviewer", "rev1", "-x", "x-", "re view", "rév", "abcdefghijklmnopqrstu"} {
		if ValidRole(bad) {
			t.Errorf("ValidRole(%q) = true", bad)
		}
	}
	for _, ok := range []string{"worker1", "reviewer12", "front-end3"} {
		if !IsWorkerLabel(ok) {
			t.Errorf("IsWorkerLabel(%q) = false", ok)
		}
	}
	for _, bad := range []string{"worker", "worker0", "worker01", "master1", "1", "reviewer-2"} {
		if IsWorkerLabel(bad) {
			t.Errorf("IsWorkerLabel(%q) = true", bad)
		}
	}
	if !IsWorkerWorktree("reviewer1-20261009183715") || !IsWorkerWorktree("front-end3-20261009183715") || IsWorkerWorktree("reviewer-20261009183715") {
		t.Error("IsWorkerWorktree")
	}
	// A worktree acw didn't make, named like one but without its stamp,
	// is never a worker's: acw stop would remove it.
	for _, mine := range []string{"fix1-login", "pr12-review", "issue42-x", "reviewer1-2026"} {
		if IsWorkerWorktree(mine) {
			t.Errorf("IsWorkerWorktree(%q) = true", mine)
		}
	}
	if !IsWorkerAgent("reviewer2-aa2ce4", "aa2ce4") || IsWorkerAgent("master-aa2ce4", "aa2ce4") || IsWorkerAgent("reviewer2-ffffff", "aa2ce4") {
		t.Error("IsWorkerAgent")
	}
	if Agent("aa2ce4", "reviewer2") != "reviewer2-aa2ce4" || WorkerBranch("reviewer2", "s") != "agents/reviewer2-s" {
		t.Error("Agent/WorkerBranch")
	}
	if a := Agent(Slug("/some/repo"), "abcdefghijklmnopqrst99"); !herdrNamePattern.MatchString(a) {
		t.Errorf("longest name %q is not a valid herdr agent name", a)
	}
}

// What provisioning names, teardown must recognize: a mismatch leaves
// real stacks orphaned by an `acw stop` that reports success.
func TestIsWorkerWorktreeMatchesWhatWorkerWorktreeMakes(t *testing.T) {
	if name := filepath.Base(WorkerWorktree("/repo", "worker12", "20260921181008")); !IsWorkerWorktree(name) {
		t.Errorf("IsWorkerWorktree(%q) = false for a name WorkerWorktree made", name)
	}
	for _, name := range []string{"worker-nostamp", "worker1", "master", "synchronous-nibbling", ".acw-status"} {
		if IsWorkerWorktree(name) {
			t.Errorf("IsWorkerWorktree(%q) = true", name)
		}
	}
}

func TestSlugIsDeterministic(t *testing.T) {
	path := "/repo/a"
	first, second := Slug(path), Slug(path)
	if first != second {
		t.Fatal("Slug is not deterministic for the same path")
	}
}

func TestSlugDiffersForDifferentPathsSameBasename(t *testing.T) {
	a, b := Slug("/home/alice/shop-frontend"), Slug("/home/bob/shop-frontend")
	if a == b {
		t.Fatalf("Slug(%q) == Slug(%q) == %q, want different slugs for different full paths sharing a basename", "/home/alice/shop-frontend", "/home/bob/shop-frontend", a)
	}
}

func TestMasterAndWorkerNamesAreHerdrSafe(t *testing.T) {
	cases := []string{
		"/repo",
		"/Users/francois/dev/projects/shop-frontend",
		"/tmp/UPPER Case With Spaces!!",
		"/",
	}
	for _, repo := range cases {
		slug := Slug(repo)
		master := Master(slug)
		if !herdrNamePattern.MatchString(master) {
			t.Errorf("Master(Slug(%q)) = %q, not a valid herdr agent name", repo, master)
		}
		for i := 1; i <= 9; i++ {
			w := Agent(slug, fmt.Sprintf("worker%d", i))
			if !herdrNamePattern.MatchString(w) {
				t.Errorf("Agent(Slug(%q), worker%d) = %q, not a valid herdr agent name", repo, i, w)
			}
		}
	}
}

func TestLabelCarriesTheRepoDirectory(t *testing.T) {
	got := Label("/Users/francois/dev/projects/shop-frontend")
	if !strings.Contains(got, "shop-frontend") {
		t.Errorf("Label() = %q, should name the repo directory — it is the only place the swarm's project is displayed", got)
	}
	if !strings.HasPrefix(got, "acw") {
		t.Errorf("Label() = %q, should stay recognizable among other Herdr workspaces", got)
	}
}

func TestWorkerNamesDistinctByIndex(t *testing.T) {
	slug := Slug("/repo")
	if Agent(slug, "worker1") == Agent(slug, "worker2") {
		t.Fatal("Agent(slug, worker1) == Agent(slug, worker2)")
	}
}
