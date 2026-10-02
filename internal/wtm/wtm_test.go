package wtm

import (
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
