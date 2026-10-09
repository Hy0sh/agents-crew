# Changelog

What changed between published versions, and why. Versions follow
[semantic versioning](https://semver.org): while the major stays at 0, a minor
bump carries new commands or new behaviour, a patch bump carries fixes.

## [Unreleased]

## [0.18.0] - 2026-10-09

### Added

- `acw stop` asks the running master for its handoff before tearing the
  swarm down: once the master is idle, acw asks it, and waits up to
  `--handoff-wait` (5 min) for `acw board handoff`, which keeps it in the
  board base. The next `acw start` on the repo puts it in the new
  master's first prompt, with the decisions still parked, and marks it
  used. What was pending used to go through a file the user kept by
  hand. `--no-handoff` stops without asking.
- `acw board edit <number> --on <who>` changes who a parked decision
  waits on. One parked on you that in fact waits on the client had to be
  closed with `resume`, which recorded an answer nobody gave, then
  parked again under a new number.

### Changed

- `acw done` no longer moves the worker's worktree: it frees the worker
  and returns at once. The watcher puts a free worker back on its waiting
  branch in the background, its `wtm switch` output in the watcher's log,
  and tells the master only when it can't. The master's turn used to block
  on the whole stack rebuild, and read its docker output.
- A task given `--branch` goes first to the free worker still on that
  branch, and a free worker stays on its branch while a queued task is
  for it: `acw done` then a follow-up on the same branch costs no stack
  round trip, and needs no `--worker`. The master brief no longer asks for
  `--worker workerN --branch` to keep a rebase on its worker.

### Fixed

- `acw board`: a worker's `*_ready` no longer shows in "Waiting on you"
  while its turn goes on (herdr says working): one that set
  `verdict_ready` and went on with an addendum for half an hour was
  listed, and marked done for nothing.
- A message from the master (`acw tell`, a dispatch) sets a worker's
  `*_ready` state back to `working`: the worker kept `plan_ready` while
  coding the go it got, and the plan stayed on the page as still to
  approve. A worker still waiting sets it again, with a new
  `state_since`.
- `acw board resume` takes `--repo` like the other `board` commands, and
  refuses, with `--repo`, a number that belongs to another repo.

## [0.17.1] - 2026-10-09

### Security

- Built with Go 1.27.2, which fixes five `net/http` and HTTP/2
  vulnerabilities of 1.27.1 (memory exhaustion on the server side among
  them). `acw board` serves HTTP on 127.0.0.1.

### Fixed

- `acw board`: Done on a PR's line (ready for your merge, review asks
  nobody holds) keeps it out of "Waiting on you" until the PR changes on
  GitHub: a new review, a thread opened or resolved, CI, a conflict. It
  used to come back two minutes later, flagged, while the PR still showed
  `CHANGES_REQUESTED`, which stays until the reviewer reads again, often
  hours after the asks were answered elsewhere. Each new Done also sent
  the master one more "marked done" line for the same PR; a PR's line
  already marked tells it once. Its ticket now waits on the reviewers,
  not on nobody. Marks are kept two weeks instead of a day, for a
  reviewer who takes days.

## [0.17.0] - 2026-10-08

### Added

- `acw queue add --after <id>` (repeatable) holds a task until the tasks
  it names are ended with `acw done`, then lets any free worker take it.
  A follow-up that needs the pushed result of the task before it (the
  `rebase --onto` of a stacked PR) used to be kept aside by hand, or
  reserved with `--worker` for that same worker even with others free.
  Removing a task it waits for holds it, with that reason.
- `worker-overrides` takes `tasks` and `keep`, and `acw queue add` takes
  `--kind`: a reviewer listing `need-review` gets every task queued
  `--kind need-review` and nothing else, and no general-purpose worker
  takes those. `keep: true` opens it with the swarm and never closes it
  for being idle, so a review does not wait for a worktree and a stack.
- `acw board` answers what waits on you, where each ticket stands and how
  the swarm runs, instead of listing workers, PRs and decisions flat. A
  "Waiting on you" block comes first, oldest first: plans, verdicts and
  review drafts to approve (`*_ready`), a worker stopped on a prompt, a PR
  ready for your merge, review asks nobody holds, decisions parked on you.
  Tickets are read from branches, PR titles and tasks (`PROJ-123`), with
  their stage, whom they wait on, their PRs and their decisions.
