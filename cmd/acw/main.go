// acw (agents-crew) launches a dedicated Herdr workspace: one master
// agent supervising N worker agents, to dispatch and supervise tasks in
// a repo in parallel, project-agnostic.
//
// `acw` (no subcommand) starts the swarm in the current directory; `acw
// stop` tears it down. Closing the terminal does nothing — Herdr is a
// persistent server that outlives it, and so do any environments
// workers started.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Hy0sh/agents-crew/internal/board"
	"github.com/Hy0sh/agents-crew/internal/config"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/preflight"
	"github.com/Hy0sh/agents-crew/internal/teardown"
	"github.com/Hy0sh/agents-crew/internal/version"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

type startOptions struct {
	workers     int
	minWorkers  int
	maxStacks   int
	masterKind  string
	workerKind  string
	masterModel string
	workerModel string
	briefPath   string
	preset      string       // picks the config entry's variant, not a config key itself
	profile     string       // from the per-project config only, no flag
	notesPath   string       // same
	extraPath   string       // same: brief-extra
	masterDir   string       // same
	roles       config.Roles // same
	// silenceMinutes: same, see config.Project.SilenceMinutes.
	silenceMinutes int
	// idleCloseMinutes: same, see config.Project.IdleCloseMinutes.
	idleCloseMinutes int
	// masterAutocompact: see config.Project.MasterAutocompact.
	masterAutocompact int
	prWatch           bool
}

// applyConfig copies the project entry's values into opts, except for
// flags the user gave on the command line: flag > config > built-in.
// changed is cmd.Flags().Changed — "given", not "differs from the
// default", so `--worker-model sonnet` still wins over a config saying
// haiku.
func applyConfig(opts *startOptions, p *config.Project, changed func(string) bool) {
	setInt := func(flag string, dst *int, v *int) {
		if v != nil && !changed(flag) {
			*dst = *v
		}
	}
	setStr := func(flag string, dst *string, v *string) {
		if v != nil && !changed(flag) {
			*dst = *v
		}
	}
	setInt("workers", &opts.workers, p.Workers)
	setInt("min-workers", &opts.minWorkers, p.MinWorkers)
	setInt("max-stacks", &opts.maxStacks, p.MaxStacks)
	setStr("master-kind", &opts.masterKind, p.MasterKind)
	setStr("worker-kind", &opts.workerKind, p.WorkerKind)
	setStr("master-model", &opts.masterModel, p.MasterModel)
	setStr("worker-model", &opts.workerModel, p.WorkerModel)
	setStr("brief", &opts.briefPath, p.Brief)
	setStr("profile", &opts.profile, p.Profile)
	setStr("notes", &opts.notesPath, p.Notes)
	setStr("brief-extra", &opts.extraPath, p.BriefExtra)
	setStr("master-dir", &opts.masterDir, p.MasterDir)
	setInt("silence-minutes", &opts.silenceMinutes, p.SilenceMinutes)
	setInt("idle-close-minutes", &opts.idleCloseMinutes, p.IdleCloseMinutes)
	setInt("master-autocompact", &opts.masterAutocompact, p.MasterAutocompact)
	if p.PRWatch != nil && !changed("pr-watch") {
		opts.prWatch = *p.PRWatch
	}
	opts.roles = p.Roles
}

// checkCounts refuses counts the pool cannot run with: workers is how
// many may be open, min-workers how many stay open, so 1 <= workers and
// 0 <= min-workers <= workers. With roles, the config checked their own,
// and --workers or --min-workers given on top would be ignored: refused.
func checkCounts(opts *startOptions, changed func(string) bool) error {
	roles := len(opts.roles) > 0
	switch {
	case roles && (changed("workers") || changed("min-workers")):
		return errors.New("the config sets roles: --workers and --min-workers don't apply, change the roles' max and min")
	case !roles && opts.workers < 1:
		return fmt.Errorf("workers is %d: at least 1, it is how many workers acw may open", opts.workers)
	case !roles && (opts.minWorkers < 0 || opts.minWorkers > opts.workers):
		return fmt.Errorf("min-workers is %d: from 0 to workers (%d)", opts.minWorkers, opts.workers)
	case opts.idleCloseMinutes < 0:
		return fmt.Errorf("idle-close-minutes is %d: 0 or more", opts.idleCloseMinutes)
	case opts.masterAutocompact != 0 && (opts.masterAutocompact < 100_000 || opts.masterAutocompact > 1_000_000):
		// Claude Code's own range for --autocompact.
		return fmt.Errorf("master-autocompact is %d: 0, or from 100000 to 1000000", opts.masterAutocompact)
	}
	return nil
}

