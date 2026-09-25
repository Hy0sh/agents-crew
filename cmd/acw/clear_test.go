package main

import (
	"strings"
	"testing"
)

// The spike: right after /clear the status line comes back within a
// second with a new session_id, and its context is null rather than 0.
func TestClearedWaitsForANewSession(t *testing.T) {
	if !cleared("b9d48445", usage{SessionID: "a6025ebc"}) {
		t.Error("a new session_id is the clear having taken")
	}
	if cleared("b9d48445", usage{SessionID: "b9d48445"}) {
		t.Error("the same session is a render from before the clear (the race seen in real use)")
	}
	if cleared("b9d48445", usage{}) {
		t.Error("no session_id read proves nothing")
	}
}

func TestClearRefusesBeforeSendingAnything(t *testing.T) {
	if got := clearRefusal("worker1", "blocked", true); !strings.Contains(got, "bloqué") {
		t.Errorf("clearRefusal(blocked) = %q, want a refusal: /clear would queue behind the prompt", got)
	}
	if got := clearRefusal("worker2", "idle", false); !strings.Contains(got, "statusline") {
		t.Errorf("clearRefusal(no usage file) = %q, want a refusal: nothing could confirm the clear", got)
	}
	if got := clearRefusal("worker1", "idle", true); got != "" {
		t.Errorf("clearRefusal(idle, usage) = %q, want none", got)
	}
}
