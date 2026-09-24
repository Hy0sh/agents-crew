package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/decision"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
)

const testToken = "secret"

// uiFixture is a state dir holding one live project with one open
// decision blocking worker2.
func uiFixture(t *testing.T) (http.Handler, string, projectInfo) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "some-repo")
	project := projectInfo{Repo: repo, Label: "some-repo", Inbox: filepath.Join(t.TempDir(), "inbox"), SessionID: "sess-1", BriefHead: "Tu es la session master"}
	slug := names.Slug(repo)
	if err := writeJSON(names.ProjectFile(slug), project); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Add(names.DecisionsFile(slug), decision.Decision{
		Task: "ticket 142", Question: "Lignes archivées ?", Blocks: "worker2", Reco: 1,
		Options: []decision.Option{{Label: "Non"}, {Label: "Oui", Consequence: "+1 colonne"}},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	agents := func() ([]herdr.Agent, error) {
		return []herdr.Agent{
			{Name: names.Master(slug)},
			{Name: names.Worker(slug, 1), Status: "working"},
			{Name: names.Worker(slug, 2), Status: "idle"},
		}, nil
	}
	prompt := func(name, text string) error {
		if text == "bloqué" {
			return errors.New(`herdr [agent prompt]: exit status 1: {"error":"agent_blocked"}`)
		}
		sent = append(sent, name+": "+text)
		return nil
	}
	sent = nil
	return newUIHandler(serverInfo{Port: 4242, Token: testToken}, agents, prompt), slug, project
}

// sent records what the fixture's master was typed.
var sent []string

func request(method, target, host, origin, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

// An answer becomes an instruction to a master allowed to push: only the
// page itself, with the token, may send one.
func TestUIRefusesForeignRequests(t *testing.T) {
	h, slug, _ := uiFixture(t)
	post := "/api/decisions/" + slug + "/D1?t=" + testToken
	cases := map[string]*http.Request{
		"no token":       request("GET", "/api/state", "127.0.0.1:4242", "", ""),
		"wrong token":    request("GET", "/api/state?t=nope", "127.0.0.1:4242", "", ""),
		"rebound host":   request("GET", "/api/state?t="+testToken, "evil.example:4242", "", ""),
		"foreign origin": request("POST", post, "127.0.0.1:4242", "https://evil.example", `{"option":1}`),
		"no origin":      request("POST", post, "127.0.0.1:4242", "", `{"option":1}`),
	}
	for name, r := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, w.Code)
		}
	}
}

func TestUIStateShowsWorkersAndDecisions(t *testing.T) {
	h, _, _ := uiFixture(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/api/state?t="+testToken, "127.0.0.1:4242", "", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var s uiStateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Projects) != 1 || !s.Projects[0].Live || len(s.Projects[0].Decisions) != 1 {
		t.Fatalf("state = %+v, want one live project with one decision", s)
	}
	ws := s.Projects[0].Workers
	if len(ws) != 2 || ws[0].State != "active" || ws[1].State != "waiting" {
		t.Errorf("workers = %+v, want worker1 active and worker2 waiting on D1", ws)
	}
}

func TestUIAnswerClosesTheDecisionAndTellsTheMaster(t *testing.T) {
	h, slug, project := uiFixture(t)
	answer := func() int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/api/decisions/"+slug+"/D1?t="+testToken, "127.0.0.1:4242",
			"http://127.0.0.1:4242", `{"option":2,"comment":"mais masquée par défaut"}`))
		return w.Code
	}
	if code := answer(); code != http.StatusNoContent {
		t.Fatalf("answer status %d, want 204", code)
	}
	d, _ := decision.Get(names.DecisionsFile(slug), "D1")
	if d.Status != decision.Answered || d.AnsweredVia != "ui" || d.Answer != "option 2 « Oui ». mais masquée par défaut" {
		t.Errorf("decision = %+v", d)
	}
	inbox, _ := os.ReadFile(project.Inbox)
	if !strings.Contains(string(inbox), "D1 tranchée") || !strings.Contains(string(inbox), "show D1") {
		t.Errorf("inbox = %q, want a line pointing the master at D1", inbox)
	}
	if code := answer(); code != http.StatusConflict {
		t.Errorf("second answer status %d, want 409", code)
	}
}

func TestUIConversation(t *testing.T) {
	h, slug, _ := uiFixture(t)
	claude := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	dir := filepath.Join(claude, "projects", "-some-repo")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "sess-1.jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/api/conversation/"+slug+"?t="+testToken, "127.0.0.1:4242", "", ""))
	var got struct {
		MasterState string        `json:"master_state"`
		Available   bool          `json:"available"`
		Messages    []chatMessage `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if !got.Available || got.MasterState != "waiting" || len(got.Messages) != 2 {
		t.Errorf("conversation = %+v, want the transcript's two messages and an idle master", got)
	}

	send := func(text string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/api/conversation/"+slug+"?t="+testToken, "127.0.0.1:4242",
			"http://127.0.0.1:4242", `{"text":"`+text+`"}`))
		return w.Code
	}
	if code := send("Et le ticket 131 ?"); code != http.StatusNoContent || len(sent) != 1 || sent[0] != names.Master(slug)+": Et le ticket 131 ?" {
		t.Errorf("send = %d, typed %v; want the message typed to the master", code, sent)
	}
	if code := send("bloqué"); code != http.StatusConflict {
		t.Errorf("send to a master on an approval prompt = %d, want 409", code)
	}
	if code := send("  "); code != http.StatusBadRequest {
		t.Errorf("empty message = %d, want 400", code)
	}
}

func TestWorkerState(t *testing.T) {
	for _, c := range []struct {
		herdr   string
		blocked bool
		want    string
	}{
		{"working", false, "active"},
		{"working", true, "waiting"},
		{"blocked", false, "waiting"},
		{"idle", false, "idle"},
		{"done", false, "idle"},
		{"", false, "idle"},
	} {
		if got := workerState(c.herdr, c.blocked); got != c.want {
			t.Errorf("workerState(%q, %v) = %s, want %s", c.herdr, c.blocked, got, c.want)
		}
	}
}