// completeFlags offers the root's flags on a bare Tab, next to the
// subcommands: cobra only lists flags once a "-" is typed. A started word
// is left to cobra, which completes subcommands (and flags after "-").
func completeFlags(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if toComplete != "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var flags []cobra.Completion
	cmd.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed && !f.Hidden {
			flags = append(flags, cobra.CompletionWithDesc("--"+f.Name, f.Usage))
		}
	})
	return flags, cobra.ShellCompDirectiveNoFileComp
}

// loadProject returns the repo's config entry, with the preset laid over
// it when one is named. A preset asked for on a repo with no entry is an
// error, not a silent launch with the defaults.
func loadProject(repo, preset string) (*config.Project, error) {
	project, err := config.Load(repo)
	if err != nil || preset == "" {
		return project, err
	}
	if project == nil {
		return nil, fmt.Errorf("--preset %s: no entry for %s in %s", preset, repo, config.Path())
	}
	return project.WithPreset(preset)
}

const rootLong = `Launches a Herdr workspace with one master agent supervising N worker
agents, in the current directory.

Per-project config (optional): ~/.config/acw/config.json, or
$XDG_CONFIG_HOME/acw/config.json. It lives outside the repo, so it works
where nothing may be committed. Entries are keyed by the directory acw is
launched from, keys are the flag names, plus seven with no flag:

  {
    "projects": {
      "/path/to/repo": {
        "workers": 4,
        "profile": "light",
        "notes": "~/.config/acw/repo.md",
        "worker-overrides": {
          "3": {"kind": "codex", "model": "gpt-5-codex", "prompt": "verifier.md"}
        }
      }
    }
  }

  profile           wtm stack profile workers start on (default: the whole
                    stack)
  brief-extra       template appended to the master's brief, built-in or
                    custom: a mode (a test campaign...) without a fork of
                    the whole brief
  notes             markdown file copied verbatim into the master's brief,
                    then into every worker's: the repo's hard rules
  worker-overrides  per worker index (1 to workers): its own kind, model,
                    and prompt, a file of standing instructions (system
                    prompt for a claude worker, copied into each of its
                    briefs by the master otherwise), and dir, an absolute
                    folder outside the repo that makes it a worker outside
                    the code: it starts there, with no worktree,
                    environment or branch
  master-dir        absolute folder outside the repo the master starts in
                    (default: the repo)
  silence-minutes   how long a working worker may show no activity (no turn
                    end, no status update, no file changed) before acw tells
                    the master (default: 30)
  presets           named variants of the entry, picked with --preset: same
                    keys, each one set replacing the entry's whole value
                    (worker-overrides included)

Paths accept ~, and a relative one is read from the repo, for a file the
team commits there.

Precedence: a flag given on the command line > the preset given with
--preset > the project's entry > the built-in default. No file or no
entry: acw behaves as without config. An unknown key refuses to start, so
a typo never goes unnoticed.`

// repoOrCwd is the swarm's repo for a command the master may run from
// elsewhere: the one given, else the current directory.
func repoOrCwd(repo string) (string, error) {
	if repo != "" {
		return filepath.Abs(repo)
	}
	return os.Getwd()
}

// repoFlag adds --repo to cmd and returns what resolves it (see
// repoOrCwd).
func repoFlag(cmd *cobra.Command) func() (string, error) {
	var repo string
	cmd.Flags().StringVar(&repo, "repo", "", "the swarm's repo (default: the current directory); the master runs from elsewhere with master-dir")
	return func() (string, error) { return repoOrCwd(repo) }
}

