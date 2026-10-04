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
- [`gh`](https://cli.github.com), logged in — only with `pr-watch` on.

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
| `-n, --workers` | `3` | most worker agents open at once; acw opens them as tasks are queued — see [Pool and queue](#pool-and-queue) |
| `--min-workers` | `0` | workers kept open with nothing queued; equal to `--workers`, the swarm is fixed |
| `--idle-close-minutes` | `10` | how long a free worker above `--min-workers` stays open with nothing queued for it |
| `--max-stacks` | same as `--workers` | concurrent isolated environments the machine can hold; when lower, acw opens a worker in the code only while one is left |
| `--master-kind` | `claude` | Herdr agent kind for the master (`claude`, `codex`, `gemini`, `cursor`...) |
| `--worker-kind` | `claude` | Herdr agent kind for the workers |
| `--master-model` | `opus` | model for the master agent; **empty means no `--model` is passed** to its CLI, for a kind that has no such flag |
| `--worker-model` | `sonnet` | model for worker agents; same empty-means-nothing rule |
| `--preset` | *(none)* | named preset of the repo's config entry, laid over it — see [Presets](#presets) |
| `--brief` | *(built-in)* | path to a custom master brief template, for when you want to change the operating rules without forking the tool — see [Custom brief template](#custom-brief-template) for the variables |
| `--pr-watch` | off | acw's watcher follows your open non-draft pull requests on the repo and tells the master what changed on them — see [PR watch](#pr-watch) |

`acw --help` / `acw stop --help` document all of this in the terminal too.

The master is created and briefed synchronously so you can start talking to
it as soon as the terminal opens. No worker is opened at launch: acw's
watcher, a detached background process, opens them as the master queues
tasks (see [Pool and queue](#pool-and-queue)). Its log lands in
`$TMPDIR/acw-watch-<timestamp>.log`.

### Pool and queue

What to do, and in which order, is the master's call; where and when it
runs is acw's, from fixed rules a model cannot bend:

- The master queues each task, `acw queue add <brief-file>`, and may
  reorder the queue (`acw queue move <id> <position>`, `add --top` for an
  urgent one) or take a task out (`acw queue remove <id>`). `acw queue`
  lists the workers and the queue.
- acw's watcher hands the tasks out in the queue's order, each to a free
  worker whose agent is idle: the same steps as `acw dispatch` (branch if
  asked, confirmed `/clear`, brief). The master hears `task #N → workerN`.
- A task that no free worker can take opens the lowest worker not open,
  up to `workers`, and for a worker in the code when the repo has wtm
  stacks, up to `max-stacks`. Opening is the worktree (named after the
  moment it opens, so a reopened worker never collides with the branch
  its first opening left), its pane, its agent, then `wtm adopt`;
  `wtm doctor`'s port clash sections, if any, go into the "opened"
  message. A failed opening is undone and the master told. Workers opened
  together come up one at a time up to their pane, and one `wtm adopt`
  at a time: run together, `git worktree add` raced for `.git/config`,
  and two adopts each missed the other's ports. The master keeps the left
  60 % of the screen; the workers share one column on the right, each new
  one halving the tallest worker pane.
- A worker set apart in `worker-overrides` only takes the tasks queued for
  it with `--worker workerN`; the others take everything else. `--worker`
  is also how a fix after a KO goes back to the worker that has the
  context.
- A task ends when the master runs `acw done workerN`, after checking its
  result: never on the worker's word, nor on a merged PR, since acw cannot
  tell which task a PR belongs to.
- A free worker with nothing queued for it is closed after
  `idle-close-minutes`, unless that would take the pool under
  `min-workers`: its pane and agent, its stack and worktree. Its task
  branch stays, as with `acw stop`. A worker whose worktree has changes is
  never closed: the master is told once. A kind acw cannot reset between
  tasks (no `/clear` to confirm) is closed as soon as its task is done.
- A task acw could not hand out (a branch held by another worktree, a
  failed switch) goes back first in the queue, held with the reason, and
  is skipped until the master moves or removes it.

`min-workers` set to `workers` opens every worker at launch and never
closes one: the fixed swarm of acw 0.10.

Each Claude Code worker starts with a `Stop` hook that pings the master every
time it hands control back — the push notification Herdr doesn't have, so a
finished PR can't sit unnoticed until someone thinks to look. The ping says
which worker moved and what moved in its status file since its previous
ping: `state` before and after, or "status unchanged". Most pings in real
use ended turns the master had triggered itself, and each cost it a read of
the file to find nothing new. Workers of any other kind
have no hooks and keep the previous behaviour, where the master polls.

Before pinging, the same hook runs `acw __turn-end` on that status file:
`updated_at` becomes the file's real modification time in UTC,
`last_turn_end` the time the turn ended, and `blocked_on` is cleared once
`state` no longer says blocked (any wording holding `block` or `bloq`,
since workers write it freely). These were the fields workers got wrong in
practice (a local time written with a `Z`, a block left set long after the
answer). It also sets `state_since`, the turn end at which the current
`state` was first seen, from what it keeps in `workerN.since` (the worker
rewrites its file whole, so the status cannot hold it): workers waiting in
the same state can be ordered without the master's memory. Everything else
in the file stays the worker's own; a file that
is missing or not a JSON object is left alone. It then prints the delta
the ping carries, and keeps what it saw in `workerN.ping` for the next
turn.

With a `claude` master, the ping is not typed into the master's input:
typed text merged with whatever you were writing to the master at that
moment. The hook appends a line to an inbox in the status directory, and
the master's first action is to run `acw __inbox-next` on it as a
background command: it waits for the next lines, prints them and exits,
and its end starts a turn without touching your draft. The master runs it
again after each batch. Claude Code kills a background command at its
timeout (30 minutes by default), so after 25 minutes with no message it
exits on its own, printing "nothing new", and the master runs it again
like after any batch. Lines written while the master
works wait in the inbox. If the master forgets to run it again, acw types
a reminder into its input after 5 minutes of unread messages. acw starts
a claude master allowed to run `acw status`, `acw queue` and `acw done`,
plus its two inbox commands when it reads an inbox
(`--allowedTools "Bash(<acw> __inbox-watch:*)" "Bash(<acw> __inbox-next:*)"
"Bash(<acw> status:*)" "Bash(<acw> queue:*)" "Bash(<acw> done:*)"`), and
nothing broader. A master of another kind has no background commands
and still gets messages typed in.

acw also starts a watcher next to the swarm, `acw __watch` (log in
`$TMPDIR/acw-watch-<timestamp>.log`), so the master no longer keeps an
`agent wait` running on every worker. Every 5 seconds it reads herdr's
state of each worker and writes to the master when:

- a worker becomes `blocked` (a tool approval or a question): the message
  carries the last lines of its pane, and from that worker's second block
  since its last context reset (so within one task), a hint that it may be
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
Herdr workspace. A swarm running for a different repo is left alone. A
worker's task branch is kept with its commits, pushed or not; only the
`agents/workerN-…` branch acw cut for it is deleted, and only when nothing
was committed on it.
**Closing the terminal does nothing** — Herdr is a persistent server that
outlives it, and so do any environments workers started. `acw stop` is the
only way to actually stop it.

```sh
acw status [--repo <dir>]
acw queue [--repo <dir>] [add <brief-file> [--branch <b>] [--worker workerN] [--top] | move <id> <pos> | remove <id>]
acw done [--repo <dir>] workerN
acw clear [--repo <dir>] worker1 [worker2...]
acw dispatch [--repo <dir>] worker1 <brief-file>
acw pause
acw resume
```

- `acw status` shows every worker at a glance, from what acw can read
  without asking anyone: herdr's state, the status file's `state` and
  since when, how old
  its `updated_at`, `last_turn_end` and the worktree's last change are,
  context and 5-hour quota, branch and base, PR, then the unread messages
  of the master's inbox. A status 40 minutes old next to a worktree changed
  2 minutes ago is a worker coding without updating its status.
- `acw clear` resets a claude worker's context before a new task: it waits
  for the worker to be idle, refuses one that is blocked (the reset would
  queue behind the prompt), sends `/clear`, and returns once the worker's
  status line reports a new session, or fails saying so after 60 s. It also
  starts over the watcher's block count for that worker. A worker that has
  not finished a turn yet has nothing to reset: `acw clear` says so and
  returns without sending anything, since a `/clear` there keeps the same
  session and could never be confirmed.
- `acw queue` and `acw done`: see [Pool and queue](#pool-and-queue). These
  two and `acw status` are the commands the master may run without a
  prompt; `acw clear` and `acw dispatch` stay yours.
- `acw dispatch` hands a free claude worker a task in one call, outside
  the queue: the worker is busy until `acw done`. The same
  wait and refusals as `acw clear`, the confirmed reset, then the brief
  file's content typed into its prompt. A blank or unreadable brief is
  refused before anything is sent. With `--branch <b>` (a fix or a rebase
  on a known branch) it first runs `git fetch` and puts the worker on that
  branch: through `wtm switch` for a worker with a wtm stack, which moves
  the stack along on the same ports, and refuses that worker when wtm
  has no switch (before 0.26.0), since a plain `git switch` would leave
  its stack behind; plain `git switch` for a worker without one. The
  stack only gets a fresh dump when the branch changes: put back on the
  branch it is already on (a fix after a KO), it restarts with its data
  as it was. It refuses a branch another worktree holds, and never
  stashes. Without `--branch` the worker names its branch
  itself; the brief tells workers with a stack to create it with `wtm
  switch`.
- `acw pause` stops the workers' stacks (`wtm stop`) for a break, and `acw
  resume` starts them again (`wtm start`) on the profile the swarm was
  launched with. Worktrees, agents and the workspace stay as they are, and
  the master hears of both in its inbox. Without a terminal wtm asks
  nothing and starts even when memory is tight; its warning is shown as is.

`status`, `queue` and `done` take `--repo` because the master may run
elsewhere (`master-dir`): its brief hands it those commands fully
written, and it is started allowed to run them. `clear` and `dispatch`
take it too, for you. They read what acw keeps in the status
directory, including the claude workers' status line, which acw sets to its
own (`ctx 34% · 5h 78%`) in place of the user's, to record that usage.

## Custom brief template

`--brief` (or the `brief` config key) replaces the master's built-in brief
with your own file, a Go [`text/template`](https://pkg.go.dev/text/template).
Start from the built-in one,
[`internal/brief/templates/master.md`](internal/brief/templates/master.md),
and keep the variables you need:

| Variable | Content |
|---|---|
| `{{.RepoPath}}` | absolute path of the repo acw runs in |
| `{{.N}}` | how many workers acw may open at once (`workers`) |
| `{{.MinWorkers}}` | how many it keeps open with nothing queued (`min-workers`) |
| `{{.IdleCloseMinutes}}` | how long a free worker above `min-workers` stays open with nothing queued for it (`idle-close-minutes`) |
| `{{.WorkerAgent}}` | the workers' Herdr kind (`claude`, `codex`...) when they all share one; otherwise `mixed: ` followed by each worker's kind |
| `{{.WorkerNames}}` | the workers' Herdr names, comma-separated (`worker1-<slug>, worker2-<slug>`) |
| `{{.EnvCapRule}}` | the stack capacity rule: how many environments may be up, which acw enforces when it opens a worker in the code |
| `{{.StackProfileRule}}` | the wtm profile rule, empty when no `profile` is configured |
| `{{.RepoRules}}` | the `notes` file's content, empty when none is configured |
| `{{.PingingWorkers}}` | the names of the workers that ping the master on each turn (the `claude` ones, which have the Stop hook), empty when none does |
| `{{.WorkerOverrides}}` | each worker configured apart in `worker-overrides`: its kind, model and standing instructions in full, and whether the master must copy them into its briefs; empty when none is |
| `{{.InboxWatch}}` | the command the master must arm a Monitor on to receive pings and acw's messages, empty when the master is not `claude`. A custom brief that mentions neither it nor `{{.InboxNext}}` gets pings typed into the master's input, as before, with a warning at launch |
| `{{.InboxNext}}` | the command the master runs in the background to read its next messages, and runs again after each batch; empty when the master is not `claude`. The built-in brief uses this one |
| `{{.SilenceMinutes}}` | the `silence-minutes` value: how long a working worker may show no activity before acw's watcher tells the master |
| `{{.StatusCommand}}` | `acw status --repo <repo>`, fully written: every worker at a glance |
| `{{.QueueCommand}}` | `acw queue --repo <repo>`, fully written: lists the workers and the queue, and with `add`, `move` or `remove` changes it |
| `{{.DoneCommand}}` | `acw done --repo <repo>`, fully written, to follow with a worker's label (`worker2`): ends its task |
| `{{.SwitchCommand}}` | `wtm switch` when acw found it (wtm 0.26.0 or later) and the workers in the code get a stack; empty otherwise |
| `{{.PRWatch}}` | `true` when `pr-watch` is on: the master receives `PR #…` lines for the PRs that changed |

Before `worker-overrides`, a template could test `{{if eq .WorkerAgent
"claude"}}` to know whether pings come in. That still works when all
workers are Claude, but `{{if .PingingWorkers}}` is right for a mixed swarm
too.

The empty ones are meant for `{{if .StackProfileRule}}...{{end}}`, as the
built-in template does. A variable that doesn't exist (a typo) makes acw
refuse to start instead of sending a brief with a hole in it.

Only the brief is a template. The `notes` file is injected as is: a
`{{.WorkerNames}}` written in it stays literal.

A whole custom brief is a fork: it stops getting what the built-in one
learns. The two custom briefs that ran a test campaign were still arming
the Monitor and looping on `herdr agent wait` two releases later. When
only a mode differs, write that mode alone and point the `brief-extra`
config key at it: the file is appended to the brief (built-in, or custom
if `brief` is set too) and goes through the same template, with the same
variables. A preset is where it usually belongs:

```json
"presets": {
  "campaign": { "workers": 5, "brief-extra": "~/.config/acw/campaign-mode.md" }
}
```

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
| `min-workers` | `--min-workers` | `0` |
| `idle-close-minutes` | `--idle-close-minutes` | `10` |
| `max-stacks` | `--max-stacks` | same as `workers` |
| `master-kind` / `worker-kind` | `--master-kind` / `--worker-kind` | `claude` |
| `master-model` / `worker-model` | `--master-model` / `--worker-model` | `opus` / `sonnet`; `""` means no `--model`, like the flag |
| `brief` | `--brief` | built-in template |
| `brief-extra` | *(no flag)* | none: a template appended to the brief, see [Custom brief template](#custom-brief-template) |
| `profile` | *(no flag)* | none: the whole stack |
| `notes` | *(no flag)* | none |
| `worker-overrides` | *(no flag)* | none: every worker as above |
| `master-dir` | *(no flag)* | none: the master starts in the repo |
| `silence-minutes` | *(no flag)* | `30`: minutes a working worker may show no activity before acw's watcher tells the master |
| `pr-watch` | `--pr-watch` | `false` |
| `presets` | *(picked with `--preset`)* | none |

**`profile`** is one of the project's wtm profiles (`wtm project edit
--profile-set light=db,backend`). Workers' environments are adopted with
`--profile` set to it, and the master is told they run on a partial stack:
when a task needs services outside it, the master has that worker switch
profiles before it starts, then switches it back. A lighter profile is
also what lets a higher `max-stacks` fit in memory, so the two keys usually
move together. acw does not check the name; if wtm doesn't know it, that
worker's `wtm adopt` fails and says so in the watcher's log.

**`notes`** is a markdown file holding the repo's hard rules. Its content
goes **verbatim** into the master's brief, and into every claude worker's
system prompt, which survives `acw clear`: the master no longer copies it
into their briefs. For the other kinds, which have no system prompt acw can
set, the master is told to copy it, still verbatim, into every brief. No
notes means the master is just told to go find the conventions itself.

It's for the handful of rules that cost a force-push when missed — commit
message shape, where screenshots belong, the directory where a test must
never be committed — not for the project's whole documentation, which the
agents already read. Verbatim matters: a master reciting them from memory is
exactly how one gets dropped, and that happened for real.

Repo conventions are not the only rules that belong there. Name the
project's sources of truth too, and when each one must be read: the
decision log before any business arbitration, the design files before any
screen. The master is told these rules bind it as well, before it asks you
anything or dispatches a task. Left out, the reading happens late or not at
all: in real use a master put an option to the user that contradicted a
decision already locked, because nothing in its notes said to read the log
first.

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
  or the master will use the roles as interchangeable task runners. A mode
  that only adds to the everyday brief (a test campaign) is a
  `brief-extra` instead, which keeps following the built-in brief.
- One swarm per repo at a time, as without presets: switching is `acw
  stop`, then `acw --preset <other>`.
- Refused at launch: an unknown preset (the error lists the defined ones),
  `--preset` on a repo with no entry, and a preset inside a preset.

**Errors**: invalid JSON or an unknown key (`worker_model` for
`worker-model`) refuses to start and names the file. A `notes` file that
can't be read only warns, and the swarm starts without it.

### PR watch

With `pr-watch` on (`--pr-watch`, or `"pr-watch": true` in the repo's
entry), acw's watcher looks at your open non-draft pull requests on the
repo every 2 minutes, with one `gh api graphql` call, and sends the master
a line only for a PR that changed:

    PR #42 (worker2): conflict with base; review CHANGES_REQUESTED by alice; open threads 1 → 3 | https://github.com/some-org/some-repo/pull/42

What counts as a change: a conflict with the base or its resolution, a new
review, more unresolved review threads, CI turned red, CI green again after
a red, a head commit pushed by someone other than you (a reviewer, GitHub's
"Update branch"), a new PR, a PR merged, closed or turned back to draft. A
CI still running, a push of yours (workers push under your account), a
review or thread reply of yours, or a resolved thread is not. The worker is the one whose status file holds the
PR's `pr_url`. After 3 failed polls in a row the master is told once that
the watch is failing.

It needs `gh`, logged in to github.com, and an `origin` on GitHub: acw
refuses to start otherwise. A preset can turn it off for one mode:
`"presets": {"test-campaign": {"pr-watch": false}}`.

## How it works

- `cmd/acw` — the CLI: flags merged with the config, each worker's final
  kind/model/prompt, launching the master, and the detached watcher that
  runs the pool (worktrees, panes, agents, environments) from `pool.json`
  and `queue.json` in the status dir, both changed under one file lock.
- `internal/herdr` — thin wrapper around the `herdr` CLI (JSON in, typed Go out).
- `internal/wtm` — thin wrapper for giving/removing a worktree's environment,
  used only by this tool's own provisioning step (not by the brief text sent
  to agents — see below).
- `internal/gitutil` — fetch + default-branch detection + worktree creation/removal.
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
