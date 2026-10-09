package brief

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const testSlug = "testslug"

// params is n identical workers of kind, one role worker, maxStacks
// environments, no profile, no notes: the shape every launch had before
// roles.
func params(kind string, n, maxStacks int) Params {
	workers := make([]Worker, n)
	for i := range workers {
		workers[i] = Worker{Kind: kind, Role: "worker", Label: fmt.Sprintf("worker%d", i+1)}
	}
	return Params{RepoPath: "/repo", Slug: testSlug, Stacks: true, MaxStacks: maxStacks, IdleCloseMinutes: 10, Workers: workers}
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

// The master orders the queue and ends tasks; acw hands them out, so the
// brief gives it no command that types into a worker.
func TestBuildHandsTheMasterTheQueue(t *testing.T) {
	p := params("claude", 2, 2)
	p.StatusCommand = "/bin/acw status --repo /repo"
	p.QueueCommand = "/bin/acw queue --repo /repo"
	p.DoneCommand = "/bin/acw done --repo /repo"
	got := Build(p)
	for _, want := range []string{
		p.StatusCommand,
		"`" + p.QueueCommand + " add <brief-file>`",
		"`" + p.QueueCommand + " move <id> <position>`",
		"--top", "--worker <worker>",
		"`" + p.DoneCommand + " <worker> <id>`",
		"up to 2", "free for 10 min",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q", want)
		}
	}
	for _, gone := range []string{"dispatch --repo", "clear --repo", "cannot reset"} {
		if strings.Contains(got, gone) {
			t.Errorf("brief still says %q", gone)
		}
	}
	p.Workers[1].Kind = "codex"
	if got := Build(p); !strings.Contains(got, "cannot reset is closed at the end of its task") {
		t.Error("with a worker acw cannot reset, the brief should say it is closed after each task")
	}
}

