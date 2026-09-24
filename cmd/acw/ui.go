package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hy0sh/agents-crew/internal/decision"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/journal"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// The UI is one local page over every swarm: each worker's state, the
// day's journal per project, and the decisions waiting on the user, which
// they answer there. One server for all projects, started by the first
// acw that finds none running, stopped once no master is left.

const uiUse = "__ui"

//go:embed ui.html
var uiPage []byte

// serverInfo is server.json: where the running server listens.
type serverInfo struct {
	Port  int    `json:"port"`
	PID   int    `json:"pid"`
	Token string `json:"token"`
}

// projectInfo is a project's project.json, written at each launch.
type projectInfo struct {
	Repo  string `json:"repo"`
	Label string `json:"label"`
	Inbox string `json:"inbox,omitempty"`
	// SessionID is the claude master's session, whose transcript the page
	// shows; BriefHead is how the brief is told apart in it.
	SessionID string `json:"session_id,omitempty"`
	BriefHead string `json:"brief_head,omitempty"`
}

func (s serverInfo) url() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", s.Port, s.Token)
}

func newUICmd() *cobra.Command {
	return &cobra.Command{
		Use:    uiUse,
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serveUI(herdr.AgentList, time.Minute)
		},
	}
}

// serveUI listens on a free local port until no master is left in Herdr.
// An error listing agents keeps it running: a Herdr hiccup must not take
// the page down under the user.
func serveUI(agents func() ([]herdr.Agent, error), every time.Duration) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	info := serverInfo{Port: ln.Addr().(*net.TCPAddr).Port, PID: os.Getpid(), Token: hex.EncodeToString(token)}
	if err := writeJSON(names.ServerFile(), info); err != nil {
		return err
	}
	defer os.Remove(names.ServerFile())

	srv := &http.Server{Handler: newUIHandler(info, agents, herdr.AgentPrompt), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		for range time.Tick(every) {
			list, err := agents()
			if err == nil && !anyMaster(list) {
				srv.Shutdown(context.Background())
				return
			}
		}
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func anyMaster(agents []herdr.Agent) bool {
	for _, a := range agents {
		if strings.HasPrefix(a.Name, "master-") {
			return true
		}
	}
	return false
}

// newUIHandler serves the page and its API. Every request must carry the
// token and the exact local Host, and a POST the page's own Origin: an
// answer becomes an instruction to a master allowed to commit and push,
// so neither another site open in the browser (CSRF) nor a DNS name
// rebound to 127.0.0.1 may send one.
func newUIHandler(info serverInfo, agents func() ([]herdr.Agent, error), prompt func(name, text string) error) http.Handler {
	host := fmt.Sprintf("127.0.0.1:%d", info.Port)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(uiPage)
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		day := r.URL.Query().Get("day")
		if _, err := time.Parse("2006-01-02", day); err != nil {
			day = journal.Day(time.Now())
		}
		list, _ := agents()
		state, err := uiState(day, list)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(state)
	})
	mux.HandleFunc("POST /api/decisions/{slug}/{id}", answerDecision)
	mux.HandleFunc("GET /api/conversation/{slug}", func(w http.ResponseWriter, r *http.Request) {
		info, ok := readProject(w, r.PathValue("slug"))
		if !ok {
			return
		}
		out := struct {
			MasterState string        `json:"master_state"` // working, waiting, or stopped
			Available   bool          `json:"available"`    // a transcript was found
			Messages    []chatMessage `json:"messages"`
		}{MasterState: "stopped", Messages: []chatMessage{}}
		list, _ := agents()
		if a, ok := herdr.FindAgent(list, names.Master(r.PathValue("slug"))); ok {
			out.MasterState = "waiting"
			if a.Status == "working" {
				out.MasterState = "working"
			}
		}
		if path := transcriptPath(info.SessionID); info.SessionID != "" && path != "" {
			msgs, err := readConversation(path, info.BriefHead)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out.Available = true
			if len(msgs) > 200 {
				msgs = msgs[len(msgs)-200:]
			}
			out.Messages = append(out.Messages, msgs...)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	// Typed into the master's input, as if from its terminal: it lands in
	// the conversation as the user's own message, and Claude Code queues it
	// while the master works.
	mux.HandleFunc("POST /api/conversation/{slug}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := readProject(w, r.PathValue("slug")); !ok {
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
			http.Error(w, "message vide", http.StatusBadRequest)
			return
		}
		if err := prompt(names.Master(r.PathValue("slug")), strings.TrimSpace(body.Text)); err != nil {
			if strings.Contains(err.Error(), "agent_blocked") {
				http.Error(w, "le master attend une réponse dans son terminal (approbation ou question)", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("t")
		if token == "" {
			token = r.Header.Get("X-Acw-Token")
		}
		if r.Host != host || subtle.ConstantTimeCompare([]byte(token), []byte(info.Token)) != 1 ||
			(r.Method != http.MethodGet && r.Header.Get("Origin") != "http://"+host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type uiWorker struct {
	Label     string `json:"label"`
	State     string `json:"state"` // active, waiting or idle
	Tache     string `json:"tache"`
	Summary   string `json:"summary"`
	UpdatedAt string `json:"updated_at"`
	PRURL     string `json:"pr_url"`
}

type uiProject struct {
	Slug      string              `json:"slug"`
	Repo      string              `json:"repo"`
	Label     string              `json:"label"`
	Live      bool                `json:"live"`
	Workers   []uiWorker          `json:"workers"`
	Decisions []decision.Decision `json:"decisions"` // open ones, and those closed on the day shown
	Journal   []journal.Entry     `json:"journal"`
}

type uiStateResponse struct {
	Day      string      `json:"day"`
	Days     []string    `json:"days"`
	Projects []uiProject `json:"projects"`
}

// uiState gathers every project under the state dir for day.
func uiState(day string, agents []herdr.Agent) (uiStateResponse, error) {
	out := uiStateResponse{Day: day}
	dirs, err := filepath.Glob(filepath.Join(names.StateDir(), "*", "project.json"))
	if err != nil {
		return out, err
	}
	days := map[string]bool{day: true}
	for _, pf := range dirs {
		var info projectInfo
		content, err := os.ReadFile(pf)
		if err != nil || json.Unmarshal(content, &info) != nil {
			continue
		}
		slug := filepath.Base(filepath.Dir(pf))
		p := uiProject{Slug: slug, Repo: info.Repo, Label: info.Label}
		_, p.Live = herdr.FindAgent(agents, names.Master(slug))

		all, err := decision.List(names.DecisionsFile(slug))
		if err != nil {
			return out, err
		}
		for _, d := range all {
			if d.Status == decision.Open || journal.Day(d.AnsweredAt) == day {
				p.Decisions = append(p.Decisions, d)
			}
		}
		if p.Journal, err = journal.Read(names.JournalDir(slug), day); err != nil {
			return out, err
		}
		jdays, _ := journal.Days(names.JournalDir(slug))
		for _, d := range jdays {
			days[d] = true
		}
		if p.Live {
			p.Workers = uiWorkers(info.Repo, slug, agents, all)
		}
		out.Projects = append(out.Projects, p)
	}
	for d := range days {
		out.Days = append(out.Days, d)
	}
	sort.Strings(out.Days)
	sort.Slice(out.Projects, func(i, j int) bool { return out.Projects[i].Label < out.Projects[j].Label })
	return out, nil
}

// uiWorkers lists a live swarm's workers from Herdr, with what each one's
// status file says.
func uiWorkers(repo, slug string, agents []herdr.Agent, decisions []decision.Decision) []uiWorker {
	var out []uiWorker
	for i := 1; ; i++ {
		a, ok := herdr.FindAgent(agents, names.Worker(slug, i))
		if !ok {
			break
		}
		label := fmt.Sprintf("worker%d", i)
		w := uiWorker{Label: label, State: workerState(a.Status, blockedOn(decisions, label, a.Name))}
		var status map[string]any
		if content, err := os.ReadFile(filepath.Join(names.StatusDir(repo), label+".json")); err == nil && json.Unmarshal(content, &status) == nil {
			str := func(k string) string {
				if v, ok := status[k]; ok && v != nil {
					return fmt.Sprint(v)
				}
				return ""
			}
			w.Tache, w.Summary, w.UpdatedAt, w.PRURL = str("tache"), str("summary"), str("updated_at"), str("pr_url")
		}
		out = append(out, w)
	}
	return out
}

// blockedOn reports whether an open decision names this worker, by its
// short label or its full agent name, whichever the master used.
func blockedOn(decisions []decision.Decision, label, name string) bool {
	for _, d := range decisions {
		if d.Status == decision.Open && (d.Blocks == label || d.Blocks == name) {
			return true
		}
	}
	return false
}

// workerState is what the page shows for a worker: waiting when it waits
// on the user (a decision, or a prompt Herdr recognized), active when it
// works, idle otherwise.
func workerState(herdrStatus string, blocked bool) string {
	switch {
	case blocked || herdrStatus == "blocked":
		return "waiting"
	case herdrStatus == "working":
		return "active"
	default:
		return "idle"
	}
}

// slugPattern is what names.Slug makes; anything else in the URL must not
// reach a file path.
var slugPattern = regexp.MustCompile(`^[0-9a-f]{6}$`)

// readProject reads slug's project.json, answering 404 itself when there
// is none.
func readProject(w http.ResponseWriter, slug string) (projectInfo, bool) {
	var info projectInfo
	if !slugPattern.MatchString(slug) {
		http.Error(w, "projet inconnu", http.StatusNotFound)
		return info, false
	}
	content, err := os.ReadFile(names.ProjectFile(slug))
	if err != nil || json.Unmarshal(content, &info) != nil {
		http.Error(w, "projet inconnu", http.StatusNotFound)
		return info, false
	}
	return info, true
}

// answerDecision records the user's answer and tells the master through
// its inbox, pointing at the answer rather than carrying it: the inbox is
// read line by line, and a comment may hold several.
func answerDecision(w http.ResponseWriter, r *http.Request) {
	slug, id := r.PathValue("slug"), r.PathValue("id")
	info, ok := readProject(w, slug)
	if !ok {
		return
	}
	if info.Inbox == "" {
		http.Error(w, "projet sans inbox", http.StatusNotFound)
		return
	}
	var body struct {
		Option  *int   `json:"option"` // from 1
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path := names.DecisionsFile(slug)
	d, err := decision.Get(path, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	answer, err := answerText(d, body.Option, body.Comment)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d, err = decision.Close(path, id, answer, "ui", time.Now())
	if errors.Is(err, decision.ErrClosed) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(d)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	self, _ := os.Executable()
	line := fmt.Sprintf("%s tranchée par l'utilisateur (%s). Lis la réponse avec %s %s --repo %s show %s, puis relaie-la au worker concerné.",
		d.ID, strings.Join(strings.Fields(d.Task), " "), shellWord(self), decisionUse, shellWord(info.Repo), d.ID)
	if err := appendLine(info.Inbox, line); err != nil {
		// Answer kept: the master still finds it with show, the user is
		// told to mention it in the terminal.
		http.Error(w, "réponse enregistrée mais master non prévenu : "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// answerText is the answer as the master reads it: the option picked,
// then the comment.
func answerText(d decision.Decision, option *int, comment string) (string, error) {
	comment = strings.TrimSpace(comment)
	var parts []string
	if option != nil {
		i := *option - 1
		if i < 0 || i >= len(d.Options) {
			return "", fmt.Errorf("option %d inexistante", *option)
		}
		parts = append(parts, "option "+strconv.Itoa(i+1)+" « "+d.Options[i].Label+" »")
	}
	if comment != "" {
		parts = append(parts, comment)
	}
	if len(parts) == 0 {
		return "", errors.New("ni option ni commentaire")
	}
	return strings.Join(parts, ". "), nil
}

// ensureUI writes the project's project.json and makes sure a UI server
// runs, starting one detached when none answers. It returns the page's
// URL, "" when it couldn't; the swarm runs fine without it.
func ensureUI(exe string, project projectInfo) (string, error) {
	slug := names.Slug(project.Repo)
	if err := writeJSON(names.ProjectFile(slug), project); err != nil {
		return "", err
	}
	if info, ok := runningUI(); ok {
		return info.url(), nil
	}
	os.Remove(names.ServerFile())
	logFile, err := os.Create(filepath.Join(os.TempDir(), "acw-ui.log"))
	if err != nil {
		return "", err
	}
	cmd := exec.Command(exe, uiUse)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	// ponytail: two acw launched in the same second can each start a
	// server; the first one's then serves nobody until it stops itself.
	for range 20 {
		time.Sleep(100 * time.Millisecond)
		if info, ok := runningUI(); ok {
			// Opened for the user once, when the server is new: the
			// launch line printing the URL is replaced by the Herdr TUI
			// right after.
			openBrowser(info.url())
			return info.url(), nil
		}
	}
	return "", fmt.Errorf("le serveur ne répond pas (voir %s)", logFile.Name())
}

// openBrowser is best effort: no opener, no page opened, the URL is still
// on the launch line.
func openBrowser(url string) {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	exec.Command(opener, url).Start()
}

// runningUI reads server.json and checks the server there answers.
func runningUI() (serverInfo, bool) {
	var info serverInfo
	content, err := os.ReadFile(names.ServerFile())
	if err != nil || json.Unmarshal(content, &info) != nil {
		return info, false
	}
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/state?t=%s", info.Port, info.Token))
	if err != nil {
		return info, false
	}
	resp.Body.Close()
	return info, resp.StatusCode == http.StatusOK
}

// writeJSON writes v to path atomically, creating its directory.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
