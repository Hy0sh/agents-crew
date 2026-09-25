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
	if cleared("", usage{SessionID: "b9d48445"}) {
		t.Error("with no session read before the clear, any render (an old one too) would pass")
	}
}

// The master knows workers by their herdr names, which the brief lists:
// both forms name the same worker, anything else is refused.
func TestClearLabel(t *testing.T) {
	for in, want := range map[string]string{"worker2": "worker2", "worker2-3f9a1c": "worker2"} {
		if got, i, err := clearLabel(in, "3f9a1c"); err != nil || got != want || i != 2 {
			t.Errorf("clearLabel(%q) = %q, %d, %v; want %q, 2", in, got, i, err, want)
		}
	}
	for _, in := range []string{"worker02", "worker2abc", "worker2-other1", "worker0", "master-3f9a1c", "worker1/../x"} {
		if _, _, err := clearLabel(in, "3f9a1c"); err == nil {
			t.Errorf("clearLabel(%q) accepted it", in)
		}
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
