package preflight

import (
	"strings"
	"testing"
)

func TestCheckStartReportsEveryMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty directory: nothing resolves

	err := CheckStart()
	if err == nil {
		t.Fatal("CheckStart() = nil, want an error when herdr and claude are both missing")
	}
	for _, want := range []string{"herdr", "claude", "herdr.dev", "npm install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("CheckStart() error = %q, missing %q", err.Error(), want)
		}
	}
}

func TestCheckStopOnlyRequiresHerdr(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := CheckStop()
	if err == nil {
		t.Fatal("CheckStop() = nil, want an error when herdr is missing")
	}
	if strings.Contains(err.Error(), "claude") {
		t.Errorf("CheckStop() error = %q, should not require claude", err.Error())
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
