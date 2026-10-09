package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	cmd.AddCommand(decision, parkCommand(), parkedCommand(), editCommand(), resumeCommand())
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
// docKind is what a worker stopped in state waits approval of.
func docKind(state string) string {
	switch state {
	case "plan_ready":
		return "plan"
	case "verdict_ready":
		return "verdict"
	case "review_ready":
		return "review draft"
	}
	return "document"
}

// checkDocPath refuses a document the user could not read later: not an
// absolute path to a file, or under the repo's worktrees dir, where the
// worktrees and the status dir go at acw stop.
func checkDocPath(repo, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("--doc %s: give the absolute path", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("--doc: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("--doc %s: not a file", path)
	}
	if rel, err := filepath.Rel(names.WorktreesDir(repo), path); err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("--doc %s: inside a worktree or the status dir, both gone at acw stop: have the worker write it outside the repo (e.g. ~/.claude/plans)", path)
	}
	return nil
}

func parkCommand() *cobra.Command {
	var ticket, on, worker, doc string
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
			index := 0
			if worker != "" {
				i, label, err := workerName(repo, worker)
				if err != nil {
					return err
				}
				index, worker = i, label
			}
			kind, branch := "", ""
			if doc != "" {
				if worker == "" {
					return errors.New("--doc needs --worker: parking its document frees it")
				}
				if on != "me" {
					return errors.New("--doc goes with --on me: only the user reads documents, on acw board")
				}
				if err := checkDocPath(repo, doc); err != nil {
					return err
				}
				s, _ := readWorkerStatus(filepath.Join(names.StatusDir(repo), worker+".json"))
				kind, branch = docKind(s.State), s.Branch
			}
			text, err := readText(args, cmd.InOrStdin(), "question")
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			err = withBoard(func(b *board.DB) error {
				id, err := b.Park(board.Parked{Repo: repo, Ticket: ticket, Worker: worker, On: on, Text: text, Kind: kind, DocPath: doc, Branch: branch, CreatedAt: time.Now()})
				if err == nil {
					fmt.Fprintf(out, "#%d parked: the user answers with \"for #%d: ...\"\n", id, id)
				}
				return err
			})
			if err != nil || doc == "" {
				return err
			}
			// The user may take days to read it: the worker waits on nothing.
			// It is parked already: an error here would have it parked twice.
			if err := finishWorker(repo, index, worker, 0, out); err != nil {
				fmt.Fprintf(out, "warning: %s not freed: %v\n", worker, err)
			}
			return nil
		},
	}
	repoOf = repoFlag(cmd)
	cmd.Flags().StringVar(&ticket, "ticket", "", "the ticket key it belongs to")
	cmd.Flags().StringVar(&on, "on", "", "who the answer is waited from: client, me (the user), or a name")
	cmd.Flags().StringVar(&worker, "worker", "", "the worker it came from, by its name (worker2, reviewer1)")
	cmd.Flags().StringVar(&doc, "doc", "", "a document to approve (plan, verdict, review draft): its absolute path, outside the repo's worktrees; the user reads it on acw board, and the worker is freed")
	return cmd
}

// interruptedTask is a task acw stop found: on a busy worker (Label, and
// what its status file said), or still queued.
type interruptedTask struct {
	queuedTask
	Label   string `json:"label,omitempty"`
	State   string `json:"state,omitempty"`
	Summary string `json:"summary,omitempty"`
	PRURL   string `json:"pr_url,omitempty"`
	DocPath string `json:"doc_path,omitempty"`
	Queued  bool   `json:"queued,omitempty"`
}

