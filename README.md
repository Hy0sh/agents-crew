# agents-crew

`acw` launches a dedicated [Herdr](https://herdr.dev) workspace with one
**master** agent (Claude Opus) supervising N **worker** agents (Claude
Sonnet), to dispatch and supervise tasks — tickets, bugs, anything — across a
repo in parallel. Project-agnostic: the master's brief states intentions
("one isolated environment per task, released when the task changes hands")
and defers to whatever the target project's own docs/skills say for the
actual mechanics, rather than hardcoding one project's tooling.

Born out of (and battle-tested on) a Django/wtm-based project; see
`internal/brief` for the operating rules baked into the master's initial
prompt.

## Requirements

- [`herdr`](https://herdr.dev) — required.
- `claude` (Claude Code CLI) — required, `npm install -g @anthropic-ai/claude-code`.
- `wtm` — optional; without it, workers just don't get an isolated
  environment provisioned automatically.

`acw` checks these on every launch and refuses to start with a clear message
if `herdr` or `claude` is missing, rather than failing a few calls deep into
the run (see `internal/preflight`).

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
| `--master-model` | `opus` | Claude model for the master agent |
| `--worker-model` | `sonnet` | Claude model for worker agents |
| `--brief` | *(built-in)* | path to a custom master brief template — same fields as the built-in one (see `internal/brief/templates/master.md`), for when you want to change the operating rules without forking the tool |

`acw --help` / `acw stop --help` document all of this in the terminal too.

The master is created and briefed synchronously so you can start talking to
it as soon as the terminal opens. Workers (worktree + environment) are
provisioned progressively and concurrently in a detached background process:
each worker's pane/agent appears within seconds of its own `git worktree
add`, and `wtm adopt` (the slow part, real services starting) runs per
worker in its own goroutine — so setup never delays opening the terminal,
and you don't stare at a workspace with only the master pane in it while N
environments provision one after another. Its log lands in
`$TMPDIR/acw-workers-<timestamp>.log`.

Runs are scoped to the current directory, not global: agent names carry a
hash of the full repo path (see `internal/names`), so several swarms — one
per project — can run at the same time without colliding.

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
