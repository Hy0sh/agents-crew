// Package board keeps what the swarms did, for acw board's page: each
// worker's current state, the pull requests followed, the tasks handled
// and the decisions taken, per repo and per day. One SQLite base for the
// machine, written by acw (watcher, acw done) and by the master (acw board
// decision), read by the page. Never purged.
package board

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Path is $XDG_STATE_HOME/acw/board.db, else ~/.local/state/acw/board.db.
func Path() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "acw", "board.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "acw", "board.db")
}

// Times are unix seconds: a day is a range of them, cut in local time.
const schema = `
CREATE TABLE IF NOT EXISTS meta (version INTEGER NOT NULL);
INSERT INTO meta (version) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM meta);
CREATE TABLE IF NOT EXISTS workers (repo TEXT, worker TEXT, state TEXT, subject TEXT, branch TEXT, pr_url TEXT, summary TEXT, since INTEGER, updated_at INTEGER, PRIMARY KEY (repo, worker));
CREATE TABLE IF NOT EXISTS prs (repo TEXT, number INTEGER, url TEXT, title TEXT, worker TEXT, base TEXT, status TEXT, ci TEXT, review TEXT, updated_at INTEGER, PRIMARY KEY (repo, number));
CREATE TABLE IF NOT EXISTS handled (repo TEXT, at INTEGER, worker TEXT, task INTEGER, subject TEXT, summary TEXT, pr_url TEXT, outcome TEXT);
CREATE TABLE IF NOT EXISTS decisions (repo TEXT, at INTEGER, worker TEXT, subject TEXT, text TEXT, why TEXT);
CREATE INDEX IF NOT EXISTS handled_day ON handled (repo, at);
CREATE INDEX IF NOT EXISTS decisions_day ON decisions (repo, at);`

type DB struct{ sql *sql.DB }

// Open opens the base at path, creating it and its schema if needed. WAL
// and a busy timeout let several watchers, the master and the page use
// it at once.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db}, nil
}

func (b *DB) Close() error { return b.sql.Close() }

type Worker struct {
	Repo      string    `json:"-"`
	Worker    string    `json:"worker"`
	State     string    `json:"state"`
	Subject   string    `json:"subject"`
	Branch    string    `json:"branch"`
	PRURL     string    `json:"pr_url"`
	Summary   string    `json:"summary"`
	Since     time.Time `json:"since"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PR struct {
	Repo      string    `json:"-"`
	Number    int       `json:"number"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Worker    string    `json:"worker"`
	Base      string    `json:"base"`
	Status    string    `json:"status"`
	CI        string    `json:"ci"`
	Review    string    `json:"review"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Handled struct {
	Repo    string    `json:"-"`
	At      time.Time `json:"at"`
	Worker  string    `json:"worker"`
	Task    int       `json:"task"`
	Subject string    `json:"subject"`
	Summary string    `json:"summary"`
	PRURL   string    `json:"pr_url"`
	Outcome string    `json:"outcome"`
}

type Decision struct {
	Repo    string    `json:"-"`
	At      time.Time `json:"at"`
	Worker  string    `json:"worker"`
	Subject string    `json:"subject"`
	Text    string    `json:"text"`
	Why     string    `json:"why"`
}

// Day is what the page shows for one repo and one day. Live is set for
// today: workers and PRs are the current state, so only today has them.
type Day struct {
	Live      bool       `json:"live"`
	Workers   []Worker   `json:"workers"`
	PRs       []PR       `json:"prs"`
	Handled   []Handled  `json:"handled"`
	Decisions []Decision `json:"decisions"`
}

type RepoDays struct {
	Repo string   `json:"repo"`
	Days []string `json:"days"`
}

func (b *DB) UpsertWorker(w Worker) error {
	_, err := b.sql.Exec(`INSERT OR REPLACE INTO workers VALUES (?,?,?,?,?,?,?,?,?)`,
		w.Repo, w.Worker, w.State, w.Subject, w.Branch, w.PRURL, w.Summary, w.Since.Unix(), w.UpdatedAt.Unix())
	return err
}

func (b *DB) DeleteWorker(repo, worker string) error {
	_, err := b.sql.Exec(`DELETE FROM workers WHERE repo = ? AND worker = ?`, repo, worker)
	return err
}

func (b *DB) UpsertPR(p PR) error {
	_, err := b.sql.Exec(`INSERT OR REPLACE INTO prs VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.Repo, p.Number, p.URL, p.Title, p.Worker, p.Base, p.Status, p.CI, p.Review, p.UpdatedAt.Unix())
	return err
}

