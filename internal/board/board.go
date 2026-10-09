// Package board keeps what the swarms did, for acw board's page: each
// worker's current state, the pull requests followed, the tasks handled
// and the decisions taken, per repo and per day. One SQLite base for the
// machine, written by acw (watcher, acw done) and by the master (acw board
// decision), read by the page. Never purged.
package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
CREATE TABLE IF NOT EXISTS watchers (repo TEXT PRIMARY KEY, seen INTEGER);
CREATE TABLE IF NOT EXISTS parked (id INTEGER PRIMARY KEY, repo TEXT, ticket TEXT, worker TEXT, on_whom TEXT, text TEXT, created_at INTEGER, closed_at INTEGER, answer TEXT);
CREATE TABLE IF NOT EXISTS marks (repo TEXT, item TEXT, at INTEGER);
CREATE TABLE IF NOT EXISTS handoffs (id INTEGER PRIMARY KEY, repo TEXT, text TEXT, created_at INTEGER, used_at INTEGER);
CREATE INDEX IF NOT EXISTS handled_day ON handled (repo, at);
CREATE INDEX IF NOT EXISTS decisions_day ON decisions (repo, at);`

// migrations[i] takes a base from version i+1 to i+2. A new base starts at
// version 1 and goes through them all, like an old one. Writes name their
// columns: a column added here lands at the end of its table.
var migrations = []string{
	`ALTER TABLE workers ADD COLUMN blocked_on TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE workers ADD COLUMN busy TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE workers ADD COLUMN ctx REAL`,
	`ALTER TABLE workers ADD COLUMN five_hour REAL`,
	`ALTER TABLE prs ADD COLUMN head TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE prs ADD COLUMN since_ready INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE prs ADD COLUMN since_hole INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE watchers ADD COLUMN queue INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE watchers ADD COLUMN inbox INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE parked ADD COLUMN kind TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE parked ADD COLUMN doc_path TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS interrupted (repo TEXT PRIMARY KEY, at INTEGER, tasks TEXT)`,
}

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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db}, nil
}

// migrate runs the migrations a base lacks. The watchers and the page may
// open the base at the same moment: BEGIN IMMEDIATE takes the write lock
// before reading the version, so the second one waits, then finds it done.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	err = func() error {
		var version int
		if err := conn.QueryRowContext(ctx, `SELECT version FROM meta`).Scan(&version); err != nil {
			return err
		}
		if version > len(migrations) {
			return nil
		}
		for _, m := range migrations[version-1:] {
			if _, err := conn.ExecContext(ctx, m); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, `UPDATE meta SET version = ?`, len(migrations)+1)
		return err
	}()
	if err != nil {
		conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func (b *DB) Close() error { return b.sql.Close() }

type Worker struct {
	Repo      string `json:"-"`
	Worker    string `json:"worker"`
	State     string `json:"state"`
	Subject   string `json:"subject"`
	Branch    string `json:"branch"`
	PRURL     string `json:"pr_url"`
	Summary   string `json:"summary"`
	BlockedOn string `json:"blocked_on"`
	// Busy is what its screen shows it waiting on: a tool running, or
	// background shells and monitors.
	Busy      string    `json:"busy"`
	Context   *float64  `json:"ctx"`
	FiveHour  *float64  `json:"five_hour"`
	Since     time.Time `json:"since"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PR struct {
	Repo   string `json:"-"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Worker string `json:"worker"`
	Head   string `json:"head"`
	Base   string `json:"base"`
	Status string `json:"status"`
	CI     string `json:"ci"`
	Review string `json:"review"`
	// SinceReady and SinceHole are when the watcher first saw the PR
	// ready for its merge, or with review asks and nobody on it; zero
	// while it isn't. A later write keeps the first date.
	SinceReady time.Time `json:"since_ready"`
	SinceHole  time.Time `json:"since_hole"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Parked is a decision the master put off until someone answers: the
// client, a third party, or the user (On "me"). Its ID is the number the
// user quotes back, the same from one swarm to the next.
type Parked struct {
	ID        int64      `json:"id"`
	Repo      string     `json:"-"`
	Ticket    string     `json:"ticket"`
	Worker    string     `json:"worker"`
	On        string     `json:"on"`
	Text      string     `json:"text"`
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	Answer    string     `json:"answer"`
	// Kind and DocPath are set on a document to approve (a plan, a
	// verdict, a review draft): what it is, and the file it is in.
	Kind    string `json:"kind"`
	DocPath string `json:"doc_path"`
	// Refused is a document the user refused on the page: still open,
	// until the master closes it once they discussed it.
	Refused bool `json:"refused"`
}

// refused is the answer of a parked document refused but still open.
const refused = "refused"

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
// today: workers and PRs are the current state, so only today has them,
// and Seen, the last time a watcher of the repo wrote it was alive.
type Day struct {
	Live bool       `json:"live"`
	Seen *time.Time `json:"seen"`
	// Queue and Inbox are the task queue's length and the master's unread
	// messages at Seen.
	Queue, Inbox int
	Workers      []Worker   `json:"workers"`
	PRs          []PR       `json:"prs"`
	Handled      []Handled  `json:"handled"`
	Decisions    []Decision `json:"decisions"`
	// Parked holds the open parked decisions, Marks the lines marked done
	// on the page within markWindow, by item: today only, like Workers.
	Parked []Parked
	Marks  map[string]time.Time
}

type RepoDays struct {
	Repo string   `json:"repo"`
	Days []string `json:"days"`
}

func (b *DB) UpsertWorker(w Worker) error {
	_, err := b.sql.Exec(`INSERT OR REPLACE INTO workers (repo, worker, state, subject, branch, pr_url, summary, since, updated_at, blocked_on, busy, ctx, five_hour)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		w.Repo, w.Worker, w.State, w.Subject, w.Branch, w.PRURL, w.Summary, w.Since.Unix(), w.UpdatedAt.Unix(), w.BlockedOn, w.Busy, w.Context, w.FiveHour)
	return err
}

// Beat records that a watcher of repo is alive at at, with its queue's
// length and the master's unread messages: a page that only reloads can't
// tell a quiet swarm from a dead watcher.
func (b *DB) Beat(repo string, at time.Time, queue, inbox int) error {
	_, err := b.sql.Exec(`INSERT OR REPLACE INTO watchers (repo, seen, queue, inbox) VALUES (?,?,?,?)`, repo, at.Unix(), queue, inbox)
	return err
}

// unix is t as unix seconds, 0 for the zero time.
func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// fromUnix is the reverse of unix.
func fromUnix(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s, 0)
}

func (b *DB) DeleteWorker(repo, worker string) error {
	_, err := b.sql.Exec(`DELETE FROM workers WHERE repo = ? AND worker = ?`, repo, worker)
	return err
}

// KeepWorkers deletes repo's worker rows but those of keep: what is left
// of a swarm once it stopped or shrank.
func (b *DB) KeepWorkers(repo string, keep []string) error {
	q, args := `DELETE FROM workers WHERE repo = ?`, []any{repo}
	for _, w := range keep {
		q += ` AND worker != ?`
		args = append(args, w)
	}
	_, err := b.sql.Exec(q, args...)
	return err
}

func (b *DB) UpsertPR(p PR) error {
	// The worker and the head are kept when the new row has none: once
	// its worker moves on, nothing names the owner any more, and a row
	// from a status file knows no head. A since stays the first one seen.
	_, err := b.sql.Exec(`INSERT INTO prs (repo, number, url, title, worker, base, status, ci, review, updated_at, head, since_ready, since_hole)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (repo, number) DO UPDATE SET url=excluded.url, title=excluded.title,
		worker=CASE WHEN excluded.worker <> '' THEN excluded.worker ELSE prs.worker END,
		head=CASE WHEN excluded.head <> '' THEN excluded.head ELSE prs.head END,
		base=excluded.base, status=excluded.status, ci=excluded.ci, review=excluded.review, updated_at=excluded.updated_at,
		since_ready=CASE WHEN excluded.since_ready = 0 THEN 0 WHEN prs.since_ready <> 0 THEN prs.since_ready ELSE excluded.since_ready END,
		since_hole=CASE WHEN excluded.since_hole = 0 THEN 0 WHEN prs.since_hole <> 0 THEN prs.since_hole ELSE excluded.since_hole END`,
		p.Repo, p.Number, p.URL, p.Title, p.Worker, p.Base, p.Status, p.CI, p.Review, p.UpdatedAt.Unix(), p.Head, unix(p.SinceReady), unix(p.SinceHole))
	return err
}

// Park records a decision put off, and returns its number.
func (b *DB) Park(p Parked) (int64, error) {
	res, err := b.sql.Exec(`INSERT INTO parked (repo, ticket, worker, on_whom, text, created_at, closed_at, answer, kind, doc_path) VALUES (?,?,?,?,?,?,NULL,'',?,?)`,
		p.Repo, p.Ticket, p.Worker, p.On, p.Text, p.CreatedAt.Unix(), p.Kind, p.DocPath)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const parkedColumns = `id, repo, ticket, worker, on_whom, text, created_at, closed_at, answer, kind, doc_path`

func scanParked(scan func(...any) error) (Parked, error) {
	var p Parked
	var created int64
	var closed sql.NullInt64
	if err := scan(&p.ID, &p.Repo, &p.Ticket, &p.Worker, &p.On, &p.Text, &created, &closed, &p.Answer, &p.Kind, &p.DocPath); err != nil {
		return p, err
	}
	p.CreatedAt = time.Unix(created, 0)
	if closed.Valid {
		t := time.Unix(closed.Int64, 0)
		p.ClosedAt = &t
	}
	p.Refused = p.ClosedAt == nil && p.Answer == refused
	return p, nil
}

// GetParked reads parked decision id, open or closed.
func (b *DB) GetParked(id int64) (Parked, error) {
	p, err := scanParked(b.sql.QueryRow(`SELECT `+parkedColumns+` FROM parked WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("no parked decision #%d", id)
	}
	return p, err
}

// OpenParked lists repo's parked decisions still waiting, oldest first.
func (b *DB) OpenParked(repo string) ([]Parked, error) {
	rows, err := b.sql.Query(`SELECT `+parkedColumns+` FROM parked WHERE repo = ? AND closed_at IS NULL ORDER BY id`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Parked{}
	for rows.Next() {
		p, err := scanParked(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CloseParked closes parked decision id with answer. One already closed
// is refused, saying when and with what: a second answer would be lost.
func (b *DB) CloseParked(id int64, answer string, at time.Time) (Parked, error) {
	res, err := b.sql.Exec(`UPDATE parked SET closed_at = ?, answer = ? WHERE id = ? AND closed_at IS NULL`, at.Unix(), answer, id)
	if err != nil {
		return Parked{}, err
	}
	p, err := b.GetParked(id)
	if err != nil {
		return p, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return p, fmt.Errorf("#%d was already closed on %s, with: %s", id, p.ClosedAt.Local().Format("02/01 15:04"), p.Answer)
	}
	return p, nil
}

// AddHandoff keeps the handoff a master leaves for the next one on repo.
func (b *DB) AddHandoff(repo, text string, at time.Time) error {
	_, err := b.sql.Exec(`INSERT INTO handoffs (repo, text, created_at, used_at) VALUES (?,?,?,NULL)`, repo, text, at.Unix())
	return err
}

// HandoffSince says a handoff was left on repo at since or after.
func (b *DB) HandoffSince(repo string, since time.Time) (bool, error) {
	var n int
	err := b.sql.QueryRow(`SELECT count(*) FROM handoffs WHERE repo = ? AND created_at >= ?`, repo, since.Unix()).Scan(&n)
	return n > 0, err
}

// TakeHandoff returns repo's latest handoff not handed to a master yet,
// and marks every waiting one used: an older one is superseded. ok is
// false without any.
func (b *DB) TakeHandoff(repo string, at time.Time) (text string, created time.Time, ok bool, err error) {
	var unix int64
	err = b.sql.QueryRow(`SELECT text, created_at FROM handoffs WHERE repo = ? AND used_at IS NULL ORDER BY id DESC LIMIT 1`, repo).Scan(&text, &unix)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	_, err = b.sql.Exec(`UPDATE handoffs SET used_at = ? WHERE repo = ? AND used_at IS NULL`, at.Unix(), repo)
	return text, time.Unix(unix, 0), err == nil, err
}

// RefuseParked marks open parked document id refused: it stays open, on
// the user's list, until the master closes it. One closed is refused.
func (b *DB) RefuseParked(id int64) (Parked, error) {
	res, err := b.sql.Exec(`UPDATE parked SET answer = ? WHERE id = ? AND closed_at IS NULL`, refused, id)
	if err != nil {
		return Parked{}, err
	}
	p, err := b.GetParked(id)
	if err != nil {
		return p, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return p, fmt.Errorf("#%d was already closed on %s, with: %s", id, p.ClosedAt.Local().Format("02/01 15:04"), p.Answer)
	}
	return p, nil
}

// SaveInterrupted keeps the tasks acw stop found on repo's workers and
// queue, for the next master. empty keeps what an earlier stop saved: a
// stop run again after a failed one finds nothing left.
func (b *DB) SaveInterrupted(repo string, at time.Time, tasks []byte, empty bool) error {
	if empty {
		return nil
	}
	_, err := b.sql.Exec(`INSERT INTO interrupted (repo, at, tasks) VALUES (?,?,?)
		ON CONFLICT (repo) DO UPDATE SET at=excluded.at, tasks=excluded.tasks`, repo, at.Unix(), string(tasks))
	return err
}

// Interrupted reads what SaveInterrupted kept for repo; ok is false
// without any.
func (b *DB) Interrupted(repo string) (tasks []byte, at time.Time, ok bool, err error) {
	var text string
	var unix int64
	err = b.sql.QueryRow(`SELECT tasks, at FROM interrupted WHERE repo = ?`, repo).Scan(&text, &unix)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	return []byte(text), time.Unix(unix, 0), true, nil
}

// DeleteInterrupted drops repo's interrupted tasks, once a master got them.
func (b *DB) DeleteInterrupted(repo string) error {
	_, err := b.sql.Exec(`DELETE FROM interrupted WHERE repo = ?`, repo)
	return err
}

// SetDrafts makes drafts repo's draft PRs: each is written with status
// draft, and a draft row not among them is gone (closed, or marked ready:
// then the PR watch writes it again as open).
func (b *DB) SetDrafts(repo string, drafts []PR) error {
	numbers := make([]any, 0, len(drafts)+1)
	numbers = append(numbers, repo)
	for _, p := range drafts {
		p.Repo, p.Status = repo, "draft"
		if err := b.UpsertPR(p); err != nil {
			return err
		}
		numbers = append(numbers, p.Number)
	}
	query := `DELETE FROM prs WHERE repo = ? AND status = 'draft'`
	if len(drafts) > 0 {
		query += ` AND number NOT IN (?` + strings.Repeat(",?", len(drafts)-1) + `)`
	}
	_, err := b.sql.Exec(query, numbers...)
	return err
}

// SetParkedOn changes who parked decision id waits on. One closed is
// refused: its answer is in.
func (b *DB) SetParkedOn(id int64, on string) (Parked, error) {
	res, err := b.sql.Exec(`UPDATE parked SET on_whom = ? WHERE id = ? AND closed_at IS NULL`, on, id)
	if err != nil {
		return Parked{}, err
	}
	p, err := b.GetParked(id)
	if err != nil {
		return p, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return p, fmt.Errorf("#%d was already closed on %s, with: %s", id, p.ClosedAt.Local().Format("02/01 15:04"), p.Answer)
	}
	return p, nil
}

// markWindow is how far back marks are read: a PR's mark holds until its
// state changes, and a reviewer can take days to come back.
const markWindow = 14 * 24 * time.Hour

// Mark records that the user marked item done on the page.
func (b *DB) Mark(repo, item string, at time.Time) error {
	_, err := b.sql.Exec(`INSERT INTO marks (repo, item, at) VALUES (?,?,?)`, repo, item, at.Unix())
	return err
}

// Marked says the user already marked item done.
func (b *DB) Marked(repo, item string) (bool, error) {
	var n int
	err := b.sql.QueryRow(`SELECT count(*) FROM marks WHERE repo = ? AND item = ?`, repo, item).Scan(&n)
	return n > 0, err
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
	d := Day{Live: start == today, Workers: []Worker{}, PRs: []PR{}, Handled: []Handled{}, Decisions: []Decision{},
		Parked: []Parked{}, Marks: map[string]time.Time{}}
	if d.Live {
		var seen int64
		switch err := b.sql.QueryRow(`SELECT seen, queue, inbox FROM watchers WHERE repo = ?`, repo).Scan(&seen, &d.Queue, &d.Inbox); err {
		case nil:
			t := time.Unix(seen, 0)
			d.Seen = &t
		case sql.ErrNoRows:
		default:
			return d, err
		}
		rows, err := b.sql.Query(`SELECT worker, state, subject, branch, pr_url, summary, blocked_on, busy, ctx, five_hour, since, updated_at FROM workers WHERE repo = ? ORDER BY worker`, repo)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			w := Worker{Repo: repo}
			var since, updated int64
			if err := rows.Scan(&w.Worker, &w.State, &w.Subject, &w.Branch, &w.PRURL, &w.Summary, &w.BlockedOn, &w.Busy, &w.Context, &w.FiveHour, &since, &updated); err != nil {
				rows.Close()
				return d, err
			}
			w.Since, w.UpdatedAt = time.Unix(since, 0), time.Unix(updated, 0)
			d.Workers = append(d.Workers, w)
		}
		rows.Close()
		rows, err = b.sql.Query(`SELECT number, url, title, worker, head, base, status, ci, review, since_ready, since_hole, updated_at FROM prs WHERE repo = ? AND updated_at >= ? AND updated_at < ? ORDER BY number DESC`, repo, start, end)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			p := PR{Repo: repo}
			var ready, hole, updated int64
			if err := rows.Scan(&p.Number, &p.URL, &p.Title, &p.Worker, &p.Head, &p.Base, &p.Status, &p.CI, &p.Review, &ready, &hole, &updated); err != nil {
				rows.Close()
				return d, err
			}
			p.SinceReady, p.SinceHole, p.UpdatedAt = fromUnix(ready), fromUnix(hole), time.Unix(updated, 0)
			d.PRs = append(d.PRs, p)
		}
		rows.Close()
		if d.Parked, err = b.OpenParked(repo); err != nil {
			return d, err
		}
		rows, err = b.sql.Query(`SELECT item, max(at) FROM marks WHERE repo = ? AND at >= ? GROUP BY item`, repo, now.Add(-markWindow).Unix())
		if err != nil {
			return d, err
		}
		for rows.Next() {
			var item string
			var at int64
			if err := rows.Scan(&item, &at); err != nil {
				rows.Close()
				return d, err
			}
			d.Marks[item] = time.Unix(at, 0)
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
SELECT repo, date(t, 'unixepoch', 'localtime') AS day FROM (
  SELECT repo, at AS t FROM handled
  UNION ALL SELECT repo, at FROM decisions
  UNION ALL SELECT repo, updated_at FROM workers
  UNION ALL SELECT repo, updated_at FROM prs)
GROUP BY repo, date(t, 'unixepoch', 'localtime')
ORDER BY max(max(t)) OVER (PARTITION BY repo) DESC, repo, max(t) DESC`)
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