// saveInterrupted keeps, for the next master, the tasks of repo's busy
// workers and its queue, and ends the pool in the same lock: a task
// queued after would be lost unseen, it is refused instead (no swarm).
// No pool (a stop run again) saves nothing and keeps what was saved. A
// board that can't be written gets the tasks printed instead.
func saveInterrupted(repo string, out io.Writer) error {
	var tasks []interruptedTask
	err := withPool(repo, func(p *poolState, q *taskQueue) (bool, error) {
		for _, w := range p.Workers {
			if w.State != workerBusy || w.Current == nil {
				continue
			}
			s, _ := readWorkerStatus(filepath.Join(names.StatusDir(repo), w.label()+".json"))
			tasks = append(tasks, interruptedTask{queuedTask: *w.Current, Label: w.label(), State: s.State, Summary: s.Summary, PRURL: s.PRURL, DocPath: s.DocPath})
		}
		for _, t := range q.Tasks {
			tasks = append(tasks, interruptedTask{queuedTask: t, Queued: true})
		}
		if err := os.Remove(names.PoolFile(repo)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return false, nil
	})
	if err != nil {
		if _, statErr := os.Stat(names.PoolFile(repo)); errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(tasks)
	if err != nil {
		return err
	}
	if !record("interrupted", func(b *board.DB) error { return b.SaveInterrupted(repo, time.Now(), data, len(tasks) == 0) }) {
		fmt.Fprintf(out, "warning: the board could not keep the interrupted tasks, here they are:\n%s\n", renderInterrupted(tasks))
	}
	return nil
}

func renderInterrupted(tasks []interruptedTask) string {
	var b strings.Builder
	for _, t := range tasks {
		head := "queued"
		if !t.Queued {
			head = "on " + t.Label
			if t.State != "" {
				head += " (" + t.State + ")"
			}
		}
		if t.Branch != "" {
			head += ", branch " + t.Branch
		}
		if t.Kind != "" {
			head += ", --kind " + t.Kind
		}
		for _, id := range t.After {
			head += fmt.Sprintf(", after #%d", id)
		}
		if t.PRURL != "" {
			head += ", PR " + t.PRURL
		}
		if t.DocPath != "" {
			head += ", document " + t.DocPath
		}
		fmt.Fprintf(&b, "- #%d %s\n", t.ID, head)
		if t.Summary != "" {
			fmt.Fprintf(&b, "  where it stood: %s\n", t.Summary)
		}
		fmt.Fprintf(&b, "  brief:\n%s\n", indent(t.Brief, "    "))
	}
	return b.String()
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n"+prefix)
}

// startPrompt is what a new master is told of the previous swarm: the
// decisions still parked and the tasks the last acw stop interrupted.
// "" with neither. delivered drops the interrupted tasks, once the
// prompt reached the master.
func startPrompt(repo string) (text string, delivered func()) {
	var b strings.Builder
	found := false
	_ = withBoard(func(db *board.DB) error {
		ps, err := db.OpenParked(repo)
		if err != nil {
			return err
		}
		if len(ps) > 0 {
			b.WriteString("Decisions still parked:\n")
			for _, p := range ps {
				b.WriteString(renderParked(p, false))
			}
		}
		data, at, ok, err := db.Interrupted(repo)
		if err != nil || !ok {
			return err
		}
		var tasks []interruptedTask
		if err := json.Unmarshal(data, &tasks); err != nil || len(tasks) == 0 {
			return err
		}
		found = true
		fmt.Fprintf(&b, "\nTasks interrupted by the last acw stop (%s); queue again what should go on, with acw queue add:\n%s", at.Local().Format("02/01 15:04"), renderInterrupted(tasks))
		return nil
	})
	delivered = func() {
		if found {
			record("interrupted", func(db *board.DB) error { return db.DeleteInterrupted(repo) })
		}
	}
	if b.Len() == 0 {
		return "", delivered
	}
	return strings.TrimLeft(b.String(), "\n") + "\nCheck it against what acw shows now, tell me in a few lines what is pending, then wait for my instructions.", delivered
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
	if p.Refused {
		head += " · refused by the user, to discuss"
	}
	if p.DocPath != "" {
		head += fmt.Sprintf("\n    %s, document: %s", p.Kind, p.DocPath)
		if p.Branch != "" {
			head += ", branch " + p.Branch
		}
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
