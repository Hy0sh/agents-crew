package brief

import (
	"os"
	"strings"
	"testing"
)

const testSlug = "testslug"

// params is n identical workers of kind, maxStacks environments, no
// profile, no notes: the shape every launch had before overrides.
func params(kind string, n, maxStacks int) Params {
	workers := make([]Worker, n)
	for i := range workers {
		workers[i] = Worker{Kind: kind}
	}
	return Params{RepoPath: "/repo", Slug: testSlug, MaxStacks: maxStacks, Workers: workers}
}

func TestBuildTellsTheMasterToWatchTheInbox(t *testing.T) {
	p := params("claude", 2, 2)
	p.InboxWatch = "'/bin/acw' __inbox-watch '/repo/inbox'"
	got := Build(p)
	if !strings.Contains(got, p.InboxWatch) || !strings.Contains(got, "Monitor") {
		t.Errorf("brief with an inbox should tell the master to arm a Monitor on %q", p.InboxWatch)
	}

	if got := Build(params("claude", 2, 2)); strings.Contains(got, "Monitor") {
		t.Error("brief without an inbox (master that can't watch one) must not mention a Monitor")
	}
}

func TestBuildListsOutsideWorkersAndCountsOnlyCodersForStacks(t *testing.T) {
	p := params("claude", 3, 2)
	p.Workers[0] = Worker{Kind: "claude", Dir: "/Users/me/studio", Overridden: true}
	got := Build(p)
	if !strings.Contains(got, "/Users/me/studio") || !strings.Contains(got, "hors code") {
		t.Error("brief should list worker1 as outside the code, with its folder")
	}
	// 2 coders, 2 environments: no arbitration, even with 3 workers.
	if strings.Contains(got, "C'est TOI qui arbitres") {
		t.Error("the stack rule must count coders only; worker1 needs no environment")
	}
}

func TestBuildNamesAllWorkers(t *testing.T) {
	got := Build(params("claude", 3, 3))
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
	got := Build(params("claude", 5, 3))
	if !strings.Contains(got, "C'est TOI qui arbitres") {
		t.Errorf("brief should instruct master to arbitrate when maxStacks < n")
	}
}

func TestBuildNoArbitrationWhenUncapped(t *testing.T) {
	got := Build(params("claude", 3, 3))
	if strings.Contains(got, "C'est TOI qui arbitres") {
		t.Errorf("brief should not mention arbitration when maxStacks == n")
	}
	if !strings.Contains(got, "pas d'arbitrage nécessaire") {
		t.Errorf("brief should state no arbitration needed when maxStacks == n")
	}
}

func TestBuildNeverHardcodesProjectTooling(t *testing.T) {
	got := Build(params("claude", 3, 3))
	for _, banned := range []string{"wtm adopt", "wtm remove", "wtm stop", "--keepdb", "docker compose"} {
		if strings.Contains(got, banned) {
			t.Errorf("brief hardcodes project-specific tooling %q — should state intent and defer to the project's own docs", banned)
		}
	}
}

func TestBuildNamesTheWorkerAgentAndStaysNeutral(t *testing.T) {
	got := Build(params("codex", 3, 3))
	if !strings.Contains(got, "codex") {
		t.Errorf("brief should tell the master what agent its workers run on")
	}
	for _, banned := range []string{"Claude Code", "AskUserQuestion"} {
		if strings.Contains(got, banned) {
			t.Errorf("brief hardcodes %q — workers can run on any herdr kind", banned)
		}
	}
}

// The README's variable table is the only place a custom-brief author
// learns what exists: a field added to MasterData without a row there
// fails here rather than going undocumented.
func TestReadmeDocumentsEveryTemplateVariable(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range strings.Split(Variables(), ", ") {
		if !strings.Contains(string(readme), "| `"+v+"` |") {
			t.Errorf("README's custom brief table has no row for %s", v)
		}
	}
}

func TestBuildInjectsNotesVerbatim(t *testing.T) {
	notes := "- clé du ticket en suffixe du titre de PR\n- aucun test committé sous apps/import_historical"
	p := params("claude", 3, 3)
	p.Notes = notes + "\n"
	got := Build(p)
	if !strings.Contains(got, "RÈGLES DU DÉPÔT\n"+notes+"\nFIN") {
		t.Errorf("brief should carry the notes verbatim, got:\n%s", got)
	}
}

func TestBuildStackProfileRule(t *testing.T) {
	if strings.Contains(Build(params("claude", 3, 3)), "profile de stack") {
		t.Errorf("brief mentions a stack profile when none is configured")
	}
	p := params("claude", 3, 3)
	p.Profile = "light"
	got := Build(p)
	if !strings.Contains(got, "profile de stack « light »") {
		t.Errorf("brief should name the configured stack profile, got:\n%s", got)
	}
	if strings.Contains(got, "wtm ") {
		t.Errorf("stack profile rule hardcodes wtm tooling — state the intent, the master finds the command")
	}
}

