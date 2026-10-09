package board

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
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
	// Answer is accept or refuse on a parked document, "" for a plain done.
	Answer string `json:"answer"`
}

// maxDoc caps a parked document read for the page.
const maxDoc = 1 << 20

// parkedDoc is a parked document as the page shows it: rendered, or why
// it can't be.
type parkedDoc struct {
	HTML  string `json:"html"`
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`
}

// markdown renders GitHub's flavour (tables, task lists), the one plans
// are written in; without html.WithUnsafe it drops raw HTML and unsafe
// links.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// renderDoc reads path now, the file the worker left, and renders it.
func renderDoc(path string) parkedDoc {
	d := parkedDoc{Path: path}
	info, err := os.Stat(path)
	switch {
	case err != nil:
		d.Error = "file not found: " + path
	case info.Size() > maxDoc:
		d.Error = fmt.Sprintf("file over %d KiB, open it in an editor: %s", maxDoc>>10, path)
	}
	if d.Error != "" {
		return d
	}
	content, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(content) {
		d.Error = "not a readable text file: " + path
		return d
	}
	var out bytes.Buffer
	if err := markdown.Convert(content, &out); err != nil {
		d.Error = err.Error()
		return d
	}
	d.HTML = out.String()
	return d
}

// Handler serves the page, its read routes, and POST /api/done. Only a
// request naming 127.0.0.1 or localhost gets through: a site whose name
// resolves to 127.0.0.1 (DNS rebinding) would otherwise read the
// decisions. notify writes to repo's master.
func Handler(b *DB, now func() time.Time, notify func(repo, msg string) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// A parked document's markdown images would load from anywhere.
		w.Header().Set("Content-Security-Policy", "img-src 'self' data:")
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
	// The path comes from the row, never from the request: only files
	// parked as documents are read.
	mux.HandleFunc("GET /api/parked/{id}/doc", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		p, err := b.GetParked(id)
		if err != nil || p.DocPath == "" {
			http.NotFound(w, r)
			return
		}
		reply(w, renderDoc(p.DocPath), nil)
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
		if req.Answer != "" && (req.Answer != "accept" && req.Answer != "refuse" || !strings.HasPrefix(req.ID, "parked:")) {
			http.Error(w, "answer: accept or refuse, on a parked document", http.StatusBadRequest)
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
// user is closed with it. A PR's line already marked, state unchanged
// (a page not reloaded yet, a double click), tells nobody twice.
func markDone(b *DB, req doneRequest, at time.Time, notify func(repo, msg string) error) error {
	if req.Answer != "" {
		return answerDoc(b, req, at, notify)
	}
	if isPRWait(req.ID) {
		if seen, err := b.Marked(req.Repo, req.ID); err != nil || seen {
			return err
		}
	}
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

// answerDoc is the user's accept or refuse on a parked document. The
// master hears first, like a done. Accept is a go, closed and recorded;
// refuse waits for the user in the terminal, open, until the master
// closes it once they talked.
func answerDoc(b *DB, req doneRequest, at time.Time, notify func(repo, msg string) error) error {
	id, err := strconv.ParseInt(strings.TrimPrefix(req.ID, "parked:"), 10, 64)
	if err != nil {
		return err
	}
	p, err := b.GetParked(id)
	if err != nil {
		return err
	}
	if p.ClosedAt != nil {
		return fmt.Errorf("#%d was already closed on %s, with: %s", id, p.ClosedAt.Local().Format("02/01 15:04"), p.Answer)
	}
	what := fmt.Sprintf("parked #%d (%s of %s", id, p.Kind, p.Worker)
	if p.Ticket != "" {
		what += ", " + p.Ticket
	}
	what += ", " + p.DocPath + ")"
	var msg string
	if req.Answer == "accept" {
		msg = fmt.Sprintf("From acw board, the user ACCEPTED %s: this is their go. Queue the follow-up on its branch, with the document's path in the brief.", what)
	} else {
		msg = fmt.Sprintf("From acw board, the user REFUSED %s: send nothing, they will tell you why in the terminal; close it with acw board resume %d once you talked.", what, id)
	}
	if err := notify(req.Repo, msg); err != nil {
		return fmt.Errorf("the master was not told: %w", err)
	}
	if req.Answer == "refuse" {
		_, err := b.RefuseParked(id)
		return err
	}
	if _, err := b.CloseParked(id, "accepted", at); err != nil {
		return err
	}
	return b.AddDecision(Decision{Repo: p.Repo, At: at, Worker: p.Worker, Subject: p.Ticket, Text: "accepted",
		Why: fmt.Sprintf("the user accepted parked #%d on acw board: %s", id, firstLine(p.Text))})
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
