// Package journal keeps what the workers worked on, one JSONL file per
// day, outside the repo so it outlives `acw stop`. It is fed by the
// workers' Stop hook, which snapshots the status file each worker keeps:
// a hook fires whatever the agents remember to do.
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Entry struct {
	At      time.Time `json:"at"`
	Worker  string    `json:"worker"`
	Tache   string    `json:"tache"`
	State   string    `json:"state"`
	Summary string    `json:"summary,omitempty"`
	PRURL   string    `json:"pr_url,omitempty"`
}

// Day is the journal file name for t, local time: "the day" is the
// user's.
func Day(t time.Time) string {
	return t.Local().Format("2006-01-02")
}

// Record appends worker's status to the day's journal, only when its task
// or state changed since that worker's last entry of the day: a turn that
// changed nothing (most of them) writes nothing.
func Record(dir, worker string, status []byte, now time.Time) error {
	var raw map[string]any
	if err := json.Unmarshal(status, &raw); err != nil {
		return err
	}
	e := Entry{At: now, Worker: worker, Tache: field(raw, "tache"), State: field(raw, "state"),
		Summary: field(raw, "summary"), PRURL: field(raw, "pr_url")}
	if e.Tache == "" && e.State == "" {
		return nil
	}

	day := Day(now)
	entries, err := Read(dir, day)
	if err != nil {
		return err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Worker == worker {
			if entries[i].Tache == e.Tache && entries[i].State == e.State {
				return nil
			}
			break
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// field reads a status field whatever its JSON type: workers write the
// file by hand, a number or a list where a string was expected must not
// lose the entry.
func field(raw map[string]any, key string) string {
	switch v := raw[key].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// Read returns the entries of day, none when there are no file. A line
// that doesn't parse is skipped rather than hiding the rest of the day.
func Read(dir, day string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(dir, day+".jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var e Entry
		if json.Unmarshal(scanner.Bytes(), &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries, scanner.Err()
}

// Days lists the days that have a journal, oldest first.
func Days(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	days := make([]string, len(files))
	for i, f := range files {
		days[i] = strings.TrimSuffix(filepath.Base(f), ".jsonl")
	}
	sort.Strings(days)
	return days, nil
}
