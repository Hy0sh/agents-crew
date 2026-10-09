package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hy0sh/agents-crew/internal/board"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// record writes to the board, best effort: a base that can't be written
// is one line on stderr, never a reason for the swarm to stop.
// It reports whether the write went through.
func record(what string, write func(*board.DB) error) bool {
	b, err := board.Open(board.Path())
	if err == nil {
		err = write(b)
		b.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "board (%s): %v\n", what, err)
	}
	return err == nil
}

// prunedWorkers lists the workers of last that are not in labels any more.
func prunedWorkers(last map[string]board.Worker, labels []string) (gone []string) {
	open := map[string]bool{}
	for _, l := range labels {
		open[l] = true
	}
	for w := range last {
		if !open[w] {
			gone = append(gone, w)
		}
	}
	return gone
}

// addDecision records d, its text from args or, without any, from in.
// Unlike record, it returns the error: the master must know its decision
// was not kept.
func addDecision(d board.Decision, args []string, in io.Reader) error {
	text, err := readText(args, in, "decision")
	if err != nil {
		return err
	}
	d.Text, d.Why = text, strings.TrimSpace(d.Why)
	return withBoard(func(b *board.DB) error { return b.AddDecision(d) })
}

// openBrowser opens url, quietly: the URL is printed anyway.
func openBrowser(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	_ = exec.Command(name, url).Start()
}

func boardCommand() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "board",
		Short: "Open a local page of what waits on you, where each ticket stands, and how the swarm runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := board.Open(board.Path())
			if err != nil {
				return err
			}
			defer b.Close()
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				return err
			}
			url := "http://" + ln.Addr().String() + "/"
			fmt.Fprintf(cmd.OutOrStdout(), "acw board on %s (Ctrl-C stops it)\n", url)
			openBrowser(url)
			return http.Serve(ln, board.Handler(b, time.Now, notifyMaster))
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "port on 127.0.0.1 (default: a free one)")

	var worker, subject, why string
	var decisionRepo func() (string, error)
	decision := &cobra.Command{
		Use:   "decision [text...]",
		Short: "Record a decision on the board, read from stdin without text",
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := decisionRepo()
			if err != nil {
				return err
			}
			if worker != "" {
				_, label, err := workerName(repo, worker)
				if err != nil {
					return err
				}
				worker = label
			}
			if err := addDecision(board.Decision{Repo: repo, At: time.Now(), Worker: worker, Subject: subject, Why: why}, args, cmd.InOrStdin()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "decision recorded")
			return nil
		},
	}
	decisionRepo = repoFlag(decision)
	decision.Flags().StringVar(&worker, "worker", "", "the worker it concerns, by its name (worker2, reviewer1)")
	decision.Flags().StringVar(&subject, "subject", "", "what it is about: a ticket, a PR, a topic")
	decision.Flags().StringVar(&why, "why", "", "the reason, in one line")
	cmd.AddCommand(decision, parkCommand(), parkedCommand(), editCommand(), resumeCommand(), handoffCommand())
	return cmd
}

// notifyMaster writes msg to repo's master the way the watcher does: into
// its inbox when it watches one, typed into it otherwise.
func notifyMaster(repo, msg string) error {
	p, _, err := readPool(repo)
	if err != nil {
		return fmt.Errorf("no swarm running in %s: %w", repo, err)
	}
	return deliver(p.Plan.Inbox, names.Master(names.Slug(repo)), msg)
}

// readText is a command's text: its arguments, or stdin without any.
func readText(args []string, in io.Reader, what string) (string, error) {
	text := strings.Join(args, " ")
	if len(args) == 0 {
		content, err := io.ReadAll(in)
		if err != nil {
			return "", err
		}
		text = string(content)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("no %s: give it as arguments or on stdin", what)
	}
	return text, nil
}

func withBoard(f func(*board.DB) error) error {
	b, err := board.Open(board.Path())
	if err != nil {
		return err
	}
	defer b.Close()
	return f(b)
}