- A Done button on each waiting line tells the master "the user marked
  done", as information, never as an approval. The line comes back,
  flagged, if the wait is still there two minutes later.
- `acw board park`, `parked` and `resume`: the master puts off a decision
  that waits on the client or on you, frees the worker, and picks it up
  again by number, in this swarm or a later one, when you answer
  "for #N: ...".
- `acw tell workerN --command /reload-plugins` types a slash command into
  an idle worker. Sent as a message, it was only ever read as text.

### Changed

- `acw done` puts the worker back on its waiting branch before freeing it,
  and a task given `--branch` that a free worker still holds takes it
  back. A worker freed after asking for a review kept its branch checked
  out, and git gives a branch to one worktree only: the reviewer, then
  whoever fixed what it found, could not check it out. A worker with
  uncommitted changes is left where it is, and the master told.
- The board's banner says how long ago the watcher last reported. It said
  "Live" whenever the page reloaded, even with the watcher dead.
- `acw status` shows what a worker's screen says it waits on: a tool
  running, background shells and monitors.
- A draft marked ready for review reaches the master as such, no longer as
  a new PR.
- A worker that stops for an approval sets its state to `plan_ready`,
  `verdict_ready` or `review_ready`.

### Fixed

- `--branch` on a branch that exists locally brings it up to origin's, or
  refuses it when they diverged. A stale copy cut by another session was
  rebased, and its force-push would have erased commits merged since.
- Two workers taking a task at once no longer both fail on
  `cannot lock ref`: acw's fetches take turns.
- A worker under load was reported "idle without finishing its turn" when
  its turn had ended: its Stop hook ran while herdr still said working.
- A worker waiting on a long tool or on background work writing outside
  its worktree is no longer reported silent.

### Upgrading

- The board's base gains tables and columns. Restart running swarms after
  upgrading: a watcher started by an older acw can no longer write its
  workers to it.

## [0.16.0] - 2026-10-08

### Changed

