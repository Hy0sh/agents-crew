package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Hy0sh/agents-crew/internal/board"
)

// reviewOptions are acw queue review's flags.
type reviewOptions struct {
	Kind, Note string
	Plan       int64 // a parked document, when the branch finds none or the wrong one
}

// queueReview queues the review of the work task id left on its branch.
// Its brief is generated from what the board kept: data only, never a
// list of points to check, which a reviewer takes for its scope.
func queueReview(repo string, id int, o reviewOptions, now time.Time, out io.Writer) error {
	p, q, err := readPool(repo)
	if err != nil {
		return err
	}
	if id < 1 || id > q.NextID {
		return fmt.Errorf("no task #%d was ever queued here (the last is #%d)", id, q.NextID)
	}
	if slices.ContainsFunc(q.Tasks, func(t queuedTask) bool { return t.ID == id }) ||
		slices.ContainsFunc(p.Workers, func(w poolWorker) bool { return w.State == workerBusy && w.Task == id }) {
		return fmt.Errorf("task #%d is not over: end it with acw done first", id)
	}
	var r reviewData
	err = withBoard(func(b *board.DB) error {
		h, ok, err := b.LastHandled(repo, id)
		if err != nil {
			return err
		}
		// ponytail: numbers start again with each swarm; a task removed
		// from this one's queue finds an older swarm's #id. The queued
		// line names the branch, which tells.
		if !ok {
			return fmt.Errorf("task #%d has no end on the board (ended before acw kept it, or removed): queue its review with acw queue add", id)
		}
		if h.Branch == "" {
			return fmt.Errorf("task #%d ended without a branch: queue its review with acw queue add", id)
		}
		r.Task = h
		if r.History, err = b.BranchHandled(repo, h.Branch); err != nil {
			return err
		}
		if o.Plan != 0 {
			if r.Plan, err = b.GetParked(o.Plan); err != nil {
				return err
			}
			if r.Plan.DocPath == "" {
				return fmt.Errorf("--plan %d: #%d has no document", o.Plan, o.Plan)
			}
		} else if r.Plan, _, err = b.BranchPlan(repo, h.Branch); err != nil {
			return err
		}
		if r.Ticket = board.TicketKey(h.Subject); r.Ticket == "" {
			r.Ticket = board.TicketKey(h.Brief)
		}
		if r.Ticket != "" {
			r.Decisions, err = b.TicketDecisions(repo, r.Ticket)
		}
		return err
	})
	if err != nil {
		return err
	}
	r.Kind, r.Note = o.Kind, strings.TrimSpace(o.Note)
	fmt.Fprintf(out, "review of task #%d (%s, on %s): ", id, r.Task.Worker, r.Task.Branch)
	return queueBrief(repo, r.brief(), addOptions{Branch: branchRequest{Branch: r.Task.Branch}, Kind: o.Kind}, now, out)
}

// reviewData is what a review brief is made of.
type reviewData struct {
	Task      board.Handled   // the task whose work is reviewed
	History   []board.Handled // every task ended on its branch, oldest first, Task included
	Plan      board.Parked    // the accepted plan, DocPath "" without
	Ticket    string
	Decisions []board.Decision
	Kind      string // this review's kind: the earlier tasks of that kind were reviews
	Note      string
}

func (r reviewData) brief() string {
	t := r.Task
	var s strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&s, format+"\n", a...) }
	head := "Review the work on branch `" + t.Branch + "`"
	if r.Ticket != "" {
		head += " for " + r.Ticket
	}
	if t.PRURL != "" {
		head += ", " + t.PRURL
	}
	line("%s.", head)
	line("")
	line("Your method is your role's. Everything below is data for it, none of it is the scope of your review: look at the whole change.")

	line("\n## The change\n")
	if t.Head != "" {
		line("- Branch `%s`, at %s when task #%d ended.", t.Branch, t.Head, t.Task)
	}
	if base := strings.TrimPrefix(t.Base, "origin/"); base != "" {
		line("- The diff to review: `git fetch origin && git diff origin/%s...HEAD`. The base is a branch and may have moved since: take it as it is now.", base)
	} else {
		line("- No base was recorded: take the PR's (`gh pr view --json baseRefName`), else the repo's default branch.")
	}
	var reviews, work []board.Handled
	for _, h := range r.History {
		if r.Kind != "" && h.Kind == r.Kind {
			reviews = append(reviews, h)
		} else {
			work = append(work, h)
		}
	}
	var last board.Handled
	if len(reviews) > 0 {
		last = reviews[len(reviews)-1]
		if last.Head != "" {
			line("- Reviewed before at %s: read `git diff %s..HEAD` too, what changed since.", last.Head, last.Head)
		}
	}

	line("\n## The plan\n")
	if r.Plan.DocPath != "" {
		line("%s (accepted, parked #%d).", r.Plan.DocPath, r.Plan.ID)
	} else {
		line("No accepted plan was found for this branch.")
	}

	line("\n## Decisions recorded, which override the plan\n")
	if len(r.Decisions) == 0 {
		line("None recorded for %s.", or(r.Ticket, "this branch"))
	}
	for _, d := range r.Decisions {
		why := ""
		if d.Why != "" {
			why = " (" + d.Why + ")"
		}
		line("- %s: %s%s", d.At.Local().Format("02/01 15:04"), d.Text, why)
	}

	line("\n## What the workers on this branch were told\n")
	line("Their briefs, oldest first: the adjustments given along the way are here, not only in the plan.")
	for _, h := range work {
		line("\n### Task #%d, %s, ended %s: %s\n", h.Task, h.Worker, h.At.Local().Format("02/01 15:04"), or(h.Summary, h.Outcome))
		line("%s", or(strings.TrimSpace(h.Brief), "(its brief was not kept)"))
	}

	if last.Task != 0 {
		line("\n## The previous review\n")
		line("Task #%d by %s: %s. Check that each of its findings is dealt with.", last.Task, last.Worker, or(last.Proof, "no file recorded, see its summary: "+last.Summary))
	}

	line("\n## Proof\n")
	if t.Proof != "" {
		line("%s. A proof older than the branch's last commit proves nothing: check the dates.", t.Proof)
	} else {
		line("No proof path was recorded.")
	}

	if r.Note != "" {
		line("\n## From the master, on top of your method\n")
		line("%s", r.Note)
	}
	return strings.TrimSpace(s.String())
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
