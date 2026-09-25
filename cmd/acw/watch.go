package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
)

const watchUse = "__watch"

// watchPlan is what the detached watcher needs, passed as one JSON
// argument like provisionPlan.
type watchPlan struct {
	Repo       string `json:"repo"`
	MasterName string `json:"master_name"`
	// Inbox is where messages go, "" when they are typed into the master.
	Inbox string `json:"inbox,omitempty"`
	// InboxNext is the command the master reruns to read its inbox, named
	// in the reminder when it forgets to.
	InboxNext      string          `json:"inbox_next,omitempty"`
	SilenceMinutes int             `json:"silence_minutes"`
	Workers        []watchedWorker `json:"workers"`
	// Stamp is the run's, also written in the status dir: a watcher whose
	// stamp is no longer there belongs to a swarm that is gone.
	Stamp string `json:"stamp"`
}

// ownsStatusDir reports whether the status dir still belongs to the run
// stamped stamp. A watcher can outlive its swarm: acw stop only removes
// the dir once it found the master, and a stop then a start within one
// poll recreates it at once, for a new swarm with the same worker names.
func ownsStatusDir(statusDir, stamp string) bool {
	content, err := os.ReadFile(filepath.Join(statusDir, "stamp"))
	return err == nil && strings.TrimSpace(string(content)) == stamp
}

type watchedWorker struct {
	Name   string `json:"name"`  // herdr agent name
	Label  string `json:"label"` // worker1, as in its status file's name
	Hooked bool   `json:"hooked"`
}

// runWatch polls the workers every interval until acw stop removes the
// status directory, and sends the master what observe finds worth it.
// Nothing here is fatal: a failed poll or delivery is logged, and the
// next poll tries again.
func runWatch(plan watchPlan, interval time.Duration) {
	statusDir := names.StatusDir(plan.Repo)
	w := newWatcher(time.Duration(plan.SilenceMinutes) * time.Minute)
	for {
		if !ownsStatusDir(statusDir, plan.Stamp) {
			return
		}
		agents, err := herdr.AgentList()
		if err != nil {
			fmt.Fprintln(os.Stderr, "herdr agent list:", err)
			time.Sleep(interval)
			continue
		}
		// The master is started before the watcher: gone from a list that
		// did answer, the swarm was closed without acw stop.
		if _, ok := herdr.FindAgent(agents, plan.MasterName); !ok {
			fmt.Fprintln(os.Stderr, "master introuvable, le veilleur s'arrête")
			return
		}
		now := time.Now()
		var views []workerView
		agentNames := map[string]string{}
		for _, ww := range plan.Workers {
			a, ok := herdr.FindAgent(agents, ww.Name)
			if !ok {
				continue
			}
			agentNames[ww.Label] = ww.Name
			views = append(views, workerView{
				Label:    ww.Label,
				Hooked:   ww.Hooked,
				Status:   a.Status,
				Activity: activity(filepath.Join(statusDir, ww.Label+".json"), a.Cwd),
			})
		}
		for _, e := range w.observe(now, views) {
			deliver(plan, eventMessage(statusDir, agentNames[e.Label], e))
		}
		if plan.Inbox != "" {
			info, err := os.Stat(plan.Inbox)
			if w.remindInbox(now, err == nil && info.Size() > 0) {
				remind := "Des messages acw attendent dans ton inbox depuis plus de 5 min : relance `" + plan.InboxNext + "` en arrière-plan."
				if err := herdr.AgentPrompt(plan.MasterName, remind); err != nil {
					fmt.Fprintln(os.Stderr, "rappel d'inbox:", err)
				}
			}
		}
		time.Sleep(interval)
	}
}

// activity is the latest of what acw can read about a worker without
// trusting it: last_turn_end (stamped by acw), its status file's mtime,
// and the latest change in its worktree.
func activity(statusPath, worktree string) time.Time {
	var latest time.Time
	later := func(t time.Time) {
		if t.After(latest) {
			latest = t
		}
	}
	if info, err := os.Stat(statusPath); err == nil {
		later(info.ModTime())
	}
	if content, err := os.ReadFile(statusPath); err == nil {
		var status struct {
			LastTurnEnd string `json:"last_turn_end"`
		}
		if json.Unmarshal(content, &status) == nil {
			if t, err := time.Parse(time.RFC3339, status.LastTurnEnd); err == nil {
				later(t)
			}
		}
	}
	if worktree != "" {
		later(gitutil.LastActivity(worktree))
	}
	return latest
}

// eventMessage builds the text for one event. A blocked worker's pane is
// read now, while the prompt is still on it.
func eventMessage(statusDir, agentName string, e watchEvent) string {
	switch e.Kind {
	case eventBlocked:
		pane, err := herdr.AgentRead(agentName, 40)
		if err != nil {
			pane = "(pane illisible : " + err.Error() + ")"
		}
		return blockedMessage(e.Label, countBlock(filepath.Join(statusDir, e.Label+".blocks")), pane)
	case eventSilent:
		return silentMessage(e.Label, e.For)
	default:
		return pingMessage(e.Label)
	}
}

