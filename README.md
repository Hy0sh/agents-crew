# agents-crew

`acw` launches a dedicated [Herdr](https://herdr.dev) workspace with one
**master** agent supervising N **worker** agents, to dispatch and supervise
tasks — tickets, bugs, anything — across a repo in parallel. By default both
are Claude Code (Opus for the master, Sonnet for the workers); any other kind
Herdr knows (`codex`, `gemini`, `cursor`...) works via `--master-kind` /
`--worker-kind`. Project-agnostic: the master's brief states intentions
("one isolated environment per task, released when the task changes hands")
and defers to whatever the target project's own docs/skills say for the
actual mechanics, rather than hardcoding one project's tooling.

Born out of (and battle-tested on) a Django/wtm-based project; see
`internal/brief` for the operating rules baked into the master's initial
prompt.

## Requirements

- [`herdr`](https://herdr.dev) — required.
- the CLI of each agent kind you ask for — required. With the defaults that's
  `claude` (Claude Code), `npm install -g @anthropic-ai/claude-code`; with
  `--worker-kind codex` it's `codex`, and so on (a Herdr kind's name is its
  executable).
- `wtm` — optional; without it, workers just don't get an isolated
  environment provisioned automatically.

`acw` checks these on every launch and refuses to start with a clear message
naming what's missing, rather than failing a few calls deep into the run (see
`internal/preflight`).

One trap that stays silent otherwise: `AGENTS.md` is the cross-agent standard,
and Claude Code reads it natively — but only when no `CLAUDE.md` shadows it in
the cwd or above. So a repo carrying its conventions in a `CLAUDE.md` alone
gives a non-Claude worker *no* project instructions at all. `acw` warns about
that combination at launch (non-blocking); the fix, in the target repo, is to
move the content to `AGENTS.md` and leave a `CLAUDE.md` containing
`@AGENTS.md`.

## Install