func TestBuildMinWorkersAtStartup(t *testing.T) {
	p := params("claude", 3, 3)
	if got := Build(p); !strings.Contains(got, "No worker is open yet") {
		t.Error("with no min-workers the brief should say no worker is open yet")
	}
	p.MinWorkers = 2
	got := Build(p)
	if !strings.Contains(got, "2 workers, the roles' min, are opening") || strings.Contains(got, "The first 2") || !strings.Contains(got, "keeps 2 open with nothing queued") {
		t.Error("with min-workers the brief should say they are opening and stay open")
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
	p.Workers[0] = Worker{Kind: "claude", Dir: "/Users/me/studio", Role: "analyst", Label: "analyst1", Tasks: []string{"analysis"}}
	got := Build(p)
	if line := lineWith(got, "- analyst1-testslug"); !strings.Contains(line, "/Users/me/studio") || !strings.Contains(line, "outside the code") {
		t.Errorf("analyst1 line = %q, want it outside the code, with its folder", line)
	}
	// 2 coders, 2 environments: none waits for one, even with 3 workers.
	if !strings.Contains(got, "every worker in the code gets its own isolated environment") {
		t.Error("the stack rule must count coders only; worker1 needs no environment")
	}
}

// A role with kinds tells the master how to reach it and that no other
// role takes those tasks; one with a min, that it stays open.
func TestBuildListsWorkerKinds(t *testing.T) {
	p := params("claude", 3, 3)
	p.Workers[2] = Worker{Kind: "claude", Model: "opus", Role: "reviewer", Label: "reviewer1", Tasks: []string{"need-review"}, Min: 1}
	line := lineWith(Build(p), "- reviewer1-testslug")
	for _, want := range []string{"`--kind need-review`", "no other role takes them", "1 kept open"} {
		if !strings.Contains(line, want) {
			t.Errorf("reviewer line %q missing %q", line, want)
		}
	}
}

// The workers by role: each role once, with its names, what runs it and
// what it takes; the instructions once per role.
func TestBuildRoles(t *testing.T) {
	p := params("claude", 4, 4)
	p.Workers = []Worker{
		{Kind: "claude", Model: "sonnet", Label: "worker1", Role: "worker"},
		{Kind: "claude", Model: "sonnet", Label: "worker2", Role: "worker"},
		{Kind: "claude", Model: "opus", Label: "reviewer1", Role: "reviewer", Tasks: []string{"need-review"}, Profile: "api", Prompt: "You review."},
		{Kind: "claude", Model: "opus", Label: "reviewer2", Role: "reviewer", Tasks: []string{"need-review"}, Profile: "api", Prompt: "You review."},
		{Kind: "codex", Model: "gpt-5-codex", Label: "verifier1", Role: "verifier", Tasks: []string{"verify"}, Prompt: "You verify."},
	}
	got := Build(p)
	line := lineWith(got, "- reviewer1-testslug, reviewer2-testslug")
	for _, want := range []string{"role reviewer", "claude opus", "`--kind need-review`", "api stack profile", "`--profile api`", "do NOT copy them"} {
		if !strings.Contains(line, want) {
			t.Errorf("reviewer line %q missing %q", line, want)
		}
	}
	if !strings.Contains(lineWith(got, "- worker1-testslug, worker2-testslug"), "without `--kind`") {
		t.Error("generalist role line")
	}
	if !strings.Contains(lineWith(got, "- verifier1-testslug"), "instructions VERBATIM") {
		t.Error("a codex role's instructions must be copied into each brief")
	}
	for _, want := range []string{
		"<<<INSTRUCTIONS FOR role reviewer\nYou review.\nEND OF INSTRUCTIONS FOR role reviewer>>>",
		"<<<INSTRUCTIONS FOR role verifier\nYou verify.\nEND OF INSTRUCTIONS FOR role verifier>>>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q", want)
		}
	}
	if strings.Count(got, "You review.") != 1 {
		t.Error("a role's instructions must be given once, not per worker")
	}
	if strings.Contains(got, "worker3") {
		t.Error("brief names a worker that doesn't exist")
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

// acw enforces the stack cap; the master is only told a task may wait.
func TestBuildStackCapIsAcws(t *testing.T) {
	got := Build(params("claude", 5, 3))
	if !strings.Contains(got, "only while an environment is left") || strings.Contains(got, "YOU arbitrate") {
		t.Error("capped: acw holds the cap, the master arbitrates nothing")
	}
	got = Build(params("claude", 3, 3))
	if strings.Contains(got, "may wait in the queue") || !strings.Contains(got, "nothing to arbitrate") {
		t.Error("uncapped: no task waits for an environment")
	}
	p := params("claude", 3, 3)
	p.Stacks = false
	if got := Build(p); !strings.Contains(got, "no worker gets an isolated environment") {
		t.Error("without stacks the brief should say acw gives none")
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
	p.Workers[2] = Worker{Kind: "codex", Role: "worker", Label: "worker3"}
	got := Build(p)
	if !strings.Contains(got, "mixed: worker1-testslug claude, worker2-testslug claude, worker3-testslug codex") {
		t.Errorf("brief should describe the mix of kinds, got:\n%s", got)
	}
	if !strings.Contains(got, "Only worker1-testslug, worker2-testslug have this hook") {
		t.Errorf("brief should say only the claude workers ping, got:\n%s", got)
	}
}

// One plain role, the swarm's own: nothing to say by role.
func TestBuildWithOneRoleHasNoRolesSection(t *testing.T) {
	if strings.Contains(Build(params("claude", 3, 3)), "The workers by role") {
		t.Errorf("brief opens a roles section for one plain role")
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
	for _, want := range []string{"/repo", "2 workers", "worker1-testslug, worker2-testslug", "nothing to arbitrate"} {
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
	if !strings.Contains(hooked, "Do not ask them to maintain `updated_at`, `last_turn_end` or `state_since`") {
		t.Error("the brief should name the three stamped fields, not a vague \"these fields\" that swallows blocked_on")
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

func TestBuildSwitchCommandOnlyWhenDetected(t *testing.T) {
	p := params("claude", 2, 2)
	if got := Build(p); strings.Contains(got, "wtm switch") {
		t.Error("without SwitchCommand the brief must not name wtm switch")
	}
	p.SwitchCommand = "wtm switch"
	got := Build(p)
	for _, want := range []string{"`wtm switch <branch> --from origin/<base>`", "never `git switch -c`", "for a worker with an environment"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief with SwitchCommand is missing %q", want)
		}
	}
}

func TestBuildNamesTheWaitingSubagentState(t *testing.T) {
	if got := Build(params("claude", 2, 2)); !strings.Contains(got, "`waiting_subagent`") {
		t.Error("the status contract should name the waiting_subagent state")
	}
}

// A document to approve is parked with its file, never presented in the
// conversation; the board's accept is a go; no handoff any more.
func TestBuildParksDocumentsToApprove(t *testing.T) {
	got := Build(params("claude", 2, 2))
	for _, want := range []string{"--doc", "`doc_path`", "ACCEPTED", "REFUSED", "interrupted"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(got), "handoff") {
		t.Error("brief still speaks of a handoff")
	}
}

func TestBuildPRWatchParagraphOnlyWhenOn(t *testing.T) {
	p := params("claude", 2, 2)
	if got := Build(p); strings.Contains(got, "PR #") {
		t.Error("brief without pr-watch must not mention PR lines")
	}
	p.PRWatch = true
	got := Build(p)
	for _, want := range []string{"PR #", "queue a rebase", "never draft a review reply", "PR watch failing"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief with pr-watch is missing %q", want)
		}
	}
}
