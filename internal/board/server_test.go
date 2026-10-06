package board

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, h http.Handler, host, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", target, nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func handler(t *testing.T) (*DB, http.Handler) {
	b := open(t)
	return b, Handler(b, func() time.Time { return noon })
}

// DNS rebinding: a page on another name pointing at 127.0.0.1 must not
// read the board.
func TestForeignHostRefused(t *testing.T) {
	_, h := handler(t)
	if w := get(t, h, "evil.example:8080", "/api/repos"); w.Code != http.StatusForbidden {
		t.Errorf("foreign host = %d, want 403", w.Code)
	}
	for _, host := range []string{"127.0.0.1:5000", "localhost:5000"} {
		if w := get(t, h, host, "/api/repos"); w.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", host, w.Code)
		}
	}
}

func TestBoardEmptyBase(t *testing.T) {
	_, h := handler(t)
	if w := get(t, h, "127.0.0.1:1", "/api/repos"); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("repos = %s", w.Body)
	}
	w := get(t, h, "127.0.0.1:1", "/api/board")
	var d Day
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil || d.Workers == nil || d.Decisions == nil {
		t.Errorf("board = %s, %v", w.Body, err)
	}
}

// No repo given: the most recently active one, today.
func TestBoardDefaultsToLatestRepoToday(t *testing.T) {
	b, h := handler(t)
	b.AddDecision(Decision{Repo: "/r", At: noon, Text: "VAT per line"})
	w := get(t, h, "127.0.0.1:1", "/api/board")
	if !strings.Contains(w.Body.String(), "VAT per line") || !strings.Contains(w.Body.String(), `"live":true`) {
		t.Errorf("board = %s", w.Body)
	}
	if w := get(t, h, "127.0.0.1:1", "/api/board?repo=/r&day=2026-10-05"); strings.Contains(w.Body.String(), "VAT") {
		t.Errorf("the 5th shows the 6th: %s", w.Body)
	}
	if w := get(t, h, "127.0.0.1:1", "/api/board?day=yesterday"); w.Code != http.StatusBadRequest {
		t.Errorf("bad day = %d, want 400", w.Code)
	}
}

func TestPageServed(t *testing.T) {
	_, h := handler(t)
	w := get(t, h, "localhost:1", "/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>acw board</title>") {
		t.Errorf("page = %d", w.Code)
	}
}
