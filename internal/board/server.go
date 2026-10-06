package board

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"time"
)

//go:embed page.html
var page []byte

// Handler serves the page and its two read-only routes. Only a request
// naming 127.0.0.1 or localhost gets through: a site whose name resolves
// to 127.0.0.1 (DNS rebinding) would otherwise read the decisions.
func Handler(b *DB, now func() time.Time) http.Handler {
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
		reply(w, d, err)
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

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
