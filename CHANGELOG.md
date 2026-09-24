# Changelog

What changed between published versions, and why. Versions follow
[semantic versioning](https://semver.org): while the major stays at 0, a minor
bump carries new commands or new behaviour, a patch bump carries fixes.

## [Unreleased]

### Added

- A working directory per agent: `master-dir`, and `dir` in
  `worker-overrides`, e.g. an agent in a folder whose `.claude` brings the
  project's product tooling. `dir` makes a worker one outside the code: no
  worktree, environment or branch, no `max-stacks` slot, and the master is
  told never to give it code. Whoever codes stays in a worktree: a folder
  inside the repo is refused. Each agent loads the instructions of the
  folder it starts in. When an agent never becomes ready, acw names
  Claude Code's trust prompt for a new folder as the likely cause.

### Changed

- acw finds the master by its name instead of its pane's directory, for
  `acw stop` and the "already running" check, so a master started in
  `master-dir` is found too.

- `presets` in a project's config entry, picked with `--preset <name>`:
  named variants of the entry, e.g. a planner, coders and a reviewer next to
  the everyday multitask swarm. A preset takes the entry's keys; each one it
  sets replaces the entry's whole value, `worker-overrides` included, so a
  preset never inherits roles written for another composition. Precedence
  becomes flag > preset > entry > built-in. An unknown preset, a `--preset`
  on a repo with no entry, and a preset inside a preset refuse to start.

### Fixed

- A worker's ping merged into what you were typing to the master: `herdr
  agent prompt` types into the master's input. With a `claude` master,
  pings and "workers ready" now go to an inbox file the master watches
  with a Claude Code Monitor, whose events leave your draft alone. Lines
  written while the Monitor is being re-armed wait in the file. The master
  is started allowed to run that one watch command, so a re-arm never stalls
  on an approval prompt. A master of another kind keeps pings typed in. A
  custom brief gets the inbox only if it uses the new `{{.InboxWatch}}`
  variable; otherwise acw warns and keeps typing pings in.

## [0.4.0] - 2026-09-23

Settings per repo and per worker, outside the repo. One breaking change:
`.acw-rules.md` is no longer read on its own (see Removed for the one-line
migration).

### Added

- A per-project config, `~/.config/acw/config.json` (or under
  `$XDG_CONFIG_HOME`), keyed by the directory acw is launched from. It is an
  override, not a registry: no entry means the same behaviour as before, and
  a flag given on the command line still wins. Its keys are the flag names,
  plus two that have no flag. `profile` starts the workers' wtm stacks on
  one of the project's profiles instead of the whole stack, and tells the
  master to switch a worker to another profile when a task needs more.
  `notes` points at the markdown file of the repo's hard rules, copied
  verbatim into every brief: outside the repo for one where nothing may be
  committed, or a relative path to a file the team commits. An unknown key
  refuses to start, so a typo in a key never gets silently ignored.
  Documented in `acw --help` and the README.
- `worker-overrides` in that config, to set one worker apart: its own kind,
  model, and a prompt file of standing instructions. The prompt has to
  outlive the context reset before each task, so a `claude` worker gets it
  as a system prompt, and for any other kind the master copies it verbatim
  into each of its briefs. The master's brief lists the overridden workers
  with their instructions, to dispatch accordingly, names the others as
  the generic ones taking the rest, and in a mixed swarm
  says which workers ping it (only the `claude` ones have the Stop hook).
  An index naming no worker, or a prompt file that can't be read, refuses
  to start. The background provisioner now takes its plan as a single JSON
  argument, since a list per worker doesn't fit positional ones. New brief
  variables: `{{.PingingWorkers}}`, `{{.WorkerOverrides}}`.

### Removed

- `.acw-rules.md` is no longer picked up automatically. `notes` does the
  same job and can point at a file inside the repo, so two entry points for
  one need was one too many. To keep an existing file, add
  `"notes": ".acw-rules.md"` to the repo's entry in the config.

### Fixed

- An error from the command itself (a missing dependency, a config typo) was
  printed twice and buried under the full usage text. It is now printed
  once, alone. A bad flag or argument still shows the usage.
- A custom `--brief` referencing a variable that doesn't exist only failed
  after the Herdr workspace and the master agent had been started. It now
  fails before anything is created, and the error lists the variables that
  do exist. They are also listed in `acw --help` and documented in the
  README, where they previously could only be found by reading the source.

## [0.3.0] - 2026-09-22

All of this comes from one full day running a 3-worker swarm on a Django
project: 7 PRs, 3 merged. Each entry names what actually went wrong.

### Added

- A `Stop` hook, installed in each Claude Code worker at startup, that pings
  the master every time the worker hands control back. Herdr has no push
  notification for agent state, and every substitute is a discipline someone
  has to keep up: re-arming `agent wait` after each wake-up and each dispatch,
  or telling each worker to report in. Disciplines get dropped — on that day a
  finished PR went unnoticed for an afternoon, and two reviews for fifteen
  minutes each. A hook is not a discipline: it fires whatever the master or
  the worker remembered, and it survives the context reset between two tasks.
  It carries no information about what changed (it cannot know) and points at
  the status file instead. Claude Code workers only — no other kind exposes
  hooks, and they keep the previous polling behaviour. Passed as inline JSON
  on the worker's own CLI, so nothing lands in a file a worker could commit by
  accident. Expect 2 to 5 pings per worker per task.
- `.acw-rules.md` at the repo root, injected verbatim into the master's brief
  when present. The brief already told the master to repeat the repo's
  unforgiving conventions in every worker brief, but it had no way to know
  them: a worker committed a test file into a directory where the repo forbids
  any, because that rule only ever lived in the master's head. Verbatim and
  never summarized — summarizing from memory is exactly how that rule got
  dropped. At the repo root and named after this tool rather than under
  `.claude/`: the swarm may well be running `codex` or `gemini`, and these are
  the repo's rules, not an agent's config.

### Changed

- Agent names drop the repo's basename and keep only the path hash
  (`worker1-3f9a1c`), while the workspace label gains it (`acw
  shop-frontend`). Herdr's sidebar is a narrow column, and
  `worker1-shop-frontend-3f9a1c` was truncated there to precisely the part
  that does not discriminate. Which repo a swarm belongs to is now displayed
  once, in the label; in code it was never the name that mattered, every
  lookup matches on the agent's cwd.
- The brief's environment rule was pushing workers toward the one move that
  breaks: it framed the isolated environment as per-task, "released when the
  task changes hands". Two workers out of three hit a session confined to its
  own directory, where creating a worktree elsewhere is plainly refused, and
  had to improvise. A worker's worktree is now stated as its own for the whole
  run: a new task is a new branch **in place**, and the only legitimate
  release is the one the master decides to reassign an environment. The rule
  also warns that an environment often follows the branch, so a switch made
  without telling the project's tooling leaves a ghost record behind — measured,
  it does not keep a stack running, but it does push later environments further
  out.
- A context reset before **every** task, instead of one left to the master's
  judgement. The argument is cost (a worker at 40% context bills that context
  on every call, for a task it no longer needs) and a second benefit found on
  the way: a worker that remembers its previous task "recognizes" problems that
  came from its own work instead of finding them. The brief also states the
  trap: a reset sent to a worker that is still working is queued, not executed,
  so the sequence has to start from a worker at rest.
- `acw stop` says what it is doing while it does it. Its slow half is one
  Docker stack going down after another, and it used to print nothing between
  the first line and the last — a silent minute reads as a hang, and when it
  does hang the step that is running is the one worth naming. It also now
  says when a worker's branch survived the teardown, which means unmerged work
  was on it.
- The shared status file gains `branch`, `decision`, `pr_url`, `proof_path`
  and `blocked_on`. The workers kept that file up to date on their own all
  day; what cost the master were the free-text values — "approach decided"
  without naming the approach (it took reading the diff to find out, and the
  approach was wrong), "PR opened" without the link. A structured field left
  empty is visible, an empty sentence is not.

## [0.2.0] - 2026-09-22

### Added

- `--master-kind` / `--worker-kind` (both default `claude`) pick any agent
  kind Herdr can start — `codex`, `gemini`, `cursor`, and the twenty-odd
  others. Nothing was abstracted: Herdr already did the work, `acw` just
  stopped passing `--kind claude` unconditionally. `--master-model` /
  `--worker-model` now accept an empty value, which passes no `--model` at
  all to that CLI — the escape hatch for a kind that has no such flag.
- A non-blocking launch warning when the workers run on something other than
  Claude Code in a repo whose project instructions live in a `CLAUDE.md` with
  no `AGENTS.md` beside it. Claude Code reads `AGENTS.md` natively but only
  when no `CLAUDE.md` shadows it, so that exact combination leaves a
  codex/gemini/... worker with zero project instructions — no error, no
  signal, just code written outside the repo's conventions. The user-level
  `~/.claude/CLAUDE.md` doesn't count: it loads alongside `AGENTS.md` rather
  than shadowing it.

### Changed

- The master's brief no longer assumes its workers are Claude Code sessions:
  it names the actual kind (`{{.WorkerAgent}}`, a new template field), speaks
  of "a context reset (`/clear` or your agent's equivalent)" and of project
  instructions "whatever file carries them (AGENTS.md, CLAUDE.md...)", and
  asks for a decision with options and a recommendation instead of naming
  Claude Code's `AskUserQuestion` tool. The rule that each worker's brief must
  repeat the repo's 3-4 unforgiving conventions gained a reason: depending on
  the worker's agent, those conventions may never have been loaded at all.
- The binary is now `acw`, not `agents-crew` — short like `wtm`, for daily
  typing. The repo/module keep the descriptive name (`github.com/Hy0sh/
  agents-crew`), only `cmd/agents-crew` moved to `cmd/acw`, same pattern as
  `worktree-manager`/`wtm`. The shared status directory moved from
  `.agents-crew-status` to `.acw-status` accordingly.

- Worker provisioning is progressive and concurrent instead of
  all-at-once-at-the-end: `git worktree add` → pane split → rename →
  agent start is fast (seconds), so each worker's pane and agent appear
  one after another as soon as its own worktree exists — not after every
  worker finishes. `wtm adopt` (the slow part, real services starting)
  now runs in its own goroutine per worker once the pane is already
  live, so N workers provision their environments concurrently instead
  of serially — roughly N× faster wall-clock time for that part, and
  nobody stares at a workspace with only the master pane in it for
  several minutes.
- Runs are scoped to the directory, not global. Agent names now include a
  `names.Slug(repo)` (a readable basename plus a hash of the full path,
  so two different directories sharing a basename never collide) instead
  of the bare `master`/`workerN` — Herdr requires every live agent name
  to be unique, so a single swarm used to be a system-wide limit.
  `agents-crew stop` and the already-running guard on plain `agents-crew`
  now match on the agent's actual pane `cwd` against the current
  directory, not on the literal name `master`, so several swarms — one
  per project — can run at the same time, and `stop` only ever tears
  down the one in the directory you run it from.

- Rebuilt the CLI on Cobra: `agents-crew --help` and `agents-crew completion
  {bash,zsh,fish,powershell}` now exist. `N`/`MAX_STACKS` positional
  arguments became `-n/--workers` and `--max-stacks` flags.
- Single binary: the standalone `agents-crew-stop` binary is gone,
  `agents-crew stop` is the only way to tear a swarm down now.
- `--master-model` (default `opus`) and `--worker-model` (default `sonnet`)
  make the models configurable instead of hardcoded.
- `--brief <path>` lets a custom `text/template` file replace the built-in
  master brief entirely, same fields (`{{.RepoPath}}`, `{{.N}}`,
  `{{.WorkerNames}}`, `{{.EnvCapRule}}`).

### Fixed

- `herdr agent start` on a just-created pane (fresh off `workspace
  create` or `pane split`) could fail with `agent_pane_busy`: the
  shell (oh-my-zsh, profile scripts...) was still initializing when the
  start was attempted, a moment after the pane itself already existed.
  `AgentStart` now retries with backoff on that specific error instead
  of surfacing it as a real failure — this hit the master's own pane in
  production, aborting the run before any worker was ever provisioned.
- `agents-crew stop` called `wtm stop`/`wtm remove` from inside each
  worker's worktree with no branch argument, assuming the same
  current-directory inference `wtm adopt` documents. Verified against `wtm
  stop --help`: the branch is mandatory for `stop`/`remove`, unlike
  `adopt`. Left a stack running after a "torn down" swarm until this was
  fixed.
- `agents-crew stop` discovered workers by asking Herdr for agents named
  `workerN` — but an environment can be `up` (provisioning already ran
  `wtm adopt`) before the pane for it exists, let alone before `herdr
  agent start` names it. Stopping during that window found nothing to
  clean up and reported success anyway, orphaning real Docker stacks.
  Worker discovery is now a filesystem scan of
  `.claude/worktrees/workerN-*`, independent of Herdr/agent state.
- Even on the happy path, `wtm remove` never deleted the worktree
  directory itself: it only removes worktrees it created via `wtm
  create`, and agents-crew's are plain `git worktree add` ones (adopted,
  not created). `.claude/worktrees/workerN-*` directories accumulated on
  every stop. `agents-crew stop` now also runs `git worktree remove
  --force` and deletes the branch.

## [0.1.0] - 2026-09-21

### Added

- Initial release: `agents-crew` launches a Herdr workspace with one master
  agent (Claude Opus) supervising N worker agents (Claude Sonnet) to
  dispatch and supervise tasks in a repo in parallel. `agents-crew stop`
  (also installed as a standalone `agents-crew-stop` binary) tears it down.
- The master's brief lives in `internal/brief/templates/*.md`, not in Go
  source, and states intentions for project-specific tooling (environment
  isolation, test data, low-level tool wrappers) rather than hardcoding one
  project's commands — the target project's own docs are the source of
  truth for the mechanics.
- `agents-crew` checks `herdr` and `claude` are installed before doing
  anything, with a clear message and install instructions if not; `wtm` is
  checked too but stays optional.

[Unreleased]: https://github.com/Hy0sh/agents-crew/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/Hy0sh/agents-crew/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/Hy0sh/agents-crew/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Hy0sh/agents-crew/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Hy0sh/agents-crew/releases/tag/v0.1.0