// countBlock adds one to the worker's block count and returns it. Kept on
// disk so that a context reset of the worker, from another process, can
// start it over (a new task).
func countBlock(path string) int {
	content, _ := os.ReadFile(path)
	n, _ := strconv.Atoi(strings.TrimSpace(string(content)))
	n++
	if err := os.WriteFile(path, []byte(strconv.Itoa(n)+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "compteur de blocages:", err)
	}
	return n
}

func deliver(plan watchPlan, msg string) {
	var err error
	if plan.Inbox != "" {
		err = appendLine(plan.Inbox, msg)
	} else {
		err = herdr.AgentPrompt(plan.MasterName, msg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "message au master:", err)
	}
}

// paneTail is how many non-empty lines of a blocked worker's pane go into
// the message: enough for the pending command and its question.
const paneTail = 12

func blockedMessage(label string, count int, pane string) string {
	var lines []string
	for _, l := range strings.Split(pane, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > paneTail {
		lines = lines[len(lines)-paneTail:]
	}
	msg := fmt.Sprintf("%s est bloqué : attente probable d'une approbation d'outil ou d'une question. Dernières lignes de son pane :\n%s",
		label, strings.Join(lines, "\n"))
	if count >= 2 {
		msg = fmt.Sprintf("%de blocage depuis sa dernière réinitialisation : il bute peut-être sur une interdiction. ", count) + msg
	}
	return msg
}

func silentMessage(label string, d time.Duration) string {
	return fmt.Sprintf("%s travaille depuis %d min sans activité visible (ni fin de tour, ni statut, ni fichier modifié dans son worktree). "+
		"Sonde son worktree ou son pane.", label, int(d.Minutes()))
}

// acw's watcher replaces the master's own polling: Herdr has no push
// notification for agent state, and a master told to keep an `agent wait`
// running on every worker let it lapse, or re-armed it for nothing every
// five minutes. The watcher polls instead and messages the master only
// when something is worth a turn. What is worth one is decided here, as a
// pure function of successive polls; runWatch does the reading and the
// delivery.

// inboxReminderAfter is how long messages may wait unread before acw
// types a reminder into the master's input: the master forgot to run
// __inbox-next again, and nothing else would ever tell it.
const inboxReminderAfter = 5 * time.Minute

// workerView is one worker as one poll sees it.
type workerView struct {
	Label  string // worker1
	Hooked bool   // has the Stop hook (claude), which already pings on turn end
	Status string // herdr's agent_status
	// Activity is the latest sign of work acw can read without trusting
	// the worker: its last turn end, its status file's mtime, and the
	// latest change in its worktree.
	Activity time.Time
}

type eventKind int

const (
	eventBlocked eventKind = iota
	eventSilent
	eventTurnEnd
)

type watchEvent struct {
	Label string
	Kind  eventKind
	For   time.Duration // eventSilent: how long without any activity
}

type workerMemory struct {
	status        string
	activity      time.Time // latest activity seen
	silentSince   time.Time // start of the current quiet stretch
	reportedQuiet bool
}

type watcher struct {
	silence      time.Duration
	workers      map[string]*workerMemory
	pendingSince time.Time // first poll that saw the inbox not empty
	reminded     bool
}

func newWatcher(silence time.Duration) *watcher {
	return &watcher{silence: silence, workers: map[string]*workerMemory{}}
}

// observe turns one poll of the workers into the events worth a message.
// The first sight of a worker counts as a change only for blocked, so an
// acw started during a block still says so.
func (w *watcher) observe(now time.Time, views []workerView) []watchEvent {
	var events []watchEvent
	for _, v := range views {
		m, seen := w.workers[v.Label]
		if !seen {
			m = &workerMemory{activity: v.Activity, silentSince: now}
			w.workers[v.Label] = m
		}
		if v.Status == "blocked" && (!seen || m.status != "blocked") {
			events = append(events, watchEvent{Label: v.Label, Kind: eventBlocked})
		}
		// A hooked worker's turn end already reached the master through
		// its Stop hook; saying it twice would cost the master a turn.
		if seen && !v.Hooked && m.status == "working" && (v.Status == "idle" || v.Status == "done") {
			events = append(events, watchEvent{Label: v.Label, Kind: eventTurnEnd})
		}
		// Silence only counts while working: a worker waiting on the master
		// or on a prompt is quiet for a reason the other events report.
		if v.Activity.After(m.activity) || v.Status != "working" {
			m.silentSince, m.reportedQuiet = now, false
			if v.Activity.After(m.activity) {
				m.activity = v.Activity
			}
		}
		if v.Status == "working" && !m.reportedQuiet {
			quiet := now.Sub(m.silentSince)
			if since := now.Sub(m.activity); since < quiet {
				quiet = since
			}
			if quiet > w.silence {
				events = append(events, watchEvent{Label: v.Label, Kind: eventSilent, For: quiet})
				m.reportedQuiet = true
			}
		}
		m.status = v.Status
	}
	return events
}

// remindInbox reports, once per batch of unread messages, that they have
// waited longer than inboxReminderAfter.
func (w *watcher) remindInbox(now time.Time, pending bool) bool {
	if !pending {
		w.pendingSince, w.reminded = time.Time{}, false
		return false
	}
	if w.pendingSince.IsZero() {
		w.pendingSince = now
	}
	if w.reminded || now.Sub(w.pendingSince) <= inboxReminderAfter {
		return false
	}
	w.reminded = true
	return true
}
