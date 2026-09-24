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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Hy0sh/agents-crew/internal/config"
	"github.com/Hy0sh/agents-crew/internal/preflight"
	"github.com/Hy0sh/agents-crew/internal/teardown"
	"github.com/Hy0sh/agents-crew/internal/version"
)

const provisionUse = "__provision-workers"

type startOptions struct {
	workers     int
	maxStacks   int
	masterKind  string
	workerKind  string
	masterModel string
	workerModel string
	briefPath   string
	preset      string                           // picks the config entry's variant, not a config key itself
	profile     string                           // from the per-project config only, no flag
	notesPath   string                           // same
	masterDir   string                           // same
	overrides   map[string]config.WorkerOverride // same
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
	setInt("max-stacks", &opts.maxStacks, p.MaxStacks)
	setStr("master-kind", &opts.masterKind, p.MasterKind)
	setStr("worker-kind", &opts.workerKind, p.WorkerKind)
	setStr("master-model", &opts.masterModel, p.MasterModel)
	setStr("worker-model", &opts.workerModel, p.WorkerModel)
	setStr("brief", &opts.briefPath, p.Brief)
	setStr("profile", &opts.profile, p.Profile)
	setStr("notes", &opts.notesPath, p.Notes)
	setStr("master-dir", &opts.masterDir, p.MasterDir)
	opts.overrides = p.WorkerOverrides
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
		return nil, fmt.Errorf("--preset %s: aucune entrée pour %s dans %s", preset, repo, config.Path())
	}
	return project.WithPreset(preset)
}

const rootLong = `Launches a Herdr workspace with one master agent supervising N worker
agents, in the current directory.

Per-project config (optional): ~/.config/acw/config.json, or
$XDG_CONFIG_HOME/acw/config.json. It lives outside the repo, so it works
where nothing may be committed. Entries are keyed by the directory acw is
launched from, keys are the flag names, plus five with no flag:

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
  presets           named variants of the entry, picked with --preset: same
                    keys, each one set replacing the entry's whole value
                    (worker-overrides included)

Paths accept ~, and a relative one is read from the repo, for a file the
team commits there.

Precedence: a flag given on the command line > the preset given with
--preset > the project's entry > the built-in default. No file or no
entry: acw behaves as without config. An unknown key refuses to start, so
a typo never goes unnoticed.`

func main() {
	opts := &startOptions{}

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
			workers, err := resolveWorkers(opts, cwd)
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

	root.Flags().IntVarP(&opts.workers, "workers", "n", 3, "number of worker agents (per-project: workers)")
	root.Flags().IntVar(&opts.maxStacks, "max-stacks", 0, "concurrent isolated environments the machine can hold (default: same as --workers) (per-project: max-stacks)")
	root.Flags().StringVar(&opts.masterKind, "master-kind", "claude", "herdr agent kind for the master (claude, codex, gemini...) (per-project: master-kind)")
	root.Flags().StringVar(&opts.workerKind, "worker-kind", "claude", "herdr agent kind for the workers (claude, codex, gemini...) (per-project: worker-kind)")
	root.Flags().StringVar(&opts.masterModel, "master-model", "opus", "model for the master agent; empty means no --model is passed to its CLI (per-project: master-model)")
	root.Flags().StringVar(&opts.workerModel, "worker-model", "sonnet", "model for worker agents; empty means no --model is passed to their CLI (per-project: worker-model)")
	root.Flags().StringVar(&opts.preset, "preset", "", "named preset of the per-project config entry, laid over it (see presets in the config)")
	root.Flags().StringVar(&opts.briefPath, "brief", "", "path to a custom master brief template (Go text/template), variables in the README; default: built-in template (per-project: brief)")

	stop := &cobra.Command{
		Use:   "stop",
		Short: "Tear down the running swarm (environments, status files, Herdr workspace)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := preflight.CheckStop(); err != nil {
				return err
			}
			return teardown.Run()
		},
	}

	// Internal: re-exec'd as a detached background process by runStart to
	// provision workers without delaying the Herdr TUI opening. Hidden from
	// --help and completion; not a documented interface.
	provision := &cobra.Command{
		Use:    provisionUse + " <plan-json>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var plan provisionPlan
			if err := json.Unmarshal([]byte(args[0]), &plan); err != nil {
				return fmt.Errorf("plan de provisioning illisible: %w", err)
			}
			provisionWorkers(plan)
			return nil
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

	root.AddCommand(stop, provision, inboxWatch, newDecisionCmd(), newJournalCmd(), newUICmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
