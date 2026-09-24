package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hy0sh/agents-crew/internal/decision"
	"github.com/Hy0sh/agents-crew/internal/journal"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// The decision queue is how a claude master asks the user something
// without blocking the conversation on it: it files the question with
// `acw __decision add`, the user answers from the UI in any order, and
// the answer comes back through the inbox.

const (
	decisionUse = "__decision"
	journalUse  = "__journal"
)

// decisionCommand is the command prefix the master files decisions with,
// or "" when it keeps asking in the conversation: same conditions as
// inboxWatchCommand, since the answer comes back through the inbox, plus
// a custom brief that never mentions {{.DecisionCmd}}.
func decisionCommand(masterKind, customBrief, exe, repo, inboxWatch string) (cmd string, warn bool) {
	if masterKind != "claude" || inboxWatch == "" {
		return "", false
	}
	if customBrief != "" && !strings.Contains(customBrief, ".DecisionCmd") {
		return "", true
	}
	return shellWord(exe) + " " + decisionUse + " --repo " + shellWord(repo), false
}

// journalCommand is the second Stop hook of worker label: it snapshots
// the worker's status file into the day's journal.
func journalCommand(exe, repo, label string) string {
	status := filepath.Join(names.StatusDir(repo), label+".json")
	return shellWord(exe) + " " + journalUse + " " + shellWord(status) + " " +
		shellWord(names.JournalDir(names.Slug(repo))) + " " + label
}

func newDecisionCmd() *cobra.Command {
	var repo string
	path := func() string { return names.DecisionsFile(names.Slug(repo)) }

	root := &cobra.Command{Use: decisionUse, Hidden: true}
	root.PersistentFlags().StringVar(&repo, "repo", "", "repo the swarm runs in")
	root.MarkPersistentFlagRequired("repo")

	var d decision.Decision
	var options []string
	var reco int
	add := &cobra.Command{
		Use:   "add",
		Short: "file a decision for the user; prints its ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, o := range options {
				label, consequence, _ := strings.Cut(o, "::")
				d.Options = append(d.Options, decision.Option{Label: strings.TrimSpace(label), Consequence: strings.TrimSpace(consequence)})
			}
			d.Reco = reco - 1
			added, err := decision.Add(path(), d, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), added.ID)
			return nil
		},
	}
	add.Flags().StringVar(&d.Task, "task", "", "the task it is about")
	add.Flags().StringVar(&d.Question, "question", "", "the question")
	add.Flags().StringArrayVar(&options, "option", nil, `an option, "label::consequence" (at least two)`)
	add.Flags().IntVar(&reco, "reco", 0, "the recommended option, from 1")
	add.Flags().StringVar(&d.Blocks, "blocks", "", "the worker waiting on it, if any")
	add.MarkFlagRequired("task")
	add.MarkFlagRequired("question")
	add.MarkFlagRequired("reco")

	show := &cobra.Command{
		Use:  "show <id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := decision.Get(path(), args[0])
			if err != nil {
				return err
			}
			printDecision(cmd.OutOrStdout(), d)
			return nil
		},
	}

	closeCmd := &cobra.Command{
		Use:   "close <id> <answer>",
		Short: "record the answer the user gave in the terminal",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := decision.Close(path(), args[0], args[1], "terminal", time.Now())
			if errors.Is(err, decision.ErrClosed) {
				printDecision(cmd.OutOrStdout(), d)
				return fmt.Errorf("%s est déjà %s : dis-le à l'utilisateur, ne la relaie pas deux fois", d.ID, d.Status)
			}
			return err
		},
	}

	root.AddCommand(add, show, closeCmd)
	return root
}

func printDecision(w io.Writer, d decision.Decision) {
	fmt.Fprintf(w, "%s (%s) : %s\nTâche : %s\n", d.ID, d.Status, d.Question, d.Task)
	if d.Blocks != "" {
		fmt.Fprintf(w, "Bloque : %s\n", d.Blocks)
	}
	for i, o := range d.Options {
		reco := ""
		if i == d.Reco {
			reco = " (recommandée)"
		}
		fmt.Fprintf(w, "  %d. %s%s : %s\n", i+1, o.Label, reco, o.Consequence)
	}
	if d.Status == decision.Answered {
		fmt.Fprintf(w, "Réponse (%s) : %s\n", d.AnsweredVia, d.Answer)
	}
}

// newJournalCmd is the Stop hook's journal half. It never fails the
// worker's turn: a missing or malformed status file just records nothing.
func newJournalCmd() *cobra.Command {
	return &cobra.Command{
		Use:    journalUse + " <status-file> <journal-dir> <worker>",
		Hidden: true,
		Args:   cobra.ExactArgs(3),
		Run: func(cmd *cobra.Command, args []string) {
			status, err := os.ReadFile(args[0])
			if err != nil {
				return
			}
			if err := journal.Record(args[1], args[2], status, time.Now()); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "journal:", err)
			}
		},
	}
}
