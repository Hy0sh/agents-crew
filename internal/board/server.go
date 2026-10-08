package board

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed page.html
var page []byte

// doneHeader must come with a POST: a header of its own makes the browser
// ask first (CORS preflight), which nothing here answers, so a page from
// another site open in the same browser can't write to the master.
const doneHeader = "X-Acw-Board"

// doneRequest is a line of "Waiting on you" the user marked done.
type doneRequest struct {
	Repo   string    `json:"repo"`
	ID     string    `json:"id"`
	Ticket string    `json:"ticket"`
	Text   string    `json:"text"`
	Since  time.Time `json:"since"`
}

// Handler serves the page, its read routes, and POST /api/done. Only a
// request naming 127.0.0.1 or localhost gets through: a site whose name
// resolves to 127.0.0.1 (DNS rebinding) would otherwise read the
// decisions. notify writes to repo's master.
func Handler(b *DB, now func() time.Time, notify func(repo, msg string) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	mux.HandleFunc("GET /api/repos", func(w http.ResponseWriter, r *http.Request) {
		repos, err := b.Repos()
		reply(w, repos, err)
	})
	mux.HandleFunc("GET /api/board", func(w http.ResponseWriter, r *http.Request) {
		t := now()
		day := t
		if s := r.URL.Query().Get("day"); s != "" {
			parsed, err := time.ParseInLocation("2006-01-02", s, t.Location())
			if err != nil {
				http.Error(w, "day: YYYY-MM-DD", http.StatusBadRequest)
				return
			}
			day = parsed
		}
		repo := r.URL.Query().Get("repo")
		if repo == "" {
			repos, err := b.Repos()
			if err != nil {
				reply(w, nil, err)
				return
			}
			if len(repos) > 0 {
				repo = repos[0].Repo
			}
		}
		d, err := b.Board(repo, day, t)
		reply(w, BuildView(d, t), err)
	})
	mux.HandleFunc("POST /api/done", func(w http.ResponseWriter, r *http.Request) {
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); r.Header.Get(doneHeader) != "1" || mt != "application/json" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req doneRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.Repo == "" || req.ID == "" || req.Text == "" {
			http.Error(w, "repo, id and text are needed", http.StatusBadRequest)
			return
		}
		if err := markDone(b, req, now(), notify); err != nil {
			reply(w, nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// markDone tells the master, then records the mark: a mark the master
// never heard of would hide a wait for nothing. A parked decision on the
// user is closed with it.
func markDone(b *DB, req doneRequest, at time.Time, notify func(repo, msg string) error) error {
	line := strings.Join(strings.Fields(req.Text), " ")
	if req.Ticket != "" {
		line = req.Ticket + " " + line
	}
	msg := fmt.Sprintf("From acw board, the user marked done: %s (waiting since %s). This is information, not an approval: check the real state, close what is closed, and ask them again otherwise.",
		line, req.Since.Local().Format("15:04"))
	if err := notify(req.Repo, msg); err != nil {
		return fmt.Errorf("the master was not told: %w", err)
	}
	if id, ok := strings.CutPrefix(req.ID, "parked:"); ok {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return err
		}
		if _, err := b.CloseParked(n, "done (marked on acw board)", at); err != nil {
			return err
		}
	}
	return b.Mark(req.Repo, req.ID, at)
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
