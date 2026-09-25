package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const statusLineUse = "__statusline"

// A claude worker's status line is acw's: Claude Code runs it after each
// message with a JSON of the session on stdin, the only place where the
// worker's context and its 5-hour quota can be read without scraping a
// pane. It keeps the last JSON in workerN.usage.json, for acw status and
// acw clear, and prints a short line for the pane. It replaces the user's
// own status line in worker panes, by choice: nobody works in those.

// statusLineCommand is the command a claude worker's status line runs.
func statusLineCommand(exe, statusDir, label string) string {
	return shellWord(exe) + " " + statusLineUse + " " + shellWord(statusDir) + " " + shellWord(label)
}

// usage is what acw reads back from a status line's JSON. A nil
// percentage is one Claude Code did not give: context is null right after
// /clear, and rate_limits only comes with a subscription.
type usage struct {
	SessionID string
	Context   *float64
	FiveHour  *float64
}

func parseUsage(content []byte) (usage, error) {
	var raw struct {
		SessionID     string `json:"session_id"`
		ContextWindow struct {
			UsedPercentage *float64 `json:"used_percentage"`
		} `json:"context_window"`
		RateLimits struct {
			FiveHour struct {
				UsedPercentage *float64 `json:"used_percentage"`
			} `json:"five_hour"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		return usage{}, err
	}
	return usage{SessionID: raw.SessionID, Context: raw.ContextWindow.UsedPercentage, FiveHour: raw.RateLimits.FiveHour.UsedPercentage}, nil
}

func readUsage(path string) (usage, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return usage{}, err
	}
	return parseUsage(content)
}

// recordUsage keeps the JSON as received, written aside and renamed so a
// reader never sees half of it, then prints the pane's status line. A
// JSON it cannot read is still kept: the file is Claude Code's, not ours.
func recordUsage(path string, in io.Reader, out io.Writer) error {
	content, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	u, _ := parseUsage(content)
	_, err = fmt.Fprintf(out, "ctx %s%% · 5h %s%%\n", percent(u.Context), percent(u.FiveHour))
	return err
}

func percent(p *float64) string {
	if p == nil {
		return "?"
	}
	return fmt.Sprintf("%.0f", *p)
}