// acw board park puts a decision off until someone answers: the client, a
// third party, or the user (--on me). It lives in the base, so the master
// finds it again after a reset or in the next swarm, by its number.
func parkCommand() *cobra.Command {
	var ticket, on, worker string
	var repoOf func() (string, error)
	cmd := &cobra.Command{
		Use:   "park [question...]",
		Short: "Put a decision off until someone answers; the question, options and context to resume come on stdin",
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := repoOf()
			if err != nil {
				return err
			}
			if on == "" {
				return errors.New("--on: who the answer is waited from (client, me, a name)")
			}
			if worker != "" {
				_, label, err := workerName(repo, worker)
				if err != nil {
					return err
				}
				worker = label
			}
			text, err := readText(args, cmd.InOrStdin(), "question")
			if err != nil {
				return err
			}
			return withBoard(func(b *board.DB) error {
				id, err := b.Park(board.Parked{Repo: repo, Ticket: ticket, Worker: worker, On: on, Text: text, CreatedAt: time.Now()})
				if err == nil {
					fmt.Fprintf(cmd.OutOrStdout(), "#%d parked: the user answers with \"for #%d: ...\"\n", id, id)
				}
				return err
			})
		},
	}
	repoOf = repoFlag(cmd)
	cmd.Flags().StringVar(&ticket, "ticket", "", "the ticket key it belongs to")
	cmd.Flags().StringVar(&on, "on", "", "who the answer is waited from: client, me (the user), or a name")
	cmd.Flags().StringVar(&worker, "worker", "", "the worker it came from, by its name (worker2, reviewer1)")
	return cmd
}

// acw board handoff keeps what the master leaves for the next one: acw
// stop asks for it, acw start hands it to the next master.
func handoffCommand() *cobra.Command {
	var repoOf func() (string, error)
	cmd := &cobra.Command{
		Use:   "handoff [text...]",
		Short: "Leave the next master a handoff, read from stdin without text: acw start gives it to it",
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := repoOf()
			if err != nil {
				return err
			}
			text, err := readText(args, cmd.InOrStdin(), "handoff")
			if err != nil {
				return err
			}
			if err := withBoard(func(b *board.DB) error { return b.AddHandoff(repo, text, time.Now()) }); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "handoff kept: the next acw start gives it to the master")
			return nil
		},
	}
	repoOf = repoFlag(cmd)
	return cmd
}

const handoffAsk = "The swarm is being stopped (acw stop). Before it is, leave the next master your handoff: run `%s` with, on stdin through a single-quoted heredoc, what is still pending, ticket by ticket: where it stands, what or whom it waits on, its PRs and branches, what you meant to do next, and what must not be forgotten. The parked decisions are kept already: name them by number, do not copy them. Do nothing else: acw stops the swarm once the handoff is kept."

// askHandoff has a running master leave its handoff before acw stop tears
// the swarm down: once it is idle, it is asked, and the stop waits for
// the handoff up to wait. Without a master, or past wait, the stop goes on.
func askHandoff(repo, self string, wait time.Duration, out io.Writer) {
	name := names.Master(names.Slug(repo))
	agents, err := herdr.AgentList()
	if _, found := herdr.FindAgent(agents, name); err != nil || !found {
		return
	}
	start := time.Now()
	fmt.Fprintf(out, "asking the master for its handoff (up to %s; --no-handoff skips it)...\n", wait)
	if err := herdr.AgentWait(name, []string{"idle", "done"}, wait); err != nil {
		fmt.Fprintln(out, "warning: the master stayed busy, no handoff asked:", err)
		return
	}
	if err := herdr.AgentPrompt(name, fmt.Sprintf(handoffAsk, shellWord(self)+" board handoff --repo "+shellWord(repo))); err != nil {
		fmt.Fprintln(out, "warning: could not ask the master for its handoff:", err)
		return
	}
	for time.Since(start) < wait {
		time.Sleep(3 * time.Second)
		var kept bool
		if withBoard(func(b *board.DB) (err error) { kept, err = b.HandoffSince(repo, start); return err }) == nil && kept {
			fmt.Fprintln(out, "handoff kept.")
			return
		}
	}
	fmt.Fprintf(out, "warning: no handoff after %s, stopping anyway.\n", wait)
}

// handoffPrompt is what a new master is told of the previous one: its
// handoff, marked used, and the decisions still parked. "" with neither.
func handoffPrompt(repo string, now time.Time) string {
	var b strings.Builder
	_ = withBoard(func(db *board.DB) error {
		text, at, ok, err := db.TakeHandoff(repo, now)
		if err != nil {
			return err
		}
		if ok {
			fmt.Fprintf(&b, "The previous master left you this handoff on %s:\n\n%s\n", at.Local().Format("02/01 15:04"), text)
		}
		ps, err := db.OpenParked(repo)
		if err != nil || len(ps) == 0 {
			return err
		}
		b.WriteString("\nDecisions still parked:\n")
		for _, p := range ps {
			b.WriteString(renderParked(p, false))
		}
		return nil
	})
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "\nCheck it against what acw shows now, tell me in a few lines what is pending, then wait for my instructions."
}