A tagged release ships prebuilt binaries for darwin/linux, amd64/arm64 — grab
the tarball for your platform from the
[latest release](https://github.com/Hy0sh/agents-crew/releases/latest).

Or, with Go installed:

```sh
go install github.com/Hy0sh/agents-crew/cmd/acw@latest
```

Or from a checkout:

```sh
go build -o ~/.local/bin/acw ./cmd/acw
```

(`~/.local/bin` just needs to be on `PATH`, same as `herdr`.)

Shell completion (bash/zsh/fish/powershell) comes from Cobra for free:
`acw completion zsh > ...` (see `acw completion --help` for where each shell
expects the file).

## Usage

```sh
cd /path/to/some/repo
acw [flags]
```

| Flag | Default | Meaning |
|---|---|---|
| `-n, --workers` | `3` | number of worker agents |
| `--max-stacks` | same as `--workers` | concurrent isolated environments the machine can hold; when lower, the master is told to arbitrate which worker gets one |
| `--master-kind` | `claude` | Herdr agent kind for the master (`claude`, `codex`, `gemini`, `cursor`...) |
| `--worker-kind` | `claude` | Herdr agent kind for the workers |
| `--master-model` | `opus` | model for the master agent; **empty means no `--model` is passed** to its CLI, for a kind that has no such flag |
| `--worker-model` | `sonnet` | model for worker agents; same empty-means-nothing rule |
| `--brief` | *(built-in)* | path to a custom master brief template — same fields as the built-in one (see `internal/brief/templates/master.md`), for when you want to change the operating rules without forking the tool |

`acw --help` / `acw stop --help` document all of this in the terminal too.

## `.acw-rules.md`, the repo's own hard rules

Drop an `.acw-rules.md` at the root of the target repo and its content goes
**verbatim** into the master's brief, with the instruction to copy it, still
verbatim, into every worker brief. Nothing to configure, no flag; no file
means the master is just told to go find the conventions itself, as before.

It's for the handful of rules that cost a force-push when missed — commit
message shape, where screenshots belong, the directory where a test must
never be committed — not for the project's whole documentation, which the
agents already read. Verbatim matters: a master reciting them from memory is
exactly how one gets dropped, and that happened for real.

At the repo root, and named after this tool rather than tucked under
`.claude/`: the swarm may well be running `codex` or `gemini`, and these are
the repo's rules, not an agent's config.

The master is created and briefed synchronously so you can start talking to
it as soon as the terminal opens. Workers (worktree + environment) are
provisioned progressively and concurrently in a detached background process:
each worker's pane/agent appears within seconds of its own `git worktree
add`, and `wtm adopt` (the slow part, real services starting) runs per
worker in its own goroutine — so setup never delays opening the terminal,
and you don't stare at a workspace with only the master pane in it while N
environments provision one after another. Its log lands in
`$TMPDIR/acw-workers-<timestamp>.log`.

Each Claude Code worker starts with a `Stop` hook that pings the master every
time it hands control back — the push notification Herdr doesn't have, so a
finished PR can't sit unnoticed until someone thinks to look. The ping says
which worker moved and nothing more (a hook can't know what changed); it
points the master at that worker's status file. Workers of any other kind
have no hooks and keep the previous behaviour, where the master polls.

Runs are scoped to the current directory, not global: agent names carry a
hash of the full repo path (see `internal/names`), so several swarms — one
per project — can run at the same time without colliding. The repo's own name
is on the workspace instead, where it is displayed once and in full.

```sh
acw stop
```

Tears down the swarm running in the **current directory**: each worker's
environment, the worktrees themselves, the shared status directory, and the
Herdr workspace. A swarm running for a different repo is left alone.
**Closing the terminal does nothing** — Herdr is a persistent server that
outlives it, and so do any environments workers started. `acw stop` is the
only way to actually stop it.

## How it works

- `internal/herdr` — thin wrapper around the `herdr` CLI (JSON in, typed Go out).
- `internal/wtm` — thin wrapper for giving/removing a worktree's environment,
  used only by this tool's own provisioning step (not by the brief text sent
  to agents — see below).
- `internal/gitutil` — fetch + default-branch detection + worktree creation/removal.
- `internal/layout` — pure math for the pane-split ratios that stack N
  worker panes evenly next to the master pane.
- `internal/names` — derives herdr-safe, per-directory agent names
  (`master-<slug>`, `worker1-<slug>`...) so multiple swarms can coexist.
- `internal/brief` — builds the master's initial prompt. The prose itself
  lives in `internal/brief/templates/*.md` (`text/template`, `go:embed`),
  not in the Go file — it's a document to read and edit, not a string
  literal to escape.
- `internal/teardown` — the `acw stop` logic.
- `internal/version` — `--version`, via `runtime/debug.ReadBuildInfo` (no ldflags).

## What it is not for

Reviewing pull requests. It was tried for a day: the reviews were good and
found a real defect, but each one cost a full environment switch for work
that produces no commit — and on a repo that bans AI-written review comments,
the result can't even be posted where reviews live. Dispatch work that ends in
a branch and a PR; run reviews separately.

## Design notes worth knowing before changing the brief

- **State intentions to the master, not literal commands for the target
  project's tooling.** An earlier version hardcoded `wtm adopt`/`--keepdb`
  directly in the brief; three real workers followed it to the letter on a
  day it didn't quite match reality, breaking their environments (one needed
  a full rebuild). The brief now says *what* must be true and tells the
  master to find *how* in the project's own docs — this tool's own
  provisioning code (which does know the project, because it's running
  inside it) is the only place literal `wtm`/`git` commands belong.
- **Herdr has no push notifications for agent state.** `herdr agent wait`
  without `--until` already returns on the first settled state
  (idle/done/blocked) — do not add `--until blocked`, it was tried and
  effectively never fires in practice (tool-approval prompts surface as
  `idle`, not `blocked`), leaving the master to only ever wake on timeout.
  The worker-side `Stop` hook is what made this reliable: anything that asks
  the master to re-arm a wait, or a worker to report in, is a discipline, and
  a day of real use dropped three of them. The hook still doesn't cover a
  worker frozen on a tool-approval prompt — it never ends its turn, so `Stop`
  never fires. That's what the wait remains the net for.
- **A `Notification` hook was considered for that frozen case and left out.**
  Zero occurrences over a full day with workers in auto mode: a real but
  unobserved failure, and not worth a second ping per worker until someone
  running workers interactively actually hits it.
- **`agent read` is a TUI capture, not text** — truncated lines, spinners,
  occasional corruption mid-redraw. The shared per-worker status file
  (`.claude/worktrees/.acw-status/workerN.json`, worker-written) is cheaper
  and more reliable for routine checks; it's still self-reported, so the
  brief also tells the master to cross-check objective signals (git status,
  CI) before trusting a push.
- **A false statement in the brief propagates to every worker at once** —
  it's more expensive than an omission. When in doubt, favor "state the
  intent, defer to the project" over a plausible-looking literal command.
- **`herdr agent start` on a just-created pane can race the shell's own
  startup** (oh-my-zsh, profile scripts) and fail with `agent_pane_busy`
  even though nothing is wrong — `internal/herdr.AgentStart` retries that
  specific error with backoff rather than surfacing it.
