package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// acw status is the swarm at a glance, for the user as for the master:
// one line per worker from what acw can read without asking anyone
// (herdr's state, the status file and its stamps, the worktree, the
// status line's usage), then the inbox. The age of a status next to a
// recent activity is what tells a worker coding without updating its
// status from one that stopped.

type statusRow struct {
	Label, Agent, State, Task  string
	Branch, BaseBranch, PR     string
	Updated, TurnEnd, Activity time.Time
	Context, FiveHour          *float64
}

// maxWorkers bounds the scan for workers in collectStatus.
const maxWorkers = 64

func renderStatus(now time.Time, rows []statusRow, unread int, lastAt time.Time) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s  [%s]  %s · statut %s · tour %s · activité %s · ctx %s%% · 5h %s%%",
			r.Label, orDash(r.Agent), orDash(r.State), age(now, r.Updated), age(now, r.TurnEnd), age(now, r.Activity),
			percent(r.Context), percent(r.FiveHour))
		if r.Branch != "" {
			branch := r.Branch
			if r.BaseBranch != "" {
				branch += " ← " + r.BaseBranch
			}
			fmt.Fprintf(&b, " · %s", branch)
		}
		if r.PR != "" {
			fmt.Fprintf(&b, " · %s", r.PR)
		}
		if r.Task != "" {
			fmt.Fprintf(&b, "\n    %s", r.Task)
		}
		b.WriteString("\n")
	}
	if unread == 0 {
		b.WriteString("inbox : vide\n")
	} else {
		fmt.Fprintf(&b, "inbox : %d messages non lus, le dernier arrivé à %s\n", unread, lastAt.Local().Format("15:04"))
	}
	return b.String()
}

func age(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return fmt.Sprintf("il y a %d min", int(now.Sub(t).Minutes()))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// collectStatus reads every worker of the swarm in repo (see scanWorkers)
// and its master's inbox.
func collectStatus(repo string) (rows []statusRow, unread int, lastAt time.Time, err error) {
	statusDir := names.StatusDir(repo)
	if _, err := os.Stat(statusDir); err != nil {
		return nil, 0, time.Time{}, fmt.Errorf("aucun swarm acw dans %s", repo)
	}
	agents, err := herdr.AgentList()
	if err != nil {
		return nil, 0, time.Time{}, fmt.Errorf("herdr agent list: %w", err)
	}
	rows = scanWorkers(statusDir, agents, names.Slug(repo))
	if content, err := os.ReadFile(names.Inbox(repo)); err == nil && len(content) > 0 {
		unread = bytes.Count(content, []byte("\n"))
		if info, err := os.Stat(names.Inbox(repo)); err == nil {
			lastAt = info.ModTime()
		}
	}
	return rows, unread, lastAt, nil
}

// scanWorkers reads every worker index that has an agent or a status
// file. It does not stop at a gap: a worker that failed to start leaves
// one, and those after it are still running.
func scanWorkers(statusDir string, agents []herdr.Agent, slug string) []statusRow {
	var rows []statusRow
	for i := 1; i <= maxWorkers; i++ {
		label := fmt.Sprintf("worker%d", i)
		statusPath := filepath.Join(statusDir, label+".json")
		agent, running := herdr.FindAgent(agents, names.Worker(slug, i))
		_, statErr := os.Stat(statusPath)
		if !running && statErr != nil {
			continue
		}
		row := statusRow{Label: label, Agent: agent.Status, Activity: activity(statusPath, agent.Cwd)}
		if content, err := os.ReadFile(statusPath); err == nil {
			var s struct {
				Tache       string `json:"tache"`
				State       string `json:"state"`
				Branch      string `json:"branch"`
				BaseBranch  string `json:"base_branch"`
				PRURL       string `json:"pr_url"`
				UpdatedAt   string `json:"updated_at"`
				LastTurnEnd string `json:"last_turn_end"`
			}
			if json.Unmarshal(content, &s) == nil {
				row.Task, row.State, row.Branch, row.BaseBranch, row.PR = s.Tache, s.State, s.Branch, s.BaseBranch, s.PRURL
				row.Updated, _ = time.Parse(time.RFC3339, s.UpdatedAt)
				row.TurnEnd, _ = time.Parse(time.RFC3339, s.LastTurnEnd)
			}
		}
		if u, err := readUsage(filepath.Join(statusDir, label+".usage.json")); err == nil {
			row.Context, row.FiveHour = u.Context, u.FiveHour
		}
		rows = append(rows, row)
	}
	return rows
}
