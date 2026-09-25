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
| `--preset` | *(none)* | named preset of the repo's config entry, laid over it — see [Presets](#presets) |
| `--brief` | *(built-in)* | path to a custom master brief template, for when you want to change the operating rules without forking the tool — see [Custom brief template](#custom-brief-template) for the variables |

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

Each Claude Code worker starts with a `Stop` hook that pings the master every
time it hands control back — the push notification Herdr doesn't have, so a
finished PR can't sit unnoticed until someone thinks to look. The ping says
which worker moved and nothing more (a hook can't know what changed); it
points the master at that worker's status file. Workers of any other kind
have no hooks and keep the previous behaviour, where the master polls.

Before pinging, the same hook runs `acw __turn-end` on that status file:
`updated_at` becomes the file's real modification time in UTC,
`last_turn_end` the time the turn ended, and `blocked_on` is cleared once
`state` no longer says blocked (any wording holding `block` or `bloq`,
since workers write it freely). These were the fields workers got wrong in
practice (a local time written with a `Z`, a block left set long after the
answer). Everything else in the file stays the worker's own; a file that
is missing or not a JSON object is left alone.

With a `claude` master, the ping is not typed into the master's input:
typed text merged with whatever you were writing to the master at that
moment. The hook appends a line to an inbox in the status directory, and
the master's first action is to run `acw __inbox-next` on it as a
background command: it waits for the next lines, prints them and exits,
and its end starts a turn without touching your draft. The master runs it
again after each batch. Unlike a Monitor, which expires after 30 minutes
and had to be re-armed all day long, a background command never expires,
so a quiet swarm costs the master nothing. Lines written while the master
works wait in the inbox. If the master forgets to run it again, acw types
a reminder into its input after 5 minutes of unread messages. acw starts
the master allowed to run its two inbox commands
(`--allowedTools "Bash(<acw> __inbox-watch:*)" "Bash(<acw> __inbox-next:*)"`),
and nothing broader. A master of another kind has no background commands
and still gets messages typed in.

acw also starts a watcher next to the swarm, `acw __watch` (log in
`$TMPDIR/acw-watch-<timestamp>.log`), so the master no longer keeps an
`agent wait` running on every worker. Every 5 seconds it reads herdr's
state of each worker and writes to the master when:

- a worker becomes `blocked` (a tool approval or a question): the message
  carries the last lines of its pane, and from that worker's second block
  since its last `acw clear` (so within one task), a hint that it may be
  hitting a forbidden call.
  Some prompts show as `idle` rather than `blocked` in herdr, so a claude
  worker gone idle for 15 s without its Stop hook having run gets the same
  message;
- a worker has been `working` for more than `silence-minutes` (default 30)
  with no activity acw can read: no turn end, no status update, no file
  changed in its worktree;
- a worker without the Stop hook (not `claude`) hands control back.

A message is read at the end of the master's current turn, so a tool
approval that denies itself after a few minutes can still expire during a
long turn of the master's. The watcher stops with its swarm: when `acw
stop` removes the status directory, when a new run stamps that directory
as its own, or when its master is gone from herdr.

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

## Custom brief template

`--brief` (or the `brief` config key) replaces the master's built-in brief
with your own file, a Go [`text/template`](https://pkg.go.dev/text/template).
Start from the built-in one,
[`internal/brief/templates/master.md`](internal/brief/templates/master.md),
and keep the variables you need:

| Variable | Content |
|---|---|
| `{{.RepoPath}}` | absolute path of the repo acw runs in |
| `{{.N}}` | number of workers |
| `{{.WorkerAgent}}` | the workers' Herdr kind (`claude`, `codex`...) when they all share one; otherwise `mixte : ` followed by each worker's kind |
| `{{.WorkerNames}}` | the workers' Herdr names, comma-separated (`worker1-<slug>, worker2-<slug>`) |
| `{{.EnvCapRule}}` | the stack capacity rule: how many environments exist, and the arbitration to do when `max-stacks` is below the worker count |
| `{{.StackProfileRule}}` | the wtm profile rule, empty when no `profile` is configured |
| `{{.RepoRules}}` | the `notes` file's content, empty when none is configured |
| `{{.PingingWorkers}}` | the names of the workers that ping the master on each turn (the `claude` ones, which have the Stop hook), empty when none does |
| `{{.WorkerOverrides}}` | each worker configured apart in `worker-overrides`: its kind, model and standing instructions in full, and whether the master must copy them into its briefs; empty when none is |
| `{{.InboxWatch}}` | the command the master must arm a Monitor on to receive pings and "workers ready", empty when the master is not `claude`. A custom brief that mentions neither it nor `{{.InboxNext}}` gets pings typed into the master's input, as before, with a warning at launch |
| `{{.InboxNext}}` | the command the master runs in the background to read its next messages, and runs again after each batch; empty when the master is not `claude`. The built-in brief uses this one |
| `{{.SilenceMinutes}}` | the `silence-minutes` value: how long a working worker may show no activity before acw's watcher tells the master |

Before `worker-overrides`, a template could test `{{if eq .WorkerAgent
"claude"}}` to know whether pings come in. That still works when all
workers are Claude, but `{{if .PingingWorkers}}` is right for a mixed swarm
too.

The empty ones are meant for `{{if .StackProfileRule}}...{{end}}`, as the
built-in template does. A variable that doesn't exist (a typo) makes acw
refuse to start instead of sending a brief with a hole in it.

Only the brief is a template. The `notes` file is injected as is: a
`{{.WorkerNames}}` written in it stays literal.

## Per-project config

Typing the same flags on every launch of the same repo gets old, and some
things have no flag at all: which wtm profile the workers' stacks start on,
notes about the project you'd rather not commit into it, and workers set
apart from the others. All of it goes in one personal file, **outside any
repo**:

```
~/.config/acw/config.json        ($XDG_CONFIG_HOME/acw/config.json if set)
```

```json
{
  "projects": {
    "/Users/me/dev/some-repo": {
      "workers": 4,
      "max-stacks": 3,
      "worker-model": "sonnet",
      "profile": "light",
      "notes": "~/.config/acw/some-repo.md"
    }
  }
}
```

It is an **override, not a registry**. Nothing to declare: a repo with no
entry (or no file at all) runs exactly as without config. Unlike `wtm`, acw
needs no answers to work, so the file only changes the defaults.

- **Key**: the directory you launch `acw` from, absolute (`~` allowed). A
  subdirectory of the repo does not match its entry.
- **Precedence**: a flag given on the command line > the preset given with
  `--preset` > the project's entry > the built-in default. `--worker-model sonnet` wins over a config saying
  `haiku`, even though `sonnet` is also the built-in value.
- **Launch line**: whenever an entry applies, acw prints what it read, e.g.
  `config: ~/.config/acw/config.json → workers=4, profile="light"`, so you
  can always tell where a value came from.

| Key | Same as | Built-in default |
|---|---|---|
| `workers` | `-n, --workers` | `3` |
| `max-stacks` | `--max-stacks` | same as `workers` |
| `master-kind` / `worker-kind` | `--master-kind` / `--worker-kind` | `claude` |
| `master-model` / `worker-model` | `--master-model` / `--worker-model` | `opus` / `sonnet`; `""` means no `--model`, like the flag |
| `brief` | `--brief` | built-in template |
| `profile` | *(no flag)* | none: the whole stack |
| `notes` | *(no flag)* | none |
| `worker-overrides` | *(no flag)* | none: every worker as above |
| `master-dir` | *(no flag)* | none: the master starts in the repo |
| `silence-minutes` | *(no flag)* | `30`: minutes a working worker may show no activity before acw's watcher tells the master |
| `presets` | *(picked with `--preset`)* | none |

**`profile`** is one of the project's wtm profiles (`wtm project edit
--profile-set light=db,backend`). Workers' environments are adopted with
`--profile` set to it, and the master is told they run on a partial stack:
when a task needs services outside it, the master has that worker switch
profiles before it starts, then switches it back. A lighter profile is
also what lets a higher `max-stacks` fit in memory, so the two keys usually
move together. acw does not check the name; if wtm doesn't know it, that
worker's `wtm adopt` fails and says so in the provisioning log.

**`notes`** is a markdown file holding the repo's hard rules. Its content
goes **verbatim** into the master's brief, with the instruction to copy it,
still verbatim, into every worker brief. No notes means the master is just
told to go find the conventions itself.

It's for the handful of rules that cost a force-push when missed — commit
message shape, where screenshots belong, the directory where a test must
never be committed — not for the project's whole documentation, which the
agents already read. Verbatim matters: a master reciting them from memory is
exactly how one gets dropped, and that happened for real.

Where the file lives is up to you. Keep it next to the config
(`~/.config/acw/some-repo.md`) for a client's repo where nothing may be
committed. Or commit it in the repo so the team shares and versions it, and
give a relative path (`"notes": "docs/acw-rules.md"`): it is read from the
repo.

**`worker-overrides`** sets one worker apart from the others: its own
kind, model, and standing instructions. Not predefined roles: whatever you
write in its prompt file.

```json
"worker-overrides": {
  "1": { "prompt": "~/.config/acw/planner.md", "model": "opus" },
  "3": { "kind": "codex", "model": "gpt-5-codex", "prompt": "verifier.md" }
}
```

- The key is the worker's index, `1` to `workers`. A worker with no entry
  takes `worker-kind` and `worker-model`, and a field left out of an entry
  falls back the same way (`"model": ""` still means no `--model`).
- `prompt` is a file, with the same path rules as `notes`. It has to
  survive the context reset the master does before every task, so it is
  not sent as a message. A `claude` worker gets it as a system prompt
  (`--append-system-prompt-file`). Any other kind has no system prompt acw
  knows how to set, so the master copies it verbatim into each of that
  worker's briefs.
- The master's brief lists every overridden worker with its instructions
  in full, and is told to dispatch accordingly: a worker whose prompt says
  to verify does not get a feature to write.
- Only `claude` workers ping the master (the Stop hook); in a mixed swarm
  the brief says which ones do, and the master watches the others.
- Refused at launch: an index that names no worker (`"4"` with 3
  workers, checked after `-n`), an unknown field, and a `prompt` file that
  can't be read. Unlike `notes`, that last one is not just a warning: a
  worker meant to verify that silently becomes a generic one would skew
  every dispatch.

### Working directories: `master-dir` and `dir`

By default the master starts in the repo and every worker in a worktree
of it. Some agents are better off elsewhere, e.g. in a folder whose
`.claude` brings the project's product tooling, next to the code:

```json
"master-dir": "~/Drive/some-project-studio",
"worker-overrides": {
  "1": { "dir": "~/Drive/some-project-studio", "prompt": "~/.config/acw/po.md" }
}
```

- **`dir` makes a worker one outside the code.** It starts in that
  folder with no worktree, no environment and no branch, and the master's
  brief says never to give it code. A worker without `dir` is a coder, in
  its worktree, as before. No role to declare: the folder decides.
- **Whoever codes stays in a worktree.** A `dir` or `master-dir` inside
  the repo (or the repo itself) is refused: that agent would work on your
  main checkout, with no isolation from the others.
- Environments go to the coders only: a worker outside the code takes no
  `max-stacks` slot, and `max-stacks` is capped to the number of coders.
- Each agent loads the instructions (`CLAUDE.md`, `.claude`) of the folder
  it starts in, not the repo's. For the master that is usually the point;
  the repo's hard rules still reach it through `notes`.
- The folder must be absolute (`~` allowed) and exist. Claude Code asks
  whether to trust a folder it has never opened, and an agent started
  there waits on that prompt: open `claude` there once beforehand. acw
  names that likely cause when an agent never becomes ready.
- `acw stop` is still run from the repo: the master is found by name,
  wherever it started.

### Presets

One repo, several ways to run it: the everyday multitask swarm, and for a
big feature a planner, coders and a reviewer. **`presets`** holds named
variants of the entry, and `--preset` picks one at launch:

```json
"/Users/me/dev/some-repo": {
  "workers": 3,
  "presets": {
    "feature": {
      "workers": 4,
      "brief": "~/.config/acw/pipeline-brief.md",
      "worker-overrides": {
        "1": { "prompt": "~/.config/acw/planner.md", "model": "opus" },
        "4": { "prompt": "~/.config/acw/reviewer.md" }
      }
    }
  }
}
```

```sh
acw --preset feature
```

- A preset takes the entry's keys. Each key it sets **replaces the entry's
  whole value**, `worker-overrides` included: merged index by index, a
  preset would inherit roles written for another composition of the swarm.
  A key it leaves out keeps the entry's value.
- A pipeline (plan, then code, then review) is a different way of
  dispatching, not only different workers: give the preset its own `brief`,
  or the master will use the roles as interchangeable task runners.
- One swarm per repo at a time, as without presets: switching is `acw
  stop`, then `acw --preset <other>`.
- Refused at launch: an unknown preset (the error lists the defined ones),
  `--preset` on a repo with no entry, and a preset inside a preset.

**Errors**: invalid JSON or an unknown key (`worker_model` for
`worker-model`) refuses to start and names the file. A `notes` file that
can't be read only warns, and the swarm starts without it.

## How it works

- `cmd/acw` — the CLI: flags merged with the config, each worker's final
  kind/model/prompt, launching the master, and the detached provisioner
  (worktrees, panes, agents, environments) it hands a JSON plan to.
- `internal/herdr` — thin wrapper around the `herdr` CLI (JSON in, typed Go out).
- `internal/wtm` — thin wrapper for giving/removing a worktree's environment,
  used only by this tool's own provisioning step (not by the brief text sent
  to agents — see below).
- `internal/gitutil` — fetch + default-branch detection + worktree creation/removal.
- `internal/layout` — pure math for the pane-split ratios that stack N
  worker panes evenly next to the master pane.
- `internal/names` — derives herdr-safe, per-directory agent names
  (`master-<slug>`, `worker1-<slug>`...) so multiple swarms can coexist,
  and where a run puts its worktrees, branches and status files: `acw stop`
  has to find exactly what provisioning created.
- `internal/preflight` — the dependency checks and warnings run before
  anything is started.
- `internal/brief` — builds the master's initial prompt. The prose itself
  lives in `internal/brief/templates/*.md` (`text/template`, `go:embed`),
  not in the Go file — it's a document to read and edit, not a string
  literal to escape.
- `internal/config` — reads the per-project config file (see
  [Per-project config](#per-project-config)).
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
  (`.claude/worktrees/.acw-status/workerN.json`, worker-written, timestamps
  stamped by acw) is cheaper
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
