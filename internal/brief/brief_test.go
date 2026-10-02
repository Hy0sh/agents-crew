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
	for _, stray := range []string{"unpredictable.;", "to check.;", "above.;"} {
		if strings.Contains(got, stray) {
			t.Errorf("brief renders a stray %q in the reset rule", stray)
		}
	}
	if strings.Contains(got, "The other workers are reset") {
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
	for _, want := range []string{"30 min", "at the end of your current turn"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q (silence threshold, or the delay a blocked message can take)", want)
		}
	}
}

func TestBuildListsOutsideWorkersAndCountsOnlyCodersForStacks(t *testing.T) {
	p := params("claude", 3, 2)
	p.Workers[0] = Worker{Kind: "claude", Dir: "/Users/me/studio", Overridden: true}
	got := Build(p)
	if !strings.Contains(got, "/Users/me/studio") || !strings.Contains(got, "outside the code") {
		t.Error("brief should list worker1 as outside the code, with its folder")
	}
	// 2 coders, 2 environments: no arbitration, even with 3 workers.
	if strings.Contains(got, "YOU arbitrate") {
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
	if !strings.Contains(got, "YOU arbitrate") {
		t.Errorf("brief should instruct master to arbitrate when maxStacks < n")
	}
}

func TestBuildNoArbitrationWhenUncapped(t *testing.T) {
	got := Build(params("claude", 3, 3))
	if strings.Contains(got, "YOU arbitrate") {
		t.Errorf("brief should not mention arbitration when maxStacks == n")
	}
	if !strings.Contains(got, "no arbitration needed") {
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

// A decision posed as prose was answered as prose, or not at all: the master
// has to reach for its agent's choice tool, named generically so the brief
// stays neutral for any herdr kind.
func TestBuildPosesDecisionsThroughTheChoiceTool(t *testing.T) {
	got := Build(params("claude", 3, 3))
	if strings.Count(got, "choice question tool") < 2 {
		t.Errorf("brief should send both scope decisions and relayed worker questions through the agent's choice tool")
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
	notes := "- ticket key as a suffix of the PR title\n- no test committed under apps/import_historical"
	p := params("claude", 3, 3)
	p.Notes = notes + "\n"
	got := Build(p)
	if !strings.Contains(got, "REPO RULES\n"+notes+"\nEND") {
		t.Errorf("brief should carry the notes verbatim, got:\n%s", got)
	}
}

func TestBuildStackProfileRule(t *testing.T) {
	if strings.Contains(Build(params("claude", 3, 3)), "stack profile") {
		t.Errorf("brief mentions a stack profile when none is configured")
	}
	p := params("claude", 3, 3)
	p.Profile = "light"
	got := Build(p)
	if !strings.Contains(got, `stack profile "light"`) {
		t.Errorf("brief should name the configured stack profile, got:\n%s", got)
	}
	if strings.Contains(got, "wtm ") {
		t.Errorf("stack profile rule hardcodes wtm tooling — state the intent, the master finds the command")
	}
}

func TestBuildWithoutNotesOmitsTheSection(t *testing.T) {
	got := Build(params("claude", 3, 3))
	if strings.Contains(got, "REPO RULES") {
		t.Errorf("brief should not open a repo-rules section when no notes are configured")
	}
}

func TestBuildMentionsTheStopHookOnlyForClaudeWorkers(t *testing.T) {
	if !strings.Contains(Build(params("claude", 3, 3)), "handed control back") {
		t.Errorf("brief should tell a master with claude workers that it gets automatic pings")
	}
	if strings.Contains(Build(params("codex", 3, 3)), "handed control back") {
		t.Errorf("brief promises pings to a master whose workers cannot send them (no hooks outside claude)")
	}
}

func TestBuildMixedKinds(t *testing.T) {
	p := params("claude", 3, 3)
	p.Workers[2] = Worker{Kind: "codex", Overridden: true}
	got := Build(p)
	if !strings.Contains(got, "mixed: worker1-testslug claude, worker2-testslug claude, worker3-testslug codex") {
		t.Errorf("brief should describe the mix of kinds, got:\n%s", got)
	}
	if !strings.Contains(got, "Only worker1-testslug, worker2-testslug have this hook") {
		t.Errorf("brief should say only the claude workers ping, got:\n%s", got)
	}
}

func TestBuildWithoutOverridesHasNoOverrideSection(t *testing.T) {
	if strings.Contains(Build(params("claude", 3, 3)), "configured apart") {
		t.Errorf("brief opens a worker-overrides section when none is configured")
	}
}

func TestBuildWorkerOverrides(t *testing.T) {
	p := params("claude", 3, 3)
	p.Workers[0] = Worker{Kind: "claude", Model: "opus", Prompt: "You plan, you do not code.\n", Overridden: true}
	p.Workers[2] = Worker{Kind: "codex", Model: "gpt-5-codex", Prompt: "You verify.", Overridden: true}
	got := Build(p)

	for _, want := range []string{
		"worker1-testslug runs on claude opus",
		"<<<INSTRUCTIONS FOR worker1-testslug\nYou plan, you do not code.\nEND OF INSTRUCTIONS FOR worker1-testslug>>>",
		"worker3-testslug runs on codex gpt-5-codex",
		"<<<INSTRUCTIONS FOR worker3-testslug\nYou verify.\nEND OF INSTRUCTIONS FOR worker3-testslug>>>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "worker2-testslug runs on") {
		t.Errorf("brief lists worker2, which has no override")
	}
	if !strings.Contains(got, "- worker2-testslug: no instructions of their own, general-purpose") {
		t.Errorf("brief should say the worker without override takes the rest, got:\n%s", got)
	}

	// The claude worker has its instructions as a system prompt; only the
	// codex one needs them copied into each brief.
	claudeLine, codexLine := lineWith(got, "worker1-testslug runs on"), lineWith(got, "worker3-testslug runs on")
	if !strings.Contains(claudeLine, "do NOT copy them") || strings.Contains(claudeLine, "instructions VERBATIM") {
		t.Errorf("claude worker line = %q; must say not to copy its instructions", claudeLine)
	}
	if !strings.Contains(codexLine, "instructions VERBATIM") {
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
	for _, want := range []string{"/repo", "2 workers", "worker1-testslug, worker2-testslug", "no arbitration needed"} {
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
	for _, want := range []string{"underlying container or services tool", "rm -rf", "/tmp", "git diff --cached --name-only", "base_branch", "ports"} {
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
	if !strings.Contains(hooked, "Do not ask them to maintain `updated_at` or `last_turn_end`") {
		t.Error("the brief should name the two stamped fields, not a vague \"these fields\" that swallows blocked_on")
	}
	got := Build(params("codex", 2, 2))
	if strings.Contains(got, "last_turn_end") {
		t.Error("with no hooked worker, the brief must not promise stamps acw won't write")
	}
	if !strings.Contains(got, "date -u") {
		t.Error("with no hooked worker, the brief should ask for updated_at in UTC")
	}
}

// Claude workers get the repo rules in their system prompt: the master
// stops copying them into their briefs, and keeps doing it for the others.
func TestBuildRepoRulesAlreadyInClaudeWorkers(t *testing.T) {
	p := params("claude", 2, 2)
	p.Notes = "- rule one"
	got := Build(p)
	if !strings.Contains(got, "already have them in their system prompt") || strings.Contains(got, "Copy them VERBATIM into every worker's brief") {
		t.Errorf("all-claude brief should say the workers already have the rules, got:\n%s", got)
	}

	p = params("codex", 2, 2)
	p.Notes = "- rule one"
	if got := Build(p); !strings.Contains(got, "Copy them VERBATIM") {
		t.Error("a brief for workers without a system prompt must keep the verbatim copy rule")
	}
}

func TestBuildPRWatchParagraphOnlyWhenOn(t *testing.T) {
	p := params("claude", 2, 2)
	if got := Build(p); strings.Contains(got, "PR #") {
		t.Error("brief without pr-watch must not mention PR lines")
	}
	p.PRWatch = true
	got := Build(p)
	for _, want := range []string{"PR #", "dispatch a rebase", "never draft a review reply", "PR watch failing"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief with pr-watch is missing %q", want)
		}
	}
}
