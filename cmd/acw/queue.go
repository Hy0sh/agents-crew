package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// queueCommand is acw queue and its subcommands: the master's hold on the
// order tasks go out in. Bare, it lists the workers and the queue.
func queueCommand() *cobra.Command {
	var repo func() (string, error)
	queue := &cobra.Command{
		Use:   "queue",
		Short: "Show the workers and the tasks waiting for one, in the order they go out",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := repo()
			if err != nil {
				return err
			}
			p, q, err := readPool(r)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), renderQueue(time.Now(), p, q))
			return nil
		},
	}
	// Persistent: `acw queue add --repo ...` as well as `acw queue --repo`.
	var repoPath string
	queue.PersistentFlags().StringVar(&repoPath, "repo", "", "the swarm's repo (default: the current directory); the master runs from elsewhere with master-dir")
	repo = func() (string, error) { return repoOrCwd(repoPath) }

	var br branchRequest
	var worker, kind string
	var after, afterMerge []int
	var top bool
	add := &cobra.Command{
		Use:   "add <brief-file> [--branch <b>] [--base <ref>] [--worker workerN] [--kind <kind>] [--after <id>]... [--after-merge <id>]... [--top]",
		Short: "Queue a task: the brief is copied in, the file may change afterwards",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := repo()
			if err != nil {
				return err
			}
			return queueAdd(r, args[0], br, worker, kind, after, afterMerge, top, time.Now(), cmd.OutOrStdout())
		},
	}
	add.Flags().StringVar(&br.Branch, "branch", "", "put the worker on this branch first (a fix or a rebase on a known branch), after a git fetch")
	add.Flags().StringVar(&br.Base, "base", "", "where --branch is cut from when it doesn't exist yet (default: the repo's default branch on origin)")
	add.Flags().StringVar(&worker, "worker", "", "only this worker may take it (a fix after a KO goes back to the worker that has the context)")
	add.Flags().StringVar(&kind, "kind", "", "only the workers whose worker-overrides tasks list this kind take it (a reviewer for need-review)")
	add.Flags().IntSliceVar(&after, "after", nil, "hold it until this task is ended with acw done (repeatable): a rebase that needs the pushed result of the task before it")
	add.Flags().IntSliceVar(&afterMerge, "after-merge", nil, "hold it until the PR this task ended with is merged (repeatable; needs pr-watch): a follow-up that builds on the merged code")
	add.Flags().BoolVar(&top, "top", false, "first in the queue instead of last")

	move := &cobra.Command{
		Use:   "move <id> <position>",
		Short: "Put a task at a position (1 goes out next); a held task may go again",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := repo()
			if err != nil {
				return err
			}
			id, pos, err := twoInts(args)
			if err != nil {
				return err
			}
			return queueMove(r, id, pos, cmd.OutOrStdout())
		},
	}
	remove := &cobra.Command{
		Use:   "remove <id>",
		Short: "Take a task out of the queue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := repo()
			if err != nil {
				return err
			}
			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("%q is not a task id", args[0])
			}
			return queueRemove(r, id, cmd.OutOrStdout())
		},
	}
	queue.AddCommand(add, move, remove)
	return queue
}

func twoInts(args []string) (int, int, error) {
	a, err := strconv.Atoi(args[0])
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a task id", args[0])
	}
	b, err := strconv.Atoi(args[1])
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a position", args[1])
	}
	return a, b, nil
}