func TestBuildWithoutNotesOmitsTheSection(t *testing.T) {
	got := Build(params("claude", 3, 3))
	if strings.Contains(got, "RÈGLES DU DÉPÔT") {
		t.Errorf("brief should not open a repo-rules section when no notes are configured")
	}
}

func TestBuildMentionsTheStopHookOnlyForClaudeWorkers(t *testing.T) {
	if !strings.Contains(Build(params("claude", 3, 3)), "a rendu la main") {
		t.Errorf("brief should tell a master with claude workers that it gets automatic pings")
	}
	if strings.Contains(Build(params("codex", 3, 3)), "a rendu la main") {
		t.Errorf("brief promises pings to a master whose workers cannot send them (no hooks outside claude)")
	}
}

func TestBuildMixedKinds(t *testing.T) {
	p := params("claude", 3, 3)
	p.Workers[2] = Worker{Kind: "codex", Overridden: true}
	got := Build(p)
	if !strings.Contains(got, "mixte : worker1-testslug claude, worker2-testslug claude, worker3-testslug codex") {
		t.Errorf("brief should describe the mix of kinds, got:\n%s", got)
	}
	if !strings.Contains(got, "Seuls worker1-testslug, worker2-testslug ont ce hook") {
		t.Errorf("brief should say only the claude workers ping, got:\n%s", got)
	}
}

func TestBuildWithoutOverridesHasNoOverrideSection(t *testing.T) {
	if strings.Contains(Build(params("claude", 3, 3)), "configurés à part") {
		t.Errorf("brief opens a worker-overrides section when none is configured")
	}
}

func TestBuildWorkerOverrides(t *testing.T) {
	p := params("claude", 3, 3)
	p.Workers[0] = Worker{Kind: "claude", Model: "opus", Prompt: "Tu planifies, tu ne codes pas.\n", Overridden: true}
	p.Workers[2] = Worker{Kind: "codex", Model: "gpt-5-codex", Prompt: "Tu vérifies.", Overridden: true}
	got := Build(p)

	for _, want := range []string{
		"worker1-testslug tourne sur claude opus",
		"<<<CONSIGNES DE worker1-testslug\nTu planifies, tu ne codes pas.\nFIN DES CONSIGNES DE worker1-testslug>>>",
		"worker3-testslug tourne sur codex gpt-5-codex",
		"<<<CONSIGNES DE worker3-testslug\nTu vérifies.\nFIN DES CONSIGNES DE worker3-testslug>>>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "worker2-testslug tourne sur") {
		t.Errorf("brief lists worker2, which has no override")
	}
	if !strings.Contains(got, "- worker2-testslug : aucune consigne propre, polyvalent") {
		t.Errorf("brief should say the worker without override takes the rest, got:\n%s", got)
	}

	// The claude worker has its instructions as a system prompt; only the
	// codex one needs them copied into each brief.
	claudeLine, codexLine := lineWith(got, "worker1-testslug tourne sur"), lineWith(got, "worker3-testslug tourne sur")
	if !strings.Contains(claudeLine, "ne les recopie PAS") || strings.Contains(claudeLine, "recopie VERBATIM") {
		t.Errorf("claude worker line = %q; must say not to copy its instructions", claudeLine)
	}
	if !strings.Contains(codexLine, "recopie VERBATIM") {
		t.Errorf("codex worker line = %q; must say to copy its instructions into every brief", codexLine)
	}
}

func lineWith(s, substr string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

func TestBuildFromSourceRendersCustomTemplate(t *testing.T) {
	got, err := BuildFromSource("Repo: {{.RepoPath}}, {{.N}} workers ({{.WorkerNames}}). {{.EnvCapRule}}", params("claude", 2, 2))
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
	if _, err := BuildFromSource("{{.Nope", params("claude", 1, 1)); err == nil {
		t.Fatal("BuildFromSource() with invalid template syntax = nil error, want one")
	}
}

func TestBuildFromSourceRejectsUnknownField(t *testing.T) {
	if _, err := BuildFromSource("{{.NotAField}}", params("claude", 1, 1)); err == nil {
		t.Fatal("BuildFromSource() referencing an unknown field = nil error, want one")
	}
}

func TestWorkersReadyMessageListsAllNames(t *testing.T) {
	got := WorkersReadyMessage(testSlug, 2)
	if !strings.Contains(got, "worker1-testslug") || !strings.Contains(got, "worker2-testslug") {
		t.Errorf("WorkersReadyMessage(testSlug, 2) = %q, missing a worker name", got)
	}
}
