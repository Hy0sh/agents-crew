package names

import (
	"regexp"
	"strings"
	"testing"
)

var herdrNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func TestSlugIsDeterministic(t *testing.T) {
	path := "/repo/a"
	first, second := Slug(path), Slug(path)
	if first != second {
		t.Fatal("Slug is not deterministic for the same path")
	}
}

func TestSlugDiffersForDifferentPathsSameBasename(t *testing.T) {
	a, b := Slug("/home/alice/gallia-utopia"), Slug("/home/bob/gallia-utopia")
	if a == b {
		t.Fatalf("Slug(%q) == Slug(%q) == %q, want different slugs for different full paths sharing a basename", "/home/alice/gallia-utopia", "/home/bob/gallia-utopia", a)
	}
}

func TestMasterAndWorkerNamesAreHerdrSafe(t *testing.T) {
	cases := []string{
		"/repo",
		"/Users/francois/dev/projects/gallia-utopia",
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
			w := Worker(slug, i)
			if !herdrNamePattern.MatchString(w) {
				t.Errorf("Worker(Slug(%q), %d) = %q, not a valid herdr agent name", repo, i, w)
			}
		}
	}
}

func TestLabelCarriesTheRepoDirectory(t *testing.T) {
	got := Label("/Users/francois/dev/projects/gallia-utopia")
	if !strings.Contains(got, "gallia-utopia") {
		t.Errorf("Label() = %q, should name the repo directory — it is the only place the swarm's project is displayed", got)
	}
	if !strings.HasPrefix(got, "acw") {
		t.Errorf("Label() = %q, should stay recognizable among other Herdr workspaces", got)
	}
}

func TestWorkerNamesDistinctByIndex(t *testing.T) {
	slug := Slug("/repo")
	if Worker(slug, 1) == Worker(slug, 2) {
		t.Fatal("Worker(slug, 1) == Worker(slug, 2)")
	}
}