func (b *DB) AddHandled(h Handled) error {
	_, err := b.sql.Exec(`INSERT INTO handled VALUES (?,?,?,?,?,?,?,?)`,
		h.Repo, h.At.Unix(), h.Worker, h.Task, h.Subject, h.Summary, h.PRURL, h.Outcome)
	return err
}

func (b *DB) AddDecision(d Decision) error {
	_, err := b.sql.Exec(`INSERT INTO decisions VALUES (?,?,?,?,?,?)`,
		d.Repo, d.At.Unix(), d.Worker, d.Subject, d.Text, d.Why)
	return err
}

// bounds is the local day holding t, as unix seconds [start, end).
func bounds(t time.Time) (int64, int64) {
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return start.Unix(), start.AddDate(0, 0, 1).Unix()
}

// Board reads repo's registers for day; now tells whether day is today.
// Every list is non-nil, so the JSON has [] rather than null.
func (b *DB) Board(repo string, day, now time.Time) (Day, error) {
	start, end := bounds(day)
	today, _ := bounds(now)
	d := Day{Live: start == today, Workers: []Worker{}, PRs: []PR{}, Handled: []Handled{}, Decisions: []Decision{}}
	if d.Live {
		rows, err := b.sql.Query(`SELECT worker, state, subject, branch, pr_url, summary, since, updated_at FROM workers WHERE repo = ? ORDER BY worker`, repo)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			w := Worker{Repo: repo}
			var since, updated int64
			if err := rows.Scan(&w.Worker, &w.State, &w.Subject, &w.Branch, &w.PRURL, &w.Summary, &since, &updated); err != nil {
				rows.Close()
				return d, err
			}
			w.Since, w.UpdatedAt = time.Unix(since, 0), time.Unix(updated, 0)
			d.Workers = append(d.Workers, w)
		}
		rows.Close()
		rows, err = b.sql.Query(`SELECT number, url, title, worker, base, status, ci, review, updated_at FROM prs WHERE repo = ? AND updated_at >= ? AND updated_at < ? ORDER BY number DESC`, repo, start, end)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			p := PR{Repo: repo}
			var updated int64
			if err := rows.Scan(&p.Number, &p.URL, &p.Title, &p.Worker, &p.Base, &p.Status, &p.CI, &p.Review, &updated); err != nil {
				rows.Close()
				return d, err
			}
			p.UpdatedAt = time.Unix(updated, 0)
			d.PRs = append(d.PRs, p)
		}
		rows.Close()
	}
	rows, err := b.sql.Query(`SELECT at, worker, task, subject, summary, pr_url, outcome FROM handled WHERE repo = ? AND at >= ? AND at < ? ORDER BY at DESC, rowid DESC`, repo, start, end)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		h := Handled{Repo: repo}
		var at int64
		if err := rows.Scan(&at, &h.Worker, &h.Task, &h.Subject, &h.Summary, &h.PRURL, &h.Outcome); err != nil {
			rows.Close()
			return d, err
		}
		h.At = time.Unix(at, 0)
		d.Handled = append(d.Handled, h)
	}
	rows.Close()
	rows, err = b.sql.Query(`SELECT at, worker, subject, text, why FROM decisions WHERE repo = ? AND at >= ? AND at < ? ORDER BY at DESC, rowid DESC`, repo, start, end)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		dc := Decision{Repo: repo}
		var at int64
		if err := rows.Scan(&at, &dc.Worker, &dc.Subject, &dc.Text, &dc.Why); err != nil {
			return d, err
		}
		dc.At = time.Unix(at, 0)
		d.Decisions = append(d.Decisions, dc)
	}
	return d, rows.Err()
}

// Repos lists every repo with a row, the most recently active first, each
// with its days, latest first.
func (b *DB) Repos() ([]RepoDays, error) {
	rows, err := b.sql.Query(`
SELECT repo, day FROM (
  SELECT repo, date(at, 'unixepoch', 'localtime') AS day FROM handled
  UNION SELECT repo, date(at, 'unixepoch', 'localtime') FROM decisions
  UNION SELECT repo, date(updated_at, 'unixepoch', 'localtime') FROM workers
  UNION SELECT repo, date(updated_at, 'unixepoch', 'localtime') FROM prs)
ORDER BY day DESC, repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RepoDays{}
	index := map[string]int{}
	for rows.Next() {
		var repo, day string
		if err := rows.Scan(&repo, &day); err != nil {
			return nil, err
		}
		i, ok := index[repo]
		if !ok {
			i = len(out)
			index[repo] = i
			out = append(out, RepoDays{Repo: repo})
		}
		out[i].Days = append(out[i].Days, day)
	}
	return out, rows.Err()
}
