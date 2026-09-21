package names

import (
	"regexp"
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
			w := Worker(slug, i)
			if !herdrNamePattern.MatchString(w) {
				t.Errorf("Worker(Slug(%q), %d) = %q, not a valid herdr agent name", repo, i, w)
			}
		}
	}
}

func TestWorkerNamesDistinctByIndex(t *testing.T) {
	slug := Slug("/repo")
	if Worker(slug, 1) == Worker(slug, 2) {
		t.Fatal("Worker(slug, 1) == Worker(slug, 2)")
	}
}
