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
