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

	var o addOptions
	add := &cobra.Command{
		Use:   "add <brief-file> [--branch <b>] [--base <ref>] [--worker <worker>] [--kind <kind>] [--after <id>]... [--after-merge <id>]... [--top]",
		Short: "Queue a task: the brief is copied in, the file may change afterwards",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := repo()
			if err != nil {
				return err
			}
			return queueAdd(r, args[0], o, time.Now(), cmd.OutOrStdout())
		},
	}
	add.Flags().StringVar(&o.Branch.Branch, "branch", "", "put the worker on this branch first (a fix or a rebase on a known branch), after a git fetch")
	add.Flags().StringVar(&o.Branch.Base, "base", "", "where --branch is cut from when it doesn't exist yet (default: the repo's default branch on origin)")
	add.Flags().StringVar(&o.Worker, "worker", "", "only this worker may take it (a fix after a KO goes back to the worker that has the context)")
	add.Flags().StringVar(&o.Kind, "kind", "", "only the workers whose worker-overrides tasks list this kind take it (a reviewer for need-review)")
	add.Flags().IntSliceVar(&o.After, "after", nil, "hold it until this task is ended with acw done (repeatable): a rebase that needs the pushed result of the task before it")
	add.Flags().IntSliceVar(&o.AfterMerge, "after-merge", nil, "hold it until the PR this task ended with is merged (repeatable; needs pr-watch): a follow-up that builds on the merged code")
	add.Flags().BoolVar(&o.Top, "top", false, "first in the queue instead of last")

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
	var ro reviewOptions
	review := &cobra.Command{
		Use:   "review <id> [--kind <kind>] [--note <text>] [--plan <parked>]",
		Short: "Queue the review of an ended task's branch, its brief made from the board: plan, decisions, briefs, proof",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := repo()
			if err != nil {
				return err
			}
			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("%q is not a task number", args[0])
			}
			return queueReview(r, id, ro, time.Now(), cmd.OutOrStdout())
		},
	}
	review.Flags().StringVar(&ro.Kind, "kind", "", "only the workers whose tasks list this kind take it (need-review); its earlier tasks of that kind on the branch are the previous reviews")
	review.Flags().StringVar(&ro.Note, "note", "", "a precise risk to look at, given on top of the reviewer's method, never as its scope")
	review.Flags().Int64Var(&ro.Plan, "plan", 0, "the parked document that is the plan, when the branch finds none or the wrong one")
	queue.AddCommand(add, move, remove, review)
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
