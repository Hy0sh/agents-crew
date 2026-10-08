package board

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	return b, Handler(b, func() time.Time { return noon }, func(string, string) error { return nil })
}

func post(t *testing.T, h http.Handler, host string, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/done", strings.NewReader(body))
	r.Host = host
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const doneBody = `{"repo": "/r", "id": "worker:worker1:plan_ready:1", "ticket": "SHOP-7", "text": "Approve worker1's plan", "since": "2026-10-06T11:00:00Z"}`

var doneHeaders = map[string]string{"X-Acw-Board": "1", "Content-Type": "application/json; charset=utf-8"}

// Marked done: the master is told, then the mark is kept.
func TestDone(t *testing.T) {
	b := open(t)
	var told []string
	h := Handler(b, func() time.Time { return noon }, func(repo, msg string) error { told = append(told, repo+": "+msg); return nil })
	if w := post(t, h, "127.0.0.1:1", doneHeaders, doneBody); w.Code != http.StatusNoContent {
		t.Fatalf("done = %d %s", w.Code, w.Body)
	}
	if len(told) != 1 || !strings.Contains(told[0], "/r: From acw board, the user marked done: SHOP-7 Approve worker1's plan") || !strings.Contains(told[0], "not an approval") {
		t.Errorf("told = %q", told)
	}
	if d, _ := b.Board("/r", noon, noon); !d.Marks["worker:worker1:plan_ready:1"].Equal(noon) {
		t.Errorf("marks = %v", d.Marks)
	}
}

// Another site open in the browser can't write: it can't set the header
// without a preflight nobody answers, nor reach us under another name.
func TestDoneRefusals(t *testing.T) {
	_, h := handler(t)
	for name, c := range map[string]struct {
		host    string
		headers map[string]string
		body    string
		code    int
	}{
		"no header":    {"127.0.0.1:1", map[string]string{"Content-Type": "application/json"}, doneBody, http.StatusForbidden},
		"form post":    {"127.0.0.1:1", map[string]string{"X-Acw-Board": "1", "Content-Type": "text/plain"}, doneBody, http.StatusForbidden},
		"foreign host": {"evil.example:1", doneHeaders, doneBody, http.StatusForbidden},
		"no text":      {"127.0.0.1:1", doneHeaders, `{"repo": "/r", "id": "x"}`, http.StatusBadRequest},
	} {
		if w := post(t, h, c.host, c.headers, c.body); w.Code != c.code {
			t.Errorf("%s = %d, want %d", name, w.Code, c.code)
		}
	}
}

// A master that could not be told leaves no mark: the line stays.
func TestDoneUnheardLeavesNoMark(t *testing.T) {
	b := open(t)
	h := Handler(b, func() time.Time { return noon }, func(string, string) error { return errors.New("no swarm") })
	if w := post(t, h, "127.0.0.1:1", doneHeaders, doneBody); w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "no swarm") {
		t.Errorf("done = %d %s", w.Code, w.Body)
	}
	if d, _ := b.Board("/r", noon, noon); len(d.Marks) != 0 {
		t.Errorf("marks = %v", d.Marks)
	}
}

// Done on a decision parked on the user closes it.
func TestDoneClosesAParkedDecision(t *testing.T) {
	b, h := handler(t)
	id, _ := b.Park(Parked{Repo: "/r", On: "me", Text: "Answer the reviewer", CreatedAt: noon})
	body := `{"repo": "/r", "id": "parked:` + strconv.FormatInt(id, 10) + `", "text": "Answer the reviewer"}`
	if w := post(t, h, "localhost:1", doneHeaders, body); w.Code != http.StatusNoContent {
		t.Fatalf("done = %d %s", w.Code, w.Body)
	}
	if p, _ := b.GetParked(id); p.ClosedAt == nil {
		t.Errorf("parked = %+v, want closed", p)
	}
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
	var d View
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil || d.Waits == nil || d.Tickets == nil || d.Workers == nil {
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