- A claude master compacts at 250000 tokens, set with
  `--master-autocompact` or the `master-autocompact` key (`0` leaves
  Claude Code's own window). Left at the model's 1M window, a day-long
  master ended past 800K, and every turn re-read all of it.
- A claude master gets its brief as a system prompt, and only a short
  kickoff as its first prompt. Sent as the first prompt, the brief was
  summarized by every compaction, its rules with it.
- A claude master is denied `Edit`, `Write` and `NotebookEdit`: it had
  coded a task itself instead of dispatching it.

## [0.15.0] - 2026-10-07

### Changed

- `acw project create` and `acw project edit` are now one command, which
  makes the entry when the repo has none and changes it when it has one.
  `create` on a repo with an entry used to fail and send you to `edit`,
  which meant knowing what the config already held.
- The README is reorganised: a quick start, one section per command, how
  the swarm runs, then the config. Nothing in it changed meaning.

### Fixed

- `pr-watch` refused a repo whose `origin` is on a GitHub Enterprise host
  with "origin is not a GitHub remote", even with `gh` logged in there:
  acw read the remote itself and only knew github.com. It now asks `gh
  repo view` for the repo, so any host gh is logged in to works, and a
  refusal gives gh's reason, with `gh auth login --hostname` to run or how
  to turn `pr-watch` off. With an `upstream` remote, the repo followed is
  the upstream one, as gh picks it (`gh repo set-default` changes it).
- A worktree an earlier run left behind took a place in `max-stacks`
  after its stack had been removed outside acw (`wtm remove` run by hand):
  a swarm then opened fewer workers than `min-workers`, and the master took
  the missing ones for still opening. Such a worktree now stops counting
  once `wtm list` shows it without a stack.
- When `max-stacks` keeps the pool under `min-workers`, the master is told
  once, with the stacks held outside the pool and how to find them.

## [0.14.0] - 2026-10-07

### Added

- `acw status` says when the watcher is no longer running. A dead watcher
  dispatched nothing more and opened no worker, with no sign anywhere: the
  master went on waiting.

## [0.13.1] - 2026-10-07

### Fixed

- `acw stop` with the master gone (Herdr crashed, its pane closed by hand)
  said there was nothing to stop and left the workers, their worktrees and
  their stacks running. It now releases them, and closes the workers'
  workspace when Herdr still lists them.

## [0.13.0] - 2026-10-06

A page to see what the swarms did, and a command to stop hand-editing the
config.

### Added

- `acw board`: a local read-only page of the swarms' workers, followed
  pull requests, decisions and handled tasks, per repo and per day, from
  a SQLite base acw fills as it goes. The master records decisions with
  `acw board decision`.
- `acw project create` / `acw project edit`: write a repo's config entry
  from flags, or one question per key without any. Files given to `brief`,
  `brief-extra` and `notes` are copied next to the config.

### Changed

- The master's brief says that a text after `❯` in a worker's pane may be
  only a greyed suggestion: `acw tell` already tells it apart, so the
  master tells anyway instead of asking.
- A worker's system prompt says that a pull request stacked on another one
  stays on that base until it is merged.
- A worker name acw does not know says that the worker and what follows
  are separate arguments (`"worker1 14"` from a shell loop).

## [0.12.0] - 2026-10-05

From a full day of a master driving five workers: most of the friction
was in how the master talks to a worker, and in what it had to repeat.

### Added

- `acw tell workerN`: a message for a worker, on stdin or as arguments.
  It goes out at the worker's next turn end, handed over by its Stop hook
  without typing anything, or typed in by the watcher when the worker is
  already idle, never over a non-empty input line (the master is told
  instead). The master may run it without a prompt.
- Every claude worker gets its role in its system prompt: it reports to
  the master only, its status file and fields, temporary files in `/tmp`.
  It survives the reset before each task, so briefs carry the task only.

### Changed

- A turn end with nothing moved in the worker's status no longer pings
  the master, unless it answers a brief or a message from it, or nothing
  moved for 15 minutes.
- `acw __inbox-next` waits 2 seconds after a first message for the ones
  right behind it: one wake-up per burst.
- The message about a closed worker names the branch it leaves with
  commits no remote holds.

### Fixed

- The master's brief told it to run `herdr agent prompt workerN`, which
  herdr answers with `agent_not_found`: herdr names the workers
  `workerN-<slug>`. `acw status` now shows that name, and the brief uses
  it for `herdr agent read` and `acw tell` for messages.

## [0.11.0] - 2026-10-04

The swarm is now elastic: no worker at launch, a task queue the master
orders, and acw opening and closing workers by fixed rules. What to do and
in which order stays the master's call; where and when it runs is no
longer left to a model. A worker's stack is tracked across runs and never
left running behind a deleted worktree; with wtm 0.27.0, acw also reaches
the stack of a worker that switched branches without `wtm switch`.

### Added

- A task queue held by acw: `acw queue` lists the workers and the queue,
  `acw queue add <brief-file>` (with `--top`, `--worker workerN`,
  `--branch`, `--base`), `move` and `remove` change it. acw's watcher hands
  the tasks out in that order, with the same steps as `acw dispatch`. A
  task it could not hand out is held first in the queue with the reason
  until the master moves or removes it.
- `acw done workerN`: the master ends a task once it checked its result;
  only then does the worker get another, or close.
- `min-workers` (default 0) and `idle-close-minutes` (default 10), keys and
  flags: workers kept open with nothing queued, and how long a free one
  above that stays open. `min-workers` equal to `workers` is the fixed
  swarm of before.

- `state_since` in a claude worker's status file, stamped by `acw
  __turn-end`: the turn end at which the current `state` was first seen.
  Workers waiting in the same state (a review, a demo, an arbitration) can
  be taken in order from the files rather than from the master's memory,
  which a compaction loses. `acw status` shows it next to the state.

### Changed

- `workers` is a ceiling: acw opens a worker when a task waits and none is
  free, up to it, and for a worker in the code when the repo has wtm
  stacks, up to `max-stacks`; it closes a worker free for
  `idle-close-minutes` with nothing queued for it, never under
  `min-workers` and never with changes in its worktree. The master no
  longer arbitrates environments.
- A worker set apart in `worker-overrides` only takes the tasks queued for
  it with `--worker`.
- A worker's worktree is named after the moment it opens, not the run's
  start: a reopened worker never collides with the branch its first
  opening left.
- The master may run `acw status`, `acw queue` and `acw done` without a
  prompt, no longer `acw clear` nor `acw dispatch`: acw does both when it
  hands a task out. `acw dispatch` stays for you, and marks the worker busy
  until `acw done`.
- A custom brief loses `{{.ClearCommand}}`, `{{.DispatchCommand}}` and
  `{{.StackedWorkers}}`, and gains `{{.QueueCommand}}`, `{{.DoneCommand}}`,
  `{{.MinWorkers}}` and `{{.IdleCloseMinutes}}`. One still naming a removed
  variable is refused at launch, before anything starts.

### Removed

- The "workers ready" message and the provisioning at launch: each opening
  is its own message.

### Fixed

- Workers' stacks come up one `wtm adopt` at a time. wtm checks a new
  index's ports against the other worktrees' from a registry read before
  it locks it: adopts run together each missed the other, took
  neighbouring indices, and with a port stride of 1 two services got the
  same host port, one stack failing to start.
- A worktree moved to another branch without `wtm switch` no longer loses
  its stack: wtm finds none under the new branch, which acw took for a
  worker never given one, then removed the worktree while the stack ran
  on. For a worker wtm gave a stack to, acw stop and a close keep the
  worktree and say how to get the stack back; pause and resume fail on it.
- acw stop and a close remove a worker's stack even when it was stopped
  (acw pause, a reboot): they ran `wtm remove` only after a successful
  `wtm stop`, so a stopped stack stayed in wtm's list. One forced `wtm
  remove` now, which takes the stack down running or not.
- That a worktree has a stack is recorded in its private git dir, not in
  pool.json, which acw stop deletes: the next run's acw stop still keeps a
  worktree whose stack it could not remove. Before keeping one, acw also
  asks wtm for the stack under the branch it adopted the worktree under;
  kept stacks count in `max-stacks`. A detached HEAD, a project wtm no
  longer knows and wtm missing from PATH each get their own repair.
- A failed `wtm adopt` is undone: it left an index, volumes and sometimes
  containers behind.
- Workers run `wtm switch --profile <profile>` when a profile is set: wtm
  does not remember it, and a worker's first switch brought the whole
  stack up.
- `acw done workerN <id>` refuses a task the worker is no longer on, so a
  second done cannot free it from the next task.
- acw stop waits for the watcher to finish what it started before tearing
  the workers down.
- An unreadable queue.json no longer makes the watcher drop its run.
- `--branch` on a worker with a stack is refused when wtm has no `switch`
  (before 0.26.0), instead of a plain `git switch` that left its stack
  behind.
- A branch or a base starting with a dash is refused: it reached git as
  an option.
- A repo reached through a symlink is recognised as registered in wtm.
- The "opened" message only carries the port clashes the worker takes
  part in, not every clash on the machine.
- A claude master whose custom brief reads no inbox may still run `acw
  status`, `acw queue` and `acw done` without a prompt.

## [0.10.0] - 2026-10-02

A worker now gets its task in one call, carries the repo rules in its
system prompt, and, with wtm 0.26.0, starts each task on a fresh stack
through `wtm switch`. wtm stays optional: without it nothing changes but
the single call. `acw stop` no longer deletes a worker's task branch.

### Added

- `acw dispatch workerN <brief-file>`: the wait, the confirmed reset and
  the brief in one call, instead of `acw clear` then `herdr agent prompt`.
  With `--branch`, the worker is first put on a known branch, through
  `wtm switch` when it has a stack and wtm has it (0.26.0), else `git
  switch`. A branch held by another worktree is refused; nothing is ever
  stashed.
- Workers with a wtm stack are told to create their task branch with
  `wtm switch`, and may run it without a prompt: `git switch -c` left their
  stack registered under the old branch, and the workaround removed the
  folder they worked in.
- A `waiting_subagent` state in the status contract, for a worker whose
  own subagent is still running.

### Changed

- Claude workers get the repo's `notes` in their system prompt, which
  survives a context reset: the master no longer copies them into every
  brief, where they drifted from one brief to the next.

### Fixed

- `acw stop` no longer force-deletes the branch a worker is on. It ran
  `git branch -D` on each worktree's current branch, which is the worker's
  last task branch: unpushed work was lost with it. Only the
  `agents/workerN-…` branch acw cut is deleted now, and only when nothing
  was committed on it.
- A worker is only counted as having a wtm stack when the repo is in wtm's
  registry and its adopt succeeded. On a repo wtm doesn't know, workers
  were told to create branches with `wtm switch`, which refused every time.

## [0.9.0] - 2026-10-02

acw can now follow your open pull requests for the master. With
`pr-watch` on, the watcher it already runs tells the master, in the same
inbox, when a PR got a conflict, a review, CI red, someone else's push, or
was merged or closed, and stays silent the rest of the time.

### Added

- A `pr-watch` config key and `--pr-watch` flag: acw's watcher follows
  your open non-draft pull requests on the repo and tells the master what
  changed on them (conflict, review, open threads, CI red or green again,
  someone else's push, merged or closed), in the same inbox. A master that
  ran its own PR polling script next to `__inbox-next` had two listeners
  to keep alive, and two REST calls per PR every cycle.

## [0.8.0] - 2026-10-01

acw now speaks English: its output, its errors, the messages it sends the
master and the built-in brief. The master still talks to you in your own
language, the one you write to it in. A custom brief is left as it is, but
one that quotes acw's messages (the ping, the idle inbox note) must follow
their new wording, and a mode can now be appended to the built-in brief
instead of forking it.

### Added

- A `brief-extra` config key: a template appended to the master's brief,
  built-in or custom. A mode such as a test campaign no longer needs a
  fork of the whole brief, which stopped getting every change made to the
  built-in one: two such forks still looped on `herdr agent wait` two
  releases after acw replaced it.
- A claude worker's turn-end ping says what moved in its status file since
  its previous ping: `state` before and after, or "status unchanged". Most
  pings ended turns the master had triggered itself, and each cost it a
  read of the file to find nothing new.
- Once the stacks are up, acw runs `wtm doctor` and adds its port clash
  sections to the "workers ready" message. A worker's stack failed to
  start on a clash doctor only reported afterwards.

### Fixed

- `acw clear` on a worker that has not finished a turn yet returns at once,
  saying its context is already empty. It used to send `/clear`, which keeps
  the session_id of a session never used, and fail after 60 s every first
  dispatch.
- `acw __inbox-next` exits after 25 minutes without a message, printing
  "nothing new". Claude Code kills a background command at its timeout (30
  minutes by default), which the brief said never happened, and the master
  had to notice and run it again by hand.

### Changed

- Everything acw prints or sends is in English, the built-in brief
  included, which also tells the master to write to the user in the user's
  language. The code and its comments were English already.
- The master's brief now asks it to: pose every question through its
  agent's choice tool, never at the end of a status point in prose; read
  the project's business and design sources itself before asking or
  dispatching; have each worker list the files it will touch before coding,
  to catch two tasks building the same module; check a PR body for leftover
  `<!--` and local paths before announcing it; carry a stacked PR over a
  rewritten base with `git rebase --onto`; and send worker prompts through a
  quoted heredoc, since double quotes let the shell run backticks out of
  them. The notes of the acw config are said to bind the master too.
- The README says the `notes` file should also name the project's sources
  of truth and when to read them.

## [0.7.0] - 2026-09-30

The master now asks its questions through its agent's choice tool, where it
used to write them out in prose on some projects. No breaking change: a
custom brief is left as it is.

### Changed

- The master's brief has it pose every decision it hands the user, its own
  and those a worker raises, through its agent's choice tool when it has one:
  one question per decision, its recommendation first, options numbered when
  the agent has no such tool. Asked in prose, a decision buried in a status
  point went unanswered, and whether the master reached for the tool depended
  on the project it ran in.

## [0.6.0] - 2026-09-25

acw now tells the master what it used to go and look for: a watcher reports
blocked, stuck and silent workers, the master reads its messages with a
background command that never expires, worker status files carry
timestamps acw sets itself, and four new commands (`status`, `clear`,
`pause`, `resume`) replace what the master and the user did by hand. No
breaking change: a custom brief that arms a Monitor on `{{.InboxWatch}}`
keeps working.

### Added

- A watcher, started with the swarm, that tells the master when a worker
  becomes blocked (with the last lines of its pane, and a hint from that
  worker's second block since its last `acw clear`), when a claude worker
  goes idle without its Stop hook having run (prompts herdr shows as idle),
  when a working worker shows no activity for `silence-minutes` (new config
  key, default 30: no turn end, status update or file change in its
  worktree), and when a worker without the Stop hook hands control back. It
  stops with its swarm.
- `acw __inbox-next`, which the master runs in the background to read its
  messages: it waits for the next ones, prints them and exits. acw types a
  reminder into the master's input when messages wait unread for 5
  minutes.
- A claude worker's `Stop` hook now normalizes its status file before
  pinging the master: `updated_at` from the file's real modification time
  in UTC, a new `last_turn_end`, and `blocked_on` cleared once `state` no
  longer says blocked, in whatever words. A master can now tell a worker
  that works without updating its status from one that stopped.
- The master's brief tells workers to get their ports from the project's
  environment tooling instead of asking the underlying container tool
  (which froze workers on a permission prompt), to keep temporary files in
  `/tmp` rather than `rm -rf` inside the worktree (a prompt that denies
  itself after a few minutes), and to check that no proof file is staged
  before a push. Two status fields join the list: `base_branch`, to see
  stacked PRs at a glance, and `ports`.
- `acw status`: every worker at a glance (herdr state, status age, last
  turn end, worktree activity, context, 5-hour quota, branch and base, PR)
  and the master's unread inbox.
- `acw clear workerN`: waits for the worker to be idle, refuses a blocked
  one, sends `/clear` and returns once a new session is reported, instead
  of the master reading the pane (which once showed a render from before
  the clear). It starts the watcher's block count over.
- `acw pause` / `acw resume`: stop the workers' stacks for a break and start
  them again on the launch profile, leaving worktrees and agents alone.
- The master's brief hands it `acw status` and `acw clear` fully written
  (`{{.StatusCommand}}`, `{{.ClearCommand}}`), and it is started allowed to
  run them.

### Changed

- A claude worker's status line is now acw's (`ctx 34% · 5h 78%`), in
  place of the user's: it records the worker's context and quota for `acw
  status` and `acw clear`.
- The built-in brief no longer has the master keep an `agent wait` running
  on every worker, nor re-arm a Monitor every 30 minutes: the watcher and
  `__inbox-next` replace both. `{{.InboxWatch}}` still works for a custom
  brief that arms a Monitor; new variables `{{.InboxNext}}` and
  `{{.SilenceMinutes}}`.
- A status file is now rewritten atomically, so the watcher never reads a
  half-written one.

## [0.5.0] - 2026-09-24

Several ways to run one repo, agents that start outside the code, and
worker pings that no longer land in the middle of what you type to the
master. No breaking change.

### Added

- `presets` in a project's config entry, picked with `--preset <name>`:
  named variants of the entry, e.g. a planner, coders and a reviewer next to
  the everyday multitask swarm. A preset takes the entry's keys; each one it
  sets replaces the entry's whole value, `worker-overrides` included, so a
  preset never inherits roles written for another composition. Precedence
  becomes flag > preset > entry > built-in. An unknown preset, a `--preset`
  on a repo with no entry, and a preset inside a preset refuse to start.
- A working directory per agent: `master-dir`, and `dir` in
  `worker-overrides`, e.g. an agent in a folder whose `.claude` brings the
  project's product tooling. `dir` makes a worker one outside the code: no
  worktree, environment or branch, no `max-stacks` slot, and the master is
  told never to give it code. Whoever codes stays in a worktree: a folder
  inside the repo is refused. Each agent loads the instructions of the
  folder it starts in. When an agent never becomes ready, acw names
  Claude Code's trust prompt for a new folder as the likely cause.
- Shell completion offers acw's flags on a bare Tab, next to the
  subcommands, instead of only once a `-` is typed; flags already on the
  command line are left out. `--brief`'s help no longer inlines every
  template variable (they are in the README), so it fits on one line.

### Changed

- acw finds the master by its name instead of its pane's directory, for
  `acw stop` and the "already running" check, so a master started in
  `master-dir` is found too.

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

[Unreleased]: https://github.com/Hy0sh/agents-crew/compare/v0.18.0...HEAD
[0.18.0]: https://github.com/Hy0sh/agents-crew/compare/v0.17.1...v0.18.0
[0.17.1]: https://github.com/Hy0sh/agents-crew/compare/v0.17.0...v0.17.1
[0.17.0]: https://github.com/Hy0sh/agents-crew/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/Hy0sh/agents-crew/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/Hy0sh/agents-crew/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/Hy0sh/agents-crew/compare/v0.13.1...v0.14.0
[0.13.1]: https://github.com/Hy0sh/agents-crew/compare/v0.13.0...v0.13.1
[0.13.0]: https://github.com/Hy0sh/agents-crew/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/Hy0sh/agents-crew/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/Hy0sh/agents-crew/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/Hy0sh/agents-crew/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/Hy0sh/agents-crew/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/Hy0sh/agents-crew/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/Hy0sh/agents-crew/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/Hy0sh/agents-crew/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/Hy0sh/agents-crew/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/Hy0sh/agents-crew/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/Hy0sh/agents-crew/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Hy0sh/agents-crew/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Hy0sh/agents-crew/releases/tag/v0.1.0
