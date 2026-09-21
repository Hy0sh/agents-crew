package preflight

import (
	"strings"
	"testing"
)

func TestCheckReportsEveryMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty directory: nothing resolves

	err := Check()
	if err == nil {
		t.Fatal("Check() = nil, want an error when herdr and claude are both missing")
	}
	for _, want := range []string{"herdr", "claude", "herdr.dev", "npm install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check() error = %q, missing %q", err.Error(), want)
		}
	}
}

func TestWarnIfWtmMissingIsNonFatal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var got string
	WarnIfWtmMissing(func(format string, a ...any) { got = format })
	if !strings.Contains(got, "wtm") {
		t.Errorf("expected a wtm-mentioning note, got %q", got)
	}
}
