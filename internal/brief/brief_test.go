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

// The built-in brief reads the inbox with a background command, which
// never expires, instead of a Monitor re-armed every 30 minutes.
func TestBuildTellsTheMasterToReadTheInboxInTheBackground(t *testing.T) {
	p := params("claude", 2, 2)
	p.InboxWatch = "'/bin/acw' __inbox-watch '/repo/inbox'"
	p.InboxNext = "'/bin/acw' __inbox-next '/repo/inbox'"
	got := Build(p)
	if !strings.Contains(got, p.InboxNext) || !strings.Contains(got, "run_in_background") {
		t.Errorf("brief with an inbox should tell the master to run %q in the background", p.InboxNext)
	}
	if strings.Contains(got, "Monitor") {
		t.Error("the built-in brief must not arm a Monitor any more")
	}

	if got := Build(params("claude", 2, 2)); strings.Contains(got, "run_in_background") {
		t.Error("brief without an inbox (master that can't read one) must not mention it")
	}
}

func TestBuildHandsTheMasterStatusAndClear(t *testing.T) {
	p := params("claude", 2, 2)
	p.StatusCommand = "/bin/acw status --repo /repo"
	p.ClearCommand = "/bin/acw clear --repo /repo"
	got := Build(p)
	if !strings.Contains(got, p.StatusCommand) {
		t.Errorf("brief should give the master %q", p.StatusCommand)
	}
	if !strings.Contains(got, p.ClearCommand+" workerN") {
		t.Errorf("brief should give the master %q for its context resets", p.ClearCommand+" workerN")
	}
	// It waits up to 10 minutes: past the Bash tool's default 2.
	if !strings.Contains(got, "600000") {
		t.Error("brief should tell the master to give acw clear a 10-minute timeout")
	}
	for _, stray := range []string{"imprévisible. ;", "pour vérifier. ;", "ci-dessus. ;"} {
		if strings.Contains(got, stray) {
			t.Errorf("brief renders a stray %q in the reset rule", stray)
		}
	}
	if strings.Contains(got, "Les autres workers se réinitialisent") {
		t.Error("with only claude workers, the brief must not talk of other workers reset by hand")
	}
}

// acw's watcher replaces the master's own polling of every worker.
func TestBuildLeansOnTheWatcherInsteadOfWaits(t *testing.T) {
	p := params("claude", 2, 2)
	p.SilenceMinutes = 30
	got := Build(p)
	if strings.Contains(got, "--timeout 300000") {
		t.Error("the brief still tells the master to keep an agent wait running on every worker")
	}
	for _, want := range []string{"30 min", "à la fin de ton tour"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q (silence threshold, or the delay a blocked message can take)", want)
		}
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

// Each of these cost a frozen or misled worker in real use.
func TestBuildCarriesTheWorkerHygieneRules(t *testing.T) {
	got := Build(params("claude", 2, 2))
	for _, want := range []string{"outil de conteneurs ou de services sous-jacent", "rm -rf", "/tmp", "git diff --cached --name-only", "base_branch", "ports"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q", want)
		}
	}
}

// Only workers with the Stop hook get their stamps from acw; the others
// must still be asked for a real UTC time.
func TestBuildSaysWhoStampsTheStatus(t *testing.T) {
	hooked := Build(params("claude", 2, 2))
	if !strings.Contains(hooked, "last_turn_end") {
		t.Error("with claude workers, the brief should say acw stamps updated_at and last_turn_end")
	}
	// blocked_on is only cleared by acw, never filled: the master must keep
	// asking for it, so the brief names exactly which fields it can drop.
	if !strings.Contains(hooked, "Ne leur demande pas de tenir `updated_at` ni `last_turn_end`") {
		t.Error("the brief should name the two stamped fields, not a vague « ces champs » that swallows blocked_on")
	}
	got := Build(params("codex", 2, 2))
	if strings.Contains(got, "last_turn_end") {
		t.Error("with no hooked worker, the brief must not promise stamps acw won't write")
	}
	if !strings.Contains(got, "date -u") {
		t.Error("with no hooked worker, the brief should ask for updated_at in UTC")
	}
}
