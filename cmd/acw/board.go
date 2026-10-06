package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hy0sh/agents-crew/internal/board"
)

// record writes to the board, best effort: a base that can't be written
// is one line on stderr, never a reason for the swarm to stop.
func record(what string, write func(*board.DB) error) {
	b, err := board.Open(board.Path())
	if err == nil {
		err = write(b)
		b.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "board (%s): %v\n", what, err)
	}
}

// addDecision records a decision for repo, its text from args or, without
// any, from in. Unlike record, it returns the error: the master must know
// its decision was not kept.
func addDecision(repo, worker, subject, why string, args []string, in io.Reader, now time.Time) error {
	text := strings.Join(args, " ")
	if len(args) == 0 {
		content, err := io.ReadAll(in)
		if err != nil {
			return err
		}
		text = string(content)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("no decision: give it as arguments or on stdin")
	}
	b, err := board.Open(board.Path())
	if err != nil {
		return err
	}
	defer b.Close()
	return b.AddDecision(board.Decision{Repo: repo, At: now, Worker: worker, Subject: subject, Text: text, Why: strings.TrimSpace(why)})
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
		Short: "Open a local read-only page of what the swarms did: workers, pull requests, decisions, tasks handled per day",
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
			return http.Serve(ln, board.Handler(b, time.Now))
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
				index, err := workerArg(repo, worker)
				if err != nil {
					return err
				}
				worker = fmt.Sprintf("worker%d", index)
			}
			if err := addDecision(repo, worker, subject, why, args, cmd.InOrStdin(), time.Now()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "decision recorded")
			return nil
		},
	}
	decisionRepo = repoFlag(decision)
	decision.Flags().StringVar(&worker, "worker", "", "the worker it concerns (workerN)")
	decision.Flags().StringVar(&subject, "subject", "", "what it is about: a ticket, a PR, a topic")
	decision.Flags().StringVar(&why, "why", "", "the reason, in one line")
	cmd.AddCommand(decision)
	return cmd
}

// boardWorker is a worker's row: herdr's blocked first (it waits on
// someone), then, while it has a task, the state it gives itself, else
// the pool's. A free worker has no subject.
func boardWorker(repo string, pw poolWorker, agentStatus string, s workerStatus, now time.Time) board.Worker {
	w := board.Worker{Repo: repo, Worker: pw.label(), State: pw.State, Since: pw.Since, UpdatedAt: now}
	if pw.State == workerBusy {
		w.Subject, w.Branch, w.PRURL, w.Summary = s.Tache, s.Branch, s.PRURL, s.Summary
		if s.State != "" {
			w.State = s.State
		}
	}
	if agentStatus == "blocked" {
		w.State = "blocked"
	}
	return w
}

// changedWorkers returns the rows of cur that differ from last, UpdatedAt
// aside, and keeps last up to date: the base is written on a move only.
func changedWorkers(last map[string]board.Worker, cur []board.Worker) []board.Worker {
	var out []board.Worker
	for _, w := range cur {
		prev, seen := last[w.Worker]
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
