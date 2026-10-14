package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

// /api/queue reads the tasks of a repo the board knows, never of a path
// the request makes up.
func TestQueueRoute(t *testing.T) {
	b := open(t)
	b.UpsertWorker(Worker{Repo: "/r", Worker: "worker1", UpdatedAt: noon})
	var asked []string
	h := Handler(b, func() time.Time { return noon }, func(string, string) error { return nil }, func(repo string) (Queue, error) {
		asked = append(asked, repo)
		return Queue{Running: []QueuedTask{{ID: 3, Worker: "worker1"}}, Queued: []QueuedTask{{ID: 4, Title: "Fix the export"}}}, nil
	})
	w := get(t, h, "127.0.0.1:1", "/api/queue?repo=/r")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"worker":"worker1"`) || !strings.Contains(w.Body.String(), "Fix the export") {
		t.Errorf("known repo = %d %s", w.Code, w.Body)
	}
	if w := get(t, h, "127.0.0.1:1", "/api/queue?repo=/etc"); w.Code != http.StatusNotFound || len(asked) != 1 {
		t.Errorf("unknown repo = %d, asked %v", w.Code, asked)
	}
}

func handler(t *testing.T) (*DB, http.Handler) {
	b := open(t)
	return b, Handler(b, func() time.Time { return noon }, func(string, string) error { return nil }, nil)
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
	h := Handler(b, func() time.Time { return noon }, func(repo, msg string) error { told = append(told, repo+": "+msg); return nil }, nil)
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

// A PR's line marked twice, state unchanged, tells the master once.
func TestDoneOnAPRTellsOnce(t *testing.T) {
	b := open(t)
	var told []string
	h := Handler(b, func() time.Time { return noon }, func(repo, msg string) error { told = append(told, msg); return nil }, nil)
	body := `{"repo": "/r", "id": "pr-hole:12:1:open|green|COMMENTED by b", "text": "#12: COMMENTED by b"}`
	for range 2 {
		if w := post(t, h, "127.0.0.1:1", doneHeaders, body); w.Code != http.StatusNoContent {
			t.Fatalf("done = %d %s", w.Code, w.Body)
		}
	}
	if len(told) != 1 {
		t.Errorf("told = %q", told)
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
	h := Handler(b, func() time.Time { return noon }, func(string, string) error { return errors.New("no swarm") }, nil)
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

// The note typed with done says which option was taken: the master hears
// it, and the closed decision keeps it.
func TestDoneOnAParkedDecisionCarriesTheNote(t *testing.T) {
	b := open(t)
	var told []string
	h := Handler(b, func() time.Time { return noon }, func(repo, msg string) error { told = append(told, msg); return nil }, nil)
	id, _ := b.Park(Parked{Repo: "/r", On: "me", Text: "Reply to the client: 1 as is, 2 reworded, 3 nothing", CreatedAt: noon})
	body := fmt.Sprintf(`{"repo": "/r", "id": "parked:%d", "text": "Reply to the client", "note": " option 2, sent myself "}`, id)
	if w := post(t, h, "localhost:1", doneHeaders, body); w.Code != http.StatusNoContent {
		t.Fatalf("done = %d %s", w.Code, w.Body)
	}
	if len(told) != 1 || !strings.Contains(told[0], "Their note on it:\noption 2, sent myself") {
		t.Errorf("told = %q", told)
	}
	if p, _ := b.GetParked(id); p.Answer != "done (marked on acw board): option 2, sent myself" {
		t.Errorf("answer = %q", p.Answer)
	}
}

// A parked document is read from its file when asked, rendered; raw HTML
// and script links are dropped; a file gone, too big or not text says so.
func TestParkedDoc(t *testing.T) {
	b, h := handler(t)
	dir := t.TempDir()
	write := func(name, content string) string {
		path := dir + "/" + name
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	park := func(path string) string {
		id, _ := b.Park(Parked{Repo: "/r", On: "me", Worker: "worker2", Text: "Approve", Kind: "plan", DocPath: path, CreatedAt: noon})
		return "/api/parked/" + strconv.FormatInt(id, 10) + "/doc"
	}
	type doc struct{ HTML, Path, Error string }
	read := func(target string) (doc, int) {
		w := get(t, h, "127.0.0.1:1", target)
		var d doc
		json.Unmarshal(w.Body.Bytes(), &d)
		return d, w.Code
	}
	plan := write("plan.md", "# Plan\n\n- step one\n\n| Step | Risk |\n|---|---|\n| migration | low |\n\n<script>alert(1)</script>\n\n[x](javascript:alert(1))\n")
	d, code := read(park(plan))
	if code != 200 || !strings.Contains(d.HTML, "<h1>Plan</h1>") || !strings.Contains(d.HTML, "<li>step one</li>") || !strings.Contains(d.HTML, "<td>migration</td>") || d.Path != plan {
		t.Errorf("doc = %d %+v", code, d)
	}
	if strings.Contains(d.HTML, "<script") || strings.Contains(d.HTML, "javascript:") {
		t.Errorf("doc kept something unsafe: %s", d.HTML)
	}
	for name, target := range map[string]string{
		"gone":     park(dir + "/gone.md"),
		"too big":  park(write("big.md", strings.Repeat("a", maxDoc+1))),
		"not text": park(write("bin.md", "\xff\xfe\x00")),
	} {
		if d, code := read(target); code != 200 || d.Error == "" || d.HTML != "" {
			t.Errorf("%s = %d %+v, want an error to show", name, code, d)
		}
	}
	// A markdown image in a document loads nothing from outside.
	if csp := get(t, h, "127.0.0.1:1", "/").Header().Get("Content-Security-Policy"); csp != "img-src 'self' data:" {
		t.Errorf("page CSP = %q", csp)
	}
	noDoc, _ := b.Park(Parked{Repo: "/r", On: "client", Text: "x", CreatedAt: noon})
	for _, target := range []string{"/api/parked/999/doc", "/api/parked/" + strconv.FormatInt(noDoc, 10) + "/doc"} {
		if _, code := read(target); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, code)
		}
	}
}

// Accept is the user's go: the master is told so, the decision closed
// and recorded. Refuse tells the master to wait for the user, and leaves
// it open, marked. A second answer to a closed one is refused.
func TestAcceptRefuse(t *testing.T) {
	b := open(t)
	var told []string
	h := Handler(b, func() time.Time { return noon }, func(repo, msg string) error { told = append(told, msg); return nil }, nil)
	answer := func(id int64, answer string) int {
		body := fmt.Sprintf(`{"repo": "/r", "id": "parked:%d", "ticket": "SHOP-9", "text": "Read worker2's plan", "answer": %q}`, id, answer)
		return post(t, h, "127.0.0.1:1", doneHeaders, body).Code
	}
	refusedID, _ := b.Park(Parked{Repo: "/r", Ticket: "SHOP-9", On: "me", Worker: "worker2", Text: "Approve the plan", Kind: "plan", DocPath: "/p.md", CreatedAt: noon})
	if code := answer(refusedID, "refuse"); code != http.StatusNoContent {
		t.Fatalf("refuse = %d", code)
	}
	if p, _ := b.GetParked(refusedID); p.ClosedAt != nil || !p.Refused {
		t.Errorf("refused = %+v, want open, refused", p)
	}
	if len(told) != 1 || !strings.Contains(told[0], "REFUSED") || !strings.Contains(told[0], "terminal") {
		t.Errorf("told = %q", told)
	}
	id, _ := b.Park(Parked{Repo: "/r", Ticket: "SHOP-9", On: "me", Worker: "worker2", Text: "Approve the plan", Kind: "plan", DocPath: "/p.md", Branch: "fix/vat", CreatedAt: noon})
	if code := answer(id, "accept"); code != http.StatusNoContent {
		t.Fatalf("accept = %d", code)
	}
	p, _ := b.GetParked(id)
	if p.ClosedAt == nil || p.Answer != "accepted" {
		t.Errorf("accepted = %+v", p)
	}
	// fix/vat is not cut in /r: a --branch on it would fail to fetch.
	if len(told) != 2 || !strings.Contains(told[1], "ACCEPTED") || !strings.Contains(told[1], "go") || !strings.Contains(told[1], "/p.md") || strings.Contains(told[1], "--branch") {
		t.Errorf("told = %q", told)
	}
	// Cut, it is suggested.
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init"}, {"branch", "fix/vat"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cut, _ := b.Park(Parked{Repo: repo, Ticket: "SHOP-9", On: "me", Worker: "worker2", Text: "Approve", Kind: "plan", DocPath: "/p.md", Branch: "fix/vat", CreatedAt: noon})
	if code := answer(cut, "accept"); code != http.StatusNoContent || len(told) != 3 || !strings.Contains(told[2], "--branch fix/vat") {
		t.Errorf("accept of a cut branch = %d, told = %q", code, told)
	}
	if d, _ := b.Board("/r", noon, noon); len(d.Decisions) != 1 || d.Decisions[0].Text != "accepted" || d.Decisions[0].Subject != "SHOP-9" {
		t.Errorf("decisions = %+v", d.Decisions)
	}
	if code := answer(id, "refuse"); code == http.StatusNoContent {
		t.Error("refusing an accepted one went through")
	}
	if code := answer(id, "maybe"); code != http.StatusBadRequest {
		t.Errorf("unknown answer = %d, want 400", code)
	}
}

// Notes typed with an answer reach the master: with a refuse, they are
// the reasons, and the master no longer waits for the terminal.
func TestAnswerNotes(t *testing.T) {
	b := open(t)
	var told []string
	h := Handler(b, func() time.Time { return noon }, func(repo, msg string) error { told = append(told, msg); return nil }, nil)
	answer := func(answer, note string) {
		t.Helper()
		id, _ := b.Park(Parked{Repo: "/r", Ticket: "SHOP-9", On: "me", Worker: "worker2", Text: "Approve the plan", Kind: "plan", DocPath: "/p.md", CreatedAt: noon})
		body := fmt.Sprintf(`{"repo": "/r", "id": "parked:%d", "text": "Read worker2's plan", "answer": %q, "note": %q}`, id, answer, note)
		if code := post(t, h, "127.0.0.1:1", doneHeaders, body).Code; code != http.StatusNoContent {
			t.Fatalf("%s = %d", answer, code)
		}
	}
	answer("refuse", "  step 3 drops the VAT rounding\n")
	if len(told) != 1 || !strings.Contains(told[0], "REFUSED") || !strings.Contains(told[0], "step 3 drops the VAT rounding") || strings.Contains(told[0], "tell you why") {
		t.Errorf("refused with notes, told = %q", told)
	}
	answer("accept", "keep the old endpoint\nuntil Friday")
	if len(told) != 2 || !strings.Contains(told[1], "ACCEPTED") || !strings.Contains(told[1], "keep the old endpoint\nuntil Friday") {
		t.Errorf("accepted with notes, told = %q", told)
	}
	if d, _ := b.Board("/r", noon, noon); len(d.Decisions) != 1 || !strings.Contains(d.Decisions[0].Why, "notes: keep the old endpoint until Friday") {
		t.Errorf("decisions = %+v", d.Decisions)
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
