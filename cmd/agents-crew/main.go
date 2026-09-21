// agents-crew launches a dedicated Herdr workspace: one master agent
// supervising N worker agents, to dispatch and supervise tasks in a repo
// in parallel, project-agnostic.
//
// `agents-crew` (no subcommand) starts the swarm in the current directory;
// `agents-crew stop` tears it down. Closing the terminal does nothing —
// Herdr is a persistent server that outlives it, and so do any
// environments workers started.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hy0sh/agents-crew/internal/preflight"
	"github.com/Hy0sh/agents-crew/internal/teardown"
	"github.com/Hy0sh/agents-crew/internal/version"
)

const provisionUse = "__provision-workers"

type startOptions struct {
	workers     int
	maxStacks   int
	masterModel string
	workerModel string
	briefPath   string
}

func main() {
	opts := &startOptions{}

	root := &cobra.Command{
		Use:     "agents-crew",
		Short:   "Master + N worker Claude Code agents over Herdr, dispatching tasks in parallel",
		Version: version.String(),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := preflight.CheckStart(); err != nil {
				return err
			}
			preflight.WarnIfWtmMissing(func(format string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), format, a...) })
			return runStart(cmd.ErrOrStderr(), opts)
		},
	}
	root.SetVersionTemplate("agents-crew {{.Version}}\n")

	root.Flags().IntVarP(&opts.workers, "workers", "n", 3, "number of worker agents")
	root.Flags().IntVar(&opts.maxStacks, "max-stacks", 0, "concurrent isolated environments the machine can hold (default: same as --workers)")
	root.Flags().StringVar(&opts.masterModel, "master-model", "opus", "Claude model for the master agent")
	root.Flags().StringVar(&opts.workerModel, "worker-model", "sonnet", "Claude model for worker agents")
	root.Flags().StringVar(&opts.briefPath, "brief", "", "path to a custom master brief template (text/template, same fields as the built-in one); default: built-in template")

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
		Use:    provisionUse + " <repo> <masterPane> <stamp> <workers> <maxStacks> <workerModel>",
		Hidden: true,
		Args:   cobra.ExactArgs(6),
		RunE: func(cmd *cobra.Command, args []string) error {
			provisionWorkers(args)
			return nil
		},
	}

	root.AddCommand(stop, provision)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
