package brief

import (
	"strings"
	"testing"
)

func TestBuildNamesAllWorkers(t *testing.T) {
	got := Build("/repo", 3, 3)
	for _, want := range []string{"worker1", "worker2", "worker3"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q", want)
		}
	}
	if strings.Contains(got, "worker4") {
		t.Errorf("brief mentions worker4 for n=3")
	}
}

func TestBuildArbitrationWhenCapped(t *testing.T) {
	got := Build("/repo", 5, 3)
	if !strings.Contains(got, "C'est TOI qui arbitres") {
		t.Errorf("brief should instruct master to arbitrate when maxStacks < n")
	}
}

func TestBuildNoArbitrationWhenUncapped(t *testing.T) {
	got := Build("/repo", 3, 3)
	if strings.Contains(got, "C'est TOI qui arbitres") {
		t.Errorf("brief should not mention arbitration when maxStacks == n")
	}
	if !strings.Contains(got, "pas d'arbitrage nécessaire") {
		t.Errorf("brief should state no arbitration needed when maxStacks == n")
	}
}

func TestBuildNeverHardcodesProjectTooling(t *testing.T) {
	got := Build("/repo", 3, 3)
	for _, banned := range []string{"wtm adopt", "wtm remove", "wtm stop", "--keepdb", "docker compose"} {
		if strings.Contains(got, banned) {
			t.Errorf("brief hardcodes project-specific tooling %q — should state intent and defer to the project's own docs", banned)
		}
	}
}

func TestWorkersReadyMessageListsAllNames(t *testing.T) {
	got := WorkersReadyMessage(2)
	if !strings.Contains(got, "worker1") || !strings.Contains(got, "worker2") {
		t.Errorf("WorkersReadyMessage(2) = %q, missing a worker name", got)
	}
}
