# Changelog

What changed between published versions, and why. Versions follow
[semantic versioning](https://semver.org): while the major stays at 0, a minor
bump carries new commands or new behaviour, a patch bump carries fixes.

## [Unreleased]

### Changed

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

- `agents-crew stop` called `wtm stop`/`wtm remove` from inside each
  worker's worktree with no branch argument, assuming the same
  current-directory inference `wtm adopt` documents. Verified against `wtm
  stop --help`: the branch is mandatory for `stop`/`remove`, unlike
  `adopt`. Left a stack running after a "torn down" swarm until this was
  fixed.

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

[Unreleased]: https://github.com/Hy0sh/agents-crew/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Hy0sh/agents-crew/releases/tag/v0.1.0
