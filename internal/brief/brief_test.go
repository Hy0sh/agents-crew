package brief

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSlug = "testslug"

func TestBuildNamesAllWorkers(t *testing.T) {
	got := Build("/repo", testSlug, "claude", 3, 3)
	for _, want := range []string{"worker1-testslug", "worker2-testslug", "worker3-testslug"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q", want)
		}
	}
	if strings.Contains(got, "worker4-testslug") {
		t.Errorf("brief mentions worker4 for n=3")
	}
}

func TestBuildArbitrationWhenCapped(t *testing.T) {
	got := Build("/repo", testSlug, "claude", 5, 3)
	if !strings.Contains(got, "C'est TOI qui arbitres") {
		t.Errorf("brief should instruct master to arbitrate when maxStacks < n")
	}
}

func TestBuildNoArbitrationWhenUncapped(t *testing.T) {
	got := Build("/repo", testSlug, "claude", 3, 3)
	if strings.Contains(got, "C'est TOI qui arbitres") {
		t.Errorf("brief should not mention arbitration when maxStacks == n")
	}
	if !strings.Contains(got, "pas d'arbitrage nécessaire") {
		t.Errorf("brief should state no arbitration needed when maxStacks == n")
	}
}

func TestBuildNeverHardcodesProjectTooling(t *testing.T) {
	got := Build("/repo", testSlug, "claude", 3, 3)
	for _, banned := range []string{"wtm adopt", "wtm remove", "wtm stop", "--keepdb", "docker compose"} {
		if strings.Contains(got, banned) {
			t.Errorf("brief hardcodes project-specific tooling %q — should state intent and defer to the project's own docs", banned)
		}
	}
}

func TestBuildNamesTheWorkerAgentAndStaysNeutral(t *testing.T) {
	got := Build("/repo", testSlug, "codex", 3, 3)
	if !strings.Contains(got, "codex") {
		t.Errorf("brief should tell the master what agent its workers run on")
	}
	for _, banned := range []string{"Claude Code", "AskUserQuestion"} {
		if strings.Contains(got, banned) {
			t.Errorf("brief hardcodes %q — workers can run on any herdr kind", banned)
		}
	}
}

func TestBuildInjectsRepoRulesVerbatim(t *testing.T) {
	repo := t.TempDir()
	rules := "- clé du ticket en suffixe du titre de PR\n- aucun test committé sous apps/import_historical"
	if err := os.WriteFile(filepath.Join(repo, RulesFile), []byte(rules+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Build(repo, testSlug, "claude", 3, 3)
	if !strings.Contains(got, rules) {
		t.Errorf("brief should carry %s verbatim, got:\n%s", RulesFile, got)
	}
}

func TestBuildWithoutRepoRulesOmitsTheSection(t *testing.T) {
	got := Build(t.TempDir(), testSlug, "claude", 3, 3)
	if strings.Contains(got, "RÈGLES DU DÉPÔT") {
		t.Errorf("brief should not open a repo-rules section when the repo declares none")
	}
}

func TestBuildMentionsTheStopHookOnlyForClaudeWorkers(t *testing.T) {
	if !strings.Contains(Build("/repo", testSlug, "claude", 3, 3), "a rendu la main") {
		t.Errorf("brief should tell a master with claude workers that it gets automatic pings")
	}
	if strings.Contains(Build("/repo", testSlug, "codex", 3, 3), "a rendu la main") {
		t.Errorf("brief promises pings to a master whose workers cannot send them (no hooks outside claude)")
	}
}

func TestBuildFromSourceRendersCustomTemplate(t *testing.T) {
	got, err := BuildFromSource("Repo: {{.RepoPath}}, {{.N}} workers ({{.WorkerNames}}). {{.EnvCapRule}}", "/repo", testSlug, "claude", 2, 2)
	if err != nil {
		t.Fatalf("BuildFromSource() error = %v", err)
	}
	for _, want := range []string{"/repo", "2 workers", "worker1-testslug, worker2-testslug", "pas d'arbitrage nécessaire"} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildFromSource() = %q, missing %q", got, want)
		}
	}
}

func TestBuildFromSourceRejectsBadSyntax(t *testing.T) {
	if _, err := BuildFromSource("{{.Nope", "/repo", testSlug, "claude", 1, 1); err == nil {
		t.Fatal("BuildFromSource() with invalid template syntax = nil error, want one")
	}
}

func TestBuildFromSourceRejectsUnknownField(t *testing.T) {
	if _, err := BuildFromSource("{{.NotAField}}", "/repo", testSlug, "claude", 1, 1); err == nil {
		t.Fatal("BuildFromSource() referencing an unknown field = nil error, want one")
	}
}

func TestWorkersReadyMessageListsAllNames(t *testing.T) {
	got := WorkersReadyMessage(testSlug, 2)
	if !strings.Contains(got, "worker1-testslug") || !strings.Contains(got, "worker2-testslug") {
		t.Errorf("WorkersReadyMessage(testSlug, 2) = %q, missing a worker name", got)
	}
}
