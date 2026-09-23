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
