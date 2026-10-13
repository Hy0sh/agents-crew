package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/wtm"
	"github.com/spf13/cobra"
)

// A worker in the code takes its stack for the minutes it needs it, with
// acw stack up, and gives it back with acw stack down: max-stacks counts
// these, so a worker never waits for a stack to open, only for one to
// test. acw gives it back itself once the worker is free (see schedule).

// stackWait is how long acw stack up waits for a place before giving the
// hand back: under the agent's ten-minute cap on a command.
const stackWait = 9 * time.Minute

func stackCommand() *cobra.Command {
	stack := &cobra.Command{
		Use:   "stack",
		Short: "Take or give back your worktree's stack (run by a worker, from its worktree)",
	}
	up := &cobra.Command{
		Use:   "up",
		Short: "Wait for a place under max-stacks, then start your worktree's stack",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, index, err := stackWorker()
			if err != nil {
				return err
			}
			return stackUp(repo, index, time.Now().Add(stackWait), 5*time.Second, cmd.OutOrStdout())
		},
	}
	down := &cobra.Command{
		Use:   "down",
		Short: "Stop your worktree's stack and give its place back; its database is kept",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, index, err := stackWorker()
			if err != nil {
				return err
			}
			p, err := readPoolFile(repo)
			if err != nil {
				return err
			}
			w := p.worker(index)
			if w == nil || w.StackSince == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "no stack held: nothing to stop.")
				return nil
			}
			if err := stackDown(repo, *w, cmd.OutOrStdout()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "stack stopped, its place is free; acw stack up brings it back, data as it was.")
			return nil
		},
	}
	stack.AddCommand(up, down)
	return stack
}

// stackWorker is the swarm's repo and the pool index of the worker whose
// worktree the current directory is in: <repo>/.claude/worktrees/<name>.
func stackWorker() (string, int, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", 0, err
	}
	top, err := gitutil.TopLevel(cwd)
	if err != nil {
		return "", 0, err
	}
	repo := filepath.Dir(filepath.Dir(filepath.Dir(top)))
	p, err := readPoolFile(repo)
	if err != nil {
		return "", 0, fmt.Errorf("%s is no worker's worktree: run acw stack from yours", top)
	}
	w := p.workerAt(top)
	if w == nil {
		return "", 0, fmt.Errorf("%s is no worker's worktree: run acw stack from yours", top)
	}
	if !w.Stacked {
		return "", 0, fmt.Errorf("%s has no wtm environment: there is no stack to start", w.label())
	}
	return repo, w.Index, nil
}

// stackUp takes a place for worker index as soon as one is free under
// max-stacks, then starts its stack. Past deadline it gives the hand back
// without one, saying so: the agent runs it again. A start that fails
// gives the place back.
func stackUp(repo string, index int, deadline time.Time, every time.Duration, out io.Writer) error {
	for told := false; ; {
		// Counted out of the lock: it may ask wtm.
		held := 0
		if p, err := readPoolFile(repo); err == nil {
			held = heldStacks(repo, p)
		}
		var w poolWorker
		var running, limit int
		got := false
		err := withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
			pw := p.worker(index)
			if pw == nil {
				return false, fmt.Errorf("this worker is no longer open")
			}
			p.Held = held
			running, limit = p.runningStacks(), p.Plan.MaxStacks
			if pw.StackSince == nil && running >= limit {
				return false, nil
			}
			if pw.StackSince == nil {
				now := time.Now()
				pw.StackSince = &now
			}
			w, got = *pw, true
			return true, nil
		})
		if err != nil {
			return err
		}
		if got {
			branch, err := gitutil.CurrentBranch(w.Worktree)
			if err == nil {
				err = wtm.Start(w.Worktree, branch, poolProfile(repo, index), out)
			}
			if err != nil {
				dropStack(repo, index)
				return fmt.Errorf("the stack did not start, its place is given back: %w", err)
			}
			fmt.Fprintln(out, "stack up: run acw stack down as soon as you are done with it.")
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(out, "still no free place (%d of %d stacks running): run acw stack up again, and work on what needs no stack meanwhile.\n", running, limit)
			return nil
		}
		if !told {
			told = true
			fmt.Fprintf(out, "max-stacks reached (%d of %d running): waiting for a place...\n", running, limit)
		}
		time.Sleep(every)
	}
}

// stackDown stops w's stack, then gives its place back: in that order, a
// place is never given while its stack still runs.
func stackDown(repo string, w poolWorker, out io.Writer) error {
	branch, err := gitutil.CurrentBranch(w.Worktree)
	if err == nil {
		err = wtm.Stop(w.Worktree, branch)
	}
	if err != nil {
		return fmt.Errorf("%s's stack was not stopped: %w", w.label(), err)
	}
	dropStack(repo, w.Index)
	fmt.Fprintf(out, "%s: stack stopped.\n", w.label())
	return nil
}

// dropStack records that worker index holds no stack any more: stopped,
// or dropped by a wtm switch.
func dropStack(repo string, index int) {
	err := withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		w := p.worker(index)
		if w == nil || w.StackSince == nil {
			return false, nil
		}
		w.StackSince = nil
		return true, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "giving a stack's place back:", err)
	}
}

// poolProfile is the stack profile worker index starts on: its own, else
// the swarm's.
func poolProfile(repo string, index int) string {
	p, err := readPoolFile(repo)
	if err != nil {
		return ""
	}
	return p.Plan.profileOf(index)
}