func parkedCommand() *cobra.Command {
	var repoOf func() (string, error)
	cmd := &cobra.Command{
		Use:   "parked [number]",
		Short: "List the decisions still parked, or print one in full",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				id, err := parkedID(args[0])
				if err != nil {
					return err
				}
				return withBoard(func(b *board.DB) error {
					p, err := b.GetParked(id)
					if err == nil {
						fmt.Fprint(out, renderParked(p, true))
					}
					return err
				})
			}
			repo, err := repoOf()
			if err != nil {
				return err
			}
			return withBoard(func(b *board.DB) error {
				ps, err := b.OpenParked(repo)
				if err != nil {
					return err
				}
				if len(ps) == 0 {
					fmt.Fprintln(out, "no decision parked")
				}
				for _, p := range ps {
					fmt.Fprint(out, renderParked(p, false))
				}
				return nil
			})
		},
	}
	repoOf = repoFlag(cmd)
	return cmd
}

// parkedOf checks that parked decision id belongs to the repo --repo
// names, when it names one: the number alone is unique across repos, and
// a master quoting another swarm's number must not touch it.
func parkedOf(b *board.DB, cmd *cobra.Command, repoOf func() (string, error), id int64) error {
	if !cmd.Flags().Changed("repo") {
		return nil
	}
	repo, err := repoOf()
	if err != nil {
		return err
	}
	p, err := b.GetParked(id)
	if err == nil && p.Repo != repo {
		err = fmt.Errorf("#%d belongs to %s, not %s", id, p.Repo, repo)
	}
	return err
}

// acw board edit changes who a parked decision waits on: one parked on
// the user that in fact waits on the client must leave "Waiting on you"
// without a resume, which would record an answer nobody gave.
func editCommand() *cobra.Command {
	var on string
	var repoOf func() (string, error)
	cmd := &cobra.Command{
		Use:   "edit number --on <who>",
		Short: "Change who a parked decision waits on",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parkedID(args[0])
			if err != nil {
				return err
			}
			if on == "" {
				return errors.New("--on: who the answer is waited from (client, me, a name)")
			}
			return withBoard(func(b *board.DB) error {
				if err := parkedOf(b, cmd, repoOf, id); err != nil {
					return err
				}
				if _, err := b.SetParkedOn(id, on); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "#%d now waits on %s\n", id, on)
				return nil
			})
		},
	}
	repoOf = repoFlag(cmd)
	cmd.Flags().StringVar(&on, "on", "", "who the answer is waited from: client, me (the user), or a name")
	return cmd
}

func resumeCommand() *cobra.Command {
	var repoOf func() (string, error)
	cmd := &cobra.Command{
		Use:   "resume number [answer...]",
		Short: "Close a parked decision with its answer, read from stdin without one; the answer is also recorded as a decision",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parkedID(args[0])
			if err != nil {
				return err
			}
			answer, err := readText(args[1:], cmd.InOrStdin(), "answer")
			if err != nil {
				return err
			}
			return withBoard(func(b *board.DB) error {
				if err := parkedOf(b, cmd, repoOf, id); err != nil {
					return err
				}
				now := time.Now()
				p, err := b.CloseParked(id, answer, now)
				if err != nil {
					return err
				}
				if err := b.AddDecision(board.Decision{Repo: p.Repo, At: now, Worker: p.Worker, Subject: p.Ticket, Text: answer, Why: fmt.Sprintf("answer to parked #%d: %s", id, firstLine(p.Text))}); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "#%d closed, answer recorded as a decision. What it asked:\n%s\n", id, p.Text)
				return nil
			})
		},
	}
	repoOf = repoFlag(cmd)
	return cmd
}

// parkedID reads "7" or "#7".
func parkedID(arg string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%q is not a parked decision's number", arg)
	}
	return id, nil
}

// renderParked is one parked decision: its first line in a listing, all
// of it on its own.
func renderParked(p board.Parked, full bool) string {
	head := fmt.Sprintf("#%d", p.ID)
	if p.Ticket != "" {
		head += " " + p.Ticket
	}
	head += fmt.Sprintf(" · waits on %s since %s", p.On, p.CreatedAt.Local().Format("02/01 15:04"))
	if p.Worker != "" {
		head += " · from " + p.Worker
	}
	if !full {
		return head + "\n    " + firstLine(p.Text) + "\n"
	}
	if p.ClosedAt != nil {
		head += fmt.Sprintf(" · closed %s: %s", p.ClosedAt.Local().Format("02/01 15:04"), p.Answer)
	}
	return head + "\n" + p.Text + "\n"
}

