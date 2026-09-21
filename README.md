# herd-tickets

Launches a dedicated [Herdr](https://herdr.dev) workspace with one **master**
agent (Claude Opus) supervising N **worker** agents (Claude Sonnet), to
dispatch and supervise tasks — tickets, bugs, anything — across a repo in
parallel. Project-agnostic: the master's brief states intentions ("one
isolated environment per task, released when the task changes hands") and
defers to whatever the target project's own docs/skills say for the actual
mechanics, rather than hardcoding one project's tooling.

Born out of (and battle-tested on) a Django/wtm-based project; see
`internal/brief` for the operating rules baked into the master's initial
prompt.

## Install

```sh
go build -o ~/.local/bin/herd-tickets ./cmd/herd-tickets
go build -o ~/.local/bin/herd-tickets-stop ./cmd/herd-tickets-stop
```

(`~/.local/bin` just needs to be on `PATH`, same as `herdr`.)

## Usage

```sh
cd /path/to/some/repo
herd-tickets [N] [MAX_STACKS]
```

- `N` — number of workers (default 3).
- `MAX_STACKS` — number of concurrent isolated environments the machine can
  hold (default N, capped to N). When lower than N, the master is told to
  arbitrate which worker gets an environment.

The master is created and briefed synchronously so you can start talking to
it as soon as the terminal opens. Workers (worktree + environment) are
provisioned in a detached background process so that setup — the slow part,
if the project's environment tool spins up real services — never delays
opening the terminal. Its log lands in `$TMPDIR/herd-tickets-workers-<timestamp>.log`.

```sh
herd-tickets-stop
```

Tears the whole thing down: each worker's environment, the shared status
directory, and the Herdr workspace. **Closing the terminal does nothing** —
Herdr is a persistent server that outlives it, and so do any environments
workers started. This is the only way to actually stop it.

## How it works

- `internal/herdr` — thin wrapper around the `herdr` CLI (JSON in, typed Go out).
- `internal/wtm` — thin wrapper for giving/removing a worktree's environment,
  used only by this tool's own provisioning step (not by the brief text sent
  to agents — see below).
- `internal/gitutil` — fetch + default-branch detection + worktree creation.
- `internal/layout` — pure math for the pane-split ratios that stack N
  worker panes evenly next to the master pane.
- `internal/brief` — builds the master's initial prompt.

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
  (`.claude/worktrees/.herd-status/workerN.json`, worker-written) is cheaper
  and more reliable for routine checks; it's still self-reported, so the
  brief also tells the master to cross-check objective signals (git status,
  CI) before trusting a push.
- **A false statement in the brief propagates to every worker at once** —
  it's more expensive than an omission. When in doubt, favor "state the
  intent, defer to the project" over a plausible-looking literal command.