// readWatchPlan reads the plan file the watcher is started with (see
// launchBackgroundWatch).
func readWatchPlan(path string) (watchPlan, error) {
	var plan watchPlan
	content, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(content, &plan)
	}
	if err != nil {
		return plan, fmt.Errorf("unreadable watcher plan: %w", err)
	}
	return plan, nil
}

// restartWatcher starts the watcher of repo's swarm again from its kept
// plan, when none runs: one killed (a stray pkill, a crash) used to leave
// the queue undispatched until acw stop took the whole swarm down.
func restartWatcher(repo string, out io.Writer) error {
	if watcherRunning(repo) {
		fmt.Fprintln(out, "the watcher is running: nothing to do.")
		return nil
	}
	plan, err := readWatchPlan(names.WatchPlan(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no watcher plan in %s: this swarm was started by an acw older than the one that keeps it; acw stop then acw start", names.StatusDir(repo))
	}
	if err != nil {
		return err
	}
	if err := launchBackgroundWatch(plan); err != nil {
		return err
	}
	fmt.Fprintf(out, "watcher started again, its log: %s\n", filepath.Join(os.TempDir(), fmt.Sprintf("acw-watch-%s.log", plan.Stamp)))
	return nil
}

// withWtm runs a stack command on the swarm of the current directory,
// which needs wtm: without it no worker ever had a stack to stop.
func withWtm(run func(repo string) error) error {
	if !wtm.Available() {
		return fmt.Errorf("wtm not found in PATH: the workers have no stack to stop or start")
	}
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	return run(repo)
}

func main() {
	opts := &startOptions{silenceMinutes: 30, idleCloseMinutes: 10}

	root := &cobra.Command{
		Use:     "acw",
		Short:   "Master + N worker coding agents over Herdr, dispatching tasks in parallel",
		Long:    rootLong,
		Version: version.String(),
		Args:    cobra.NoArgs,
		// Flags on a bare Tab; see completeFlags.
		ValidArgsFunction: completeFlags,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			project, err := loadProject(cwd, opts.preset)
			if err != nil {
				return fmt.Errorf("config acw: %w", err)
			}
			if project != nil {
				applyConfig(opts, project, cmd.Flags().Changed)
				summary := project.Summary()
				if opts.preset != "" {
					summary = strings.TrimSuffix(fmt.Sprintf("preset=%q, %s", opts.preset, summary), ", ")
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "config: %s → %s\n", config.Path(), summary)
			}
			if err := checkCounts(opts, cmd.Flags().Changed); err != nil {
				return fmt.Errorf("config acw: %w", err)
			}
			workers, err := buildSlots(opts, cwd)
			if err != nil {
				return fmt.Errorf("config acw: %w", err)
			}
			if opts.masterDir != "" {
				if _, err := validateAgentDir(cwd, opts.masterDir); err != nil {
					return fmt.Errorf("config acw: master-dir: %w", err)
				}
			}
			if err := preflight.CheckStart(append([]string{opts.masterKind}, distinctKinds(workers)...)...); err != nil {
				return err
			}
			preflight.WarnIfWtmMissing(func(format string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), format, a...) })
			return runStart(cmd.ErrOrStderr(), cwd, opts, workers)
		},
	}
	root.SetVersionTemplate("acw {{.Version}}\n")
	// main prints the error itself; cobra printing it too showed it twice.
	root.SilenceErrors = true
	// Runs once flags and args are validated: a bad flag still gets the
	// usage, an error from the command itself (a config typo, a missing
	// dependency) gets only its message instead of being buried under it.
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) { cmd.SilenceUsage = true }

	root.Flags().IntVarP(&opts.workers, "workers", "n", 3, "most worker agents open at once, opened as tasks are queued (per-project: workers)")
	root.Flags().IntVar(&opts.minWorkers, "min-workers", 0, "workers kept open with nothing queued; --workers for a fixed swarm (per-project: min-workers)")
	root.Flags().IntVar(&opts.idleCloseMinutes, "idle-close-minutes", 10, "how long a free worker above --min-workers stays open with nothing queued for it (per-project: idle-close-minutes)")
	root.Flags().IntVar(&opts.maxStacks, "max-stacks", 0, "concurrent isolated environments the machine can hold, which also caps the workers in the code (default: same as --workers) (per-project: max-stacks)")
	root.Flags().StringVar(&opts.masterKind, "master-kind", "claude", "herdr agent kind for the master (claude, codex, gemini...) (per-project: master-kind)")
	root.Flags().StringVar(&opts.workerKind, "worker-kind", "claude", "herdr agent kind for the workers (claude, codex, gemini...) (per-project: worker-kind)")
	root.Flags().StringVar(&opts.masterModel, "master-model", "opus", "model for the master agent; empty means no --model is passed to its CLI (per-project: master-model)")
	root.Flags().StringVar(&opts.workerModel, "worker-model", "sonnet", "model for worker agents; empty means no --model is passed to their CLI (per-project: worker-model)")
	root.Flags().IntVar(&opts.masterAutocompact, "master-autocompact", 250_000, "context size in tokens, 100000 to 1000000, at which a claude master compacts; 0 leaves Claude Code's own (per-project: master-autocompact)")
	root.Flags().StringVar(&opts.preset, "preset", "", "named preset of the per-project config entry, laid over it (see presets in the config)")
	root.Flags().BoolVar(&opts.prWatch, "pr-watch", false, "follow your open non-draft pull requests on this repo and tell the master what changed on them; needs gh, logged in (per-project: pr-watch)")
	root.Flags().StringVar(&opts.briefPath, "brief", "", "path to a custom master brief template (Go text/template), variables in the README; default: built-in template (per-project: brief)")

	var noHandoff bool
	var handoffWait time.Duration
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Have the master leave its handoff, then tear down the running swarm (environments, status files, Herdr workspace)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := preflight.CheckStop(); err != nil {
				return err
			}
			if !noHandoff {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				self, err := os.Executable()
				if err != nil {
					return err
				}
				askHandoff(cwd, self, handoffWait, cmd.OutOrStdout())
			}
			if err := teardown.Run(); err != nil {
				return err
			}
			// teardown works on the current directory.
			if cwd, err := os.Getwd(); err == nil {
				record("stop", func(b *board.DB) error { return b.KeepWorkers(cwd, nil) })
			}
			return nil
		},
	}
	stop.Flags().BoolVar(&noHandoff, "no-handoff", false, "stop without asking the master for its handoff")
	stop.Flags().DurationVar(&handoffWait, "handoff-wait", 5*time.Minute, "how long to wait for the master's handoff")

	var statusRepo func() (string, error)
	status := &cobra.Command{
		Use:   "status",
		Short: "Show every worker at a glance: state, status age, activity, context, quota, inbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := statusRepo()
			if err != nil {
				return err
			}
			rows, unread, lastAt, err := collectStatus(repo)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), renderStatus(time.Now(), rows, unread, lastAt, watcherRunning(repo)))
			return nil
		},
	}
	statusRepo = repoFlag(status)

	var clearRepo func() (string, error)
	clearCmd := &cobra.Command{
		Use:   "clear <worker>...",
		Short: "Reset workers' context before a new task, and confirm it took",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := clearRepo()
			if err != nil {
				return err
			}
			for _, label := range args {
				if err := clearWorker(repo, label, cmd.OutOrStdout()); err != nil {
					return err
				}
			}
			return nil
		},
	}
	clearRepo = repoFlag(clearCmd)

	var dispatchRepo func() (string, error)
	var br branchRequest
	dispatch := &cobra.Command{
		Use:   "dispatch <worker> <brief-file> [--branch <b>] [--base <ref>]",
		Short: "Give a worker its next task: wait until it is idle, reset its context, type the brief",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := dispatchRepo()
			if err != nil {
				return err
			}
			return dispatchByHand(repo, args[0], args[1], br, cmd.OutOrStdout())
		},
	}
	dispatchRepo = repoFlag(dispatch)
	dispatch.Flags().StringVar(&br.Branch, "branch", "", "put the worker on this branch first (a fix or a rebase on a known branch), after a git fetch")
	dispatch.Flags().StringVar(&br.Base, "base", "", "where --branch is cut from when it doesn't exist yet (default: the repo's default branch on origin)")

	queue := queueCommand()

	var doneRepo func() (string, error)
	done := &cobra.Command{
		Use:   "done <worker> [task-id]",
		Short: "Mark a worker's task as finished: acw gives it the next one, or closes it",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := doneRepo()
			if err != nil {
				return err
			}
			index, doneLabel, err := workerName(repo, args[0])
			if err != nil {
				return err
			}
			task := 0
			if len(args) == 2 {
				if task, err = strconv.Atoi(strings.TrimPrefix(args[1], "#")); err != nil {
					return fmt.Errorf("%q is not a task id", args[1])
				}
			}
			return finishWorker(repo, index, doneLabel, task, cmd.OutOrStdout())
		},
	}
	doneRepo = repoFlag(done)

	var tellRepo func() (string, error)
	var slash string
	tell := &cobra.Command{
		Use:   "tell <worker> [message...]",
		Short: "Leave a worker a message, read from stdin without one: it gets it at the end of its turn, or at once when idle",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := tellRepo()
			if err != nil {
				return err
			}
			if slash != "" {
				if len(args) > 1 {
					return errors.New("--command takes no message: the command is all that is typed")
				}
				return commandWorker(repo, args[0], slash, cmd.OutOrStdout())
			}
			return tellByHand(repo, args[0], args[1:], cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	tellRepo = repoFlag(tell)
	tell.Flags().StringVar(&slash, "command", "", "type this slash command (e.g. /reload-plugins) into the worker's input once it is idle, instead of a message")

	pause := &cobra.Command{
		Use:   "pause",
		Short: "Stop the workers' stacks for a break; worktrees, agents and workspace stay",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withWtm(func(repo string) error { return pauseStacks(repo, cmd.OutOrStdout()) })
		},
	}
	resume := &cobra.Command{
		Use:   "resume",
		Short: "Start the workers' stacks again, on the profile the swarm was launched with",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withWtm(func(repo string) error { return resumeStacks(repo, cmd.OutOrStdout()) })
		},
	}

	// Internal: what the master's Monitor runs (see inboxWatchCommand).
	inboxWatch := &cobra.Command{
		Use:    inboxWatchUse + " <inbox>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return watchInbox(args[0], cmd.OutOrStdout(), 500*time.Millisecond)
		},
	}

	// Internal: acw's watcher, detached by runStart (see runWatch).
	watch := &cobra.Command{
		Use:    watchUse + " <plan-file>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := readWatchPlan(args[0])
			if err != nil {
				return err
			}
			runWatch(plan, 5*time.Second)
			return nil
		},
	}

	var watchRepo func() (string, error)
	restartWatch := &cobra.Command{
		Use:   "watch",
		Short: "Start the swarm's watcher again when it is not running, without touching the swarm",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := watchRepo()
			if err != nil {
				return err
			}
			return restartWatcher(repo, cmd.OutOrStdout())
		},
	}
	watchRepo = repoFlag(restartWatch)

	// Internal: what the master runs in the background (see nextInbox).
	inboxNext := &cobra.Command{
		Use:    inboxNextUse + " <inbox>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return nextInbox(args[0], cmd.OutOrStdout(), 500*time.Millisecond, inboxQuiet)
		},
	}

	// Internal: a claude worker's status line (see recordUsage).
	statusLine := &cobra.Command{
		Use:    statusLineUse + " <status-dir> <worker>",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return recordUsage(filepath.Join(args[0], args[1]+".usage.json"), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}

	// Internal: what a claude worker's Stop hook runs (see turnEndCommand).
	turnEnd := &cobra.Command{
		Use:    turnEndUse + " <status-dir> <worker> <inbox> <master>",
		Hidden: true,
		Args:   cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			return turnEnd(args[0], args[1], args[2], args[3], time.Now(), cmd.OutOrStdout())
		},
	}

	root.AddCommand(stop, status, queue, done, projectCommand(), boardCommand(), tell, clearCmd, dispatch, pause, resume, restartWatch, watch, inboxWatch, inboxNext, turnEnd, statusLine)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