// boardWorker is a worker's row: herdr's blocked first (it waits on
// someone), then, while it has a task, the state it gives itself, else
// the pool's. A free worker has no subject. Since is when that state
// began: state_since for the worker's own, which acw stamps; herdr's
// blocked is dated by changedWorkers. A <what>_ready waits on the user
// only once the turn is over: while herdr says working, the worker went
// on (an addendum, the go it got) and nothing is to approve yet.
func boardWorker(repo string, pw poolWorker, agentStatus string, s workerStatus, now time.Time) board.Worker {
	w := board.Worker{Repo: repo, Worker: pw.label(), State: pw.State, Since: pw.Since, UpdatedAt: now}
	if pw.State == workerBusy {
		w.Subject, w.Branch, w.PRURL, w.Summary, w.BlockedOn = s.Tache, s.Branch, s.PRURL, s.Summary, s.BlockedOn
		if s.State != "" {
			w.State = s.State
			if t, err := time.Parse(time.RFC3339, s.StateSince); err == nil {
				w.Since = t
			}
		}
		if agentStatus == "working" && strings.HasSuffix(w.State, "_ready") {
			w.State = "working"
		}
	}
	if agentStatus == "blocked" {
		w.State = "blocked"
	}
	return w
}

// changedWorkers returns the rows of cur that differ from last, UpdatedAt
// aside, and keeps last up to date: the base is written on a move only.
// herdr's blocked has no date of its own: it dates from the poll that
// first saw it.
func changedWorkers(last map[string]board.Worker, cur []board.Worker) []board.Worker {
	var out []board.Worker
	for _, w := range cur {
		prev, seen := last[w.Worker]
		if w.State == "blocked" {
			w.Since = w.UpdatedAt
			if seen && prev.State == "blocked" {
				w.Since = prev.Since
			}
		}
		prev.UpdatedAt = w.UpdatedAt
		if !seen || prev != w {
			out = append(out, w)
		}
		last[w.Worker] = w
	}
	return out
}

var pullURL = regexp.MustCompile(`/pull/(\d+)$`)

// prFromURL is a PR row from a worker's pr_url, for a swarm without the
// PR watch: number and link only.
func prFromURL(repo, url, worker string, now time.Time) (board.PR, bool) {
	url = normalizePRURL(url)
	m := pullURL.FindStringSubmatch(url)
	if m == nil || !strings.HasPrefix(url, "https://") {
		return board.PR{}, false
	}
	n, _ := strconv.Atoi(m[1])
	return board.PR{Repo: repo, Number: n, URL: url, Worker: worker, Status: "open", UpdatedAt: now}, true
}

// boardPR is a followed PR's row; fate, when set, is what became of a PR
// gone from the search (merged, closed...). held says a busy worker or a
// queued task is on it: review asks on a PR nobody holds are a hole the
// user must hear about. A ready or hole PR is dated now; the base keeps
// the first date.
func boardPR(repo string, pr prState, worker, fate string, held bool, now time.Time) board.PR {
	status := "open"
	if pr.Mergeable == "CONFLICTING" {
		status = "conflicting"
	}
	if fate != "" {
		status = fate
	}
	var review []string
	if pr.LastReview != "" {
		review = append(review, pr.LastReview)
	}
	if pr.OpenThreads > 0 {
		review = append(review, fmt.Sprintf("%d threads open", pr.OpenThreads))
	}
	row := board.PR{Repo: repo, Number: pr.Number, URL: pr.URL, Title: pr.Title, Worker: worker, Head: pr.Head, Base: pr.Base,
		Status: status, CI: pr.CI, Review: strings.Join(review, " · "), UpdatedAt: now}
	if status == "open" && pr.CI == "green" && strings.HasPrefix(pr.LastReview, "APPROVED") {
		row.SinceReady = now
	}
	if fate == "" && !held && (strings.HasPrefix(pr.LastReview, "CHANGES_REQUESTED") || pr.OpenThreads > 0) {
		row.SinceHole = now
	}
	return row
}

// heldPRs is what busy workers and queued tasks are on: the PR URLs and
// branches of the busy workers' status files, and the queued tasks'
// branches. A free worker's status still names its last PR: it holds
// nothing any more.
func heldPRs(statusDir string, pool poolState, queue taskQueue) (urls, branches map[string]bool) {
	urls, branches = map[string]bool{}, map[string]bool{}
	for _, pw := range pool.Workers {
		if pw.State != workerBusy {
			continue
		}
		s, _ := readWorkerStatus(filepath.Join(statusDir, pw.label()+".json"))
		if s.PRURL != "" {
			urls[normalizePRURL(s.PRURL)] = true
		}
		if s.Branch != "" {
			branches[s.Branch] = true
		}
	}
	for _, t := range queue.Tasks {
		if t.Branch != "" {
			branches[t.Branch] = true
		}
	}
	return urls, branches
}
