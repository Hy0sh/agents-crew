// Package decision is the master's queue of questions for the user: each
// one is written to a file the user answers from the UI (or the master
// closes when the answer came through its terminal), in any order,
// instead of blocking the conversation one question at a time.
//
// The master (through `acw __decision`) and the UI server both write the
// same file, so every change goes through update: an exclusive flock, a
// fresh read, then an atomic rename.
package decision

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	Open      = "open"
	Answered  = "answered"
	Abandoned = "abandoned" // still open when its swarm was stopped
)

// ErrClosed is returned when answering a decision that is no longer open,
// along with the decision as it stands, so the caller can show the answer
// that won instead of overwriting it.
var ErrClosed = errors.New("décision déjà close")

type Option struct {
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
}

type Decision struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Task      string    `json:"task"`
	// Blocks is the worker waiting on it, as the master named it, "" for
	// none.
	Blocks   string   `json:"blocks,omitempty"`
	Question string   `json:"question"`
	Options  []Option `json:"options"`
	Reco     int      `json:"reco"` // index in Options
	Status   string   `json:"status"`

	Answer      string    `json:"answer,omitempty"`
	AnsweredAt  time.Time `json:"answered_at,omitzero"`
	AnsweredVia string    `json:"answered_via,omitempty"` // "ui" or "terminal"
}

// List returns every decision in path, none when the file doesn't exist.
func List(path string) ([]Decision, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ds []Decision
	if err := json.Unmarshal(content, &ds); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ds, nil
}

// Get returns the decision id from path.
func Get(path, id string) (Decision, error) {
	ds, err := List(path)
	if err != nil {
		return Decision{}, err
	}
	for _, d := range ds {
		if d.ID == id {
			return d, nil
		}
	}
	return Decision{}, fmt.Errorf("décision %s introuvable", id)
}

// Add validates d, gives it the next ID and stores it as open. A question
// without at least two options is refused: an open-ended question is
// exactly what the master's brief forbids.
func Add(path string, d Decision, now time.Time) (Decision, error) {
	if strings.TrimSpace(d.Question) == "" {
		return Decision{}, errors.New("question vide")
	}
	if len(d.Options) < 2 {
		return Decision{}, errors.New("au moins deux options, avec leurs conséquences")
	}
	if d.Reco < 0 || d.Reco >= len(d.Options) {
		return Decision{}, fmt.Errorf("recommandation %d hors des options (1 à %d)", d.Reco+1, len(d.Options))
	}
	err := update(path, func(ds []Decision) ([]Decision, error) {
		d.ID = nextID(ds)
		d.CreatedAt = now
		d.Status = Open
		return append(ds, d), nil
	})
	return d, err
}

// nextID is one past the highest ID ever given: IDs are never reused, so
// "D3" in an old journal always means the same decision.
func nextID(ds []Decision) string {
	highest := 0
	for _, d := range ds {
		if n, err := strconv.Atoi(strings.TrimPrefix(d.ID, "D")); err == nil && n > highest {
			highest = n
		}
	}
	return "D" + strconv.Itoa(highest+1)
}

// Close records answer for decision id. A decision no longer open is left
// as is and returned with ErrClosed.
func Close(path, id, answer, via string, now time.Time) (Decision, error) {
	var out Decision
	err := update(path, func(ds []Decision) ([]Decision, error) {
		for i := range ds {
			if ds[i].ID != id {
				continue
			}
			if ds[i].Status != Open {
				out = ds[i]
				return nil, ErrClosed
			}
			ds[i].Status, ds[i].Answer, ds[i].AnsweredAt, ds[i].AnsweredVia = Answered, answer, now, via
			out = ds[i]
			return ds, nil
		}
		return nil, fmt.Errorf("décision %s introuvable", id)
	})
	return out, err
}

// AbandonOpen marks every open decision abandoned, for `acw stop` and a
// start that finds no master: their
// swarm is gone, nobody would relay an answer. It returns how many.
func AbandonOpen(path string, now time.Time) (int, error) {
	n := 0
	err := update(path, func(ds []Decision) ([]Decision, error) {
		for i := range ds {
			if ds[i].Status == Open {
				ds[i].Status, ds[i].AnsweredAt = Abandoned, now
				n++
			}
		}
		return ds, nil
	})
	return n, err
}

// update applies fn to the decisions in path under an exclusive lock and
// writes the result atomically. fn returning an error writes nothing.
func update(path string, fn func([]Decision) ([]Decision, error)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	ds, err := List(path)
	if err != nil {
		return err
	}
	ds, err = fn(ds)
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
