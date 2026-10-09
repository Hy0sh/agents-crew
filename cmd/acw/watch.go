package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Hy0sh/agents-crew/internal/board"
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
	InboxNext      string `json:"inbox_next,omitempty"`
	SilenceMinutes int    `json:"silence_minutes"`
	// PRWatchRepo is the GitHub "host/owner/name" whose open PRs the watcher
	// follows (see prWatcher), "" when pr-watch is off.
	PRWatchRepo string `json:"pr_watch_repo,omitempty"`
	// Stamp is the run's, also written in the status dir: a watcher whose
	// stamp is no longer there belongs to a swarm that is gone.
	Stamp string `json:"stamp"`
}

// ownsRun reports whether repo's status dir still belongs to the run
// stamped stamp, the one its pool was written for. A watcher can outlive
// its swarm: acw stop only removes the dir once it found the master, and
// a stop then a start within one poll recreates it at once, for a new
// swarm with the same worker names.
func ownsRun(repo, stamp string) bool {
	// pool.json alone: a queue.json the master left unreadable must not
	// stop the watcher for good.
	p, err := readPoolFile(repo)
	return err == nil && p.Plan.Stamp == stamp
}

// runWatch polls the workers every interval until acw stop removes the
// status directory, sends the master what observe finds worth it, and
// runs the pool (see schedule). Nothing here is fatal: a failed poll or
// delivery is logged, and the next poll tries again.
func runWatch(plan watchPlan, interval time.Duration) {
	statusDir := names.StatusDir(plan.Repo)
	slug := names.Slug(plan.Repo)
	w := newWatcher(time.Duration(plan.SilenceMinutes) * time.Minute)
	worktrees := worktreeCache{}
	screens := screenCache{}
	var prs *prWatcher
	if plan.PRWatchRepo != "" {
		prs = newPRWatcher(plan.PRWatchRepo)
	}
	poolMem := newPoolMemory()
	// tellHeld keeps the master from hearing every poll that a worker's
	// input line holds its message back.
	tellHeld := map[string]bool{}
	// Held until every opening, close and dispatch it started is over:
	// acw stop waits for it, so none of them runs during its teardown.
	locked := false
	if lock, err := os.OpenFile(names.WatchLock(plan.Repo), os.O_CREATE|os.O_RDWR, 0o644); err == nil {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err == nil {
			defer lock.Close()
			locked = true
		}
	}
	defer inflight.Wait()
	// With the lock held, no other watcher runs: whatever is half done in
	// the pool of this run was left by one that died.
	if locked && ownsRun(plan.Repo, plan.Stamp) {
		recoverPool(plan.Repo)
	}
	boardLast := map[string]board.Worker{}
	var prsWritten, beaten time.Time
	for {
		if !ownsRun(plan.Repo, plan.Stamp) {
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
			fmt.Fprintln(os.Stderr, "master not found, the watcher stops")
			return
		}
		now := time.Now()
		pool, queue, err := readPool(plan.Repo)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pool:", err)
			time.Sleep(interval)
			continue
		}
		var views []workerView
		var labels []string
		agentNames := map[string]string{}
		for _, pw := range pool.Workers {
			label, name := pw.label(), names.Worker(slug, pw.Index)
			labels = append(labels, label)
			a, ok := herdr.FindAgent(agents, name)
			if !ok {
				continue
			}
			agentNames[label] = name
			v := workerView{Label: label, Hooked: pool.Plan.Workers[pw.Index-1].Kind == "claude", Status: a.Status}
			// Only a working worker's activity counts (see observe), and
			// reading it costs a git status: skipped for the others.
			if a.Status == "working" {
				s, mtime := readWorkerStatus(filepath.Join(statusDir, label+".json"))
				v.Activity = activity(s, mtime, worktrees.get(now, label, a.Cwd))
			}
			if info, err := os.Stat(filepath.Join(statusDir, label+".turn")); err == nil {
				v.TurnEnd = info.ModTime()
			}
			views = append(views, v)
			if pw.State == workerBusy && (a.Status == "idle" || a.Status == "done") {
				blocking, err := tellIdle(statusDir, label, name)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s: master's message: %v\n", label, err)
				}
				if blocking == "" {
					delete(tellHeld, label)
				} else if !tellHeld[label] {
					tellHeld[label] = true
					msg := fmt.Sprintf("%s: your message waits, its input line is not empty: “%s”. It goes out once that line is empty (sent or cleared); if that is mine, it's my turn to finish.", label, oneLine(blocking))
					if err := deliver(plan.Inbox, plan.MasterName, msg); err != nil {
						fmt.Fprintln(os.Stderr, "message to the master:", err)
					}
				}
			}
		}
		// The board: each worker's row when it moved, and without the PR
		// watch, the PRs its status names.
		var rows []board.Worker
		var prRows []board.PR
		for _, pw := range pool.Workers {
			a, found := herdr.FindAgent(agents, names.Worker(slug, pw.Index))
			s, _ := readWorkerStatus(filepath.Join(statusDir, pw.label()+".json"))
			row := boardWorker(plan.Repo, pw, a.Status, s, now)
			if found {
				row.Busy = screens.get(now, pw.label(), a.Name)
			}
			if u, err := readUsage(filepath.Join(statusDir, pw.label()+".usage.json")); err == nil {
				row.Context, row.FiveHour = u.Context, u.FiveHour
			}
			rows = append(rows, row)
			if p, ok := prFromURL(plan.Repo, s.PRURL, pw.label(), now); ok && prs == nil {
				prRows = append(prRows, p)
			}
		}
		if now.Sub(beaten) >= beatEvery && record("heartbeat", func(b *board.DB) error {
			return b.Beat(plan.Repo, now, len(queue.Tasks), unreadLines(plan.Inbox))
		}) {
			beaten = now
		}
		moved := changedWorkers(boardLast, rows)
		gone := prunedWorkers(boardLast, labels)
		if len(moved) > 0 || len(gone) > 0 || len(prRows) > 0 && now.Sub(prsWritten) >= prWatchEvery {
			ok := record("workers", func(b *board.DB) error {
				for _, w := range moved {
					if err := b.UpsertWorker(w); err != nil {
						return err
					}
				}
				if len(gone) > 0 {
					if err := b.KeepWorkers(plan.Repo, labels); err != nil {
						return err
					}
				}
				if now.Sub(prsWritten) >= prWatchEvery {
					for _, p := range prRows {
						if err := b.UpsertPR(p); err != nil {
							return err
						}
					}
					prsWritten = now
				}
				return nil
			})
			// A failed write is retried on the next poll.
			// The pruned ones leave boardLast once the base forgot them.
			for _, w := range moved {
				if !ok {
					delete(boardLast, w.Worker)
				}
			}
			for _, g := range gone {
				if ok {
					delete(boardLast, g)
				}
			}
		}
		w.forget(labels)
		for _, e := range w.observe(now, views) {
			// Waiting on a long tool or on background work writes nothing
			// in the worktree: that silence is work, read on its screen.
			if e.Kind == eventSilent {
				if screen, err := herdr.AgentScreen(agentNames[e.Label]); err == nil && busyOnScreen(screen) != "" {
					w.rearm(e.Label, now)
					continue
				}
			}
			if err := deliver(plan.Inbox, plan.MasterName, eventMessage(statusDir, agentNames[e.Label], e)); err != nil {
				fmt.Fprintln(os.Stderr, "message to the master:", err)
			}
		}
		if prs != nil && now.Sub(prs.last) >= prWatchEvery {
			prs.last = now
			owners := prOwners(statusDir, labels)
			for _, line := range prs.poll(owners) {
				if err := deliver(plan.Inbox, plan.MasterName, line); err != nil {
					fmt.Fprintln(os.Stderr, "message to the master:", err)
				}
			}
			if merged := mergedURLs(prs.closed); len(merged) > 0 {
				if err := withPool(plan.Repo, func(p *poolState, q *taskQueue) (bool, error) {
					changed := false
					for _, url := range merged {
						changed = q.mergedPR(url) || changed
					}
					return changed, nil
				}); err != nil {
					fmt.Fprintln(os.Stderr, "merged PRs:", err)
				}
			}
			heldURLs, heldBranches := heldPRs(statusDir, pool, queue)
			held := func(pr prState) bool { return heldURLs[normalizePRURL(pr.URL)] || heldBranches[pr.Head] }
			record("prs", func(b *board.DB) error {
				for _, pr := range prs.prev {
					if err := b.UpsertPR(boardPR(plan.Repo, pr, owners[normalizePRURL(pr.URL)], "", held(pr), now)); err != nil {
						return err
					}
				}
				for _, c := range prs.closed {
					if err := b.UpsertPR(boardPR(plan.Repo, c.PR, owners[normalizePRURL(c.PR.URL)], c.Fate, held(c.PR), now)); err != nil {
						return err
					}
				}
				return nil
			})
		}
		pool.Held = heldStacks(plan.Repo, pool)
		runPool(plan.Repo, pool, queue, pollWorkers(pool, agents, statusDir, now, poolMem), now, poolMem)
		if plan.Inbox != "" {
			info, err := os.Stat(plan.Inbox)
			if w.remindInbox(now, err == nil && info.Size() > 0) {
				remind := "acw messages have been waiting in your inbox for more than 5 min: run `" + plan.InboxNext + "` again in the background."
				if err := herdr.AgentPrompt(plan.MasterName, remind); err != nil {
					fmt.Fprintln(os.Stderr, "inbox reminder:", err)
				}
			}
		}
		time.Sleep(interval)
	}
}

// beatEvery is how often the watcher tells the board it is alive: the
// page turns orange after 2 min without it.
const beatEvery = 30 * time.Second

// screenCache keeps what each worker's screen shows it waiting on
// (busyOnScreen) between reads: a herdr call per worker every poll is a
// steady cost for a line of the board.
type screenCache map[string]struct {
	at   time.Time
	busy string
}

func (c screenCache) get(now time.Time, label, agent string) string {
	if e, ok := c[label]; ok && now.Sub(e.at) < worktreeEvery {
		return e.busy
	}
	var busy string
	if screen, err := herdr.AgentScreen(agent); err == nil {
		busy = busyOnScreen(screen)
	}
	c[label] = struct {
		at   time.Time
		busy string
	}{now, busy}
	return busy
}

// unreadLines counts the messages waiting in an inbox file, one per line;
// 0 without an inbox.
func unreadLines(path string) int {
	if path == "" {
		return 0
	}
	content, _ := os.ReadFile(path)
	return bytes.Count(content, []byte("\n"))
}

// worktreeEvery is how often the watcher reruns git status on a working
// worker's worktree: silence is counted in minutes, and a status per
// worker every poll is a steady cost on a large repo.
const worktreeEvery = 30 * time.Second

// worktreeCache keeps each worker's last worktree activity between git
// status runs.
type worktreeCache map[string]struct {
	at, activity time.Time
}

func (c worktreeCache) get(now time.Time, label, worktree string) time.Time {
	if e, ok := c[label]; ok && now.Sub(e.at) < worktreeEvery {
		return e.activity
	}
	a := worktreeActivity(worktree)
	c[label] = struct{ at, activity time.Time }{now, a}
	return a
}

// eventMessage builds the text for one event. A blocked worker's pane is
// read now, while the prompt is still on it.
func eventMessage(statusDir, agentName string, e watchEvent) string {
	switch e.Kind {
	case eventBlocked, eventIdleNoTurnEnd:
		pane, err := herdr.AgentRead(agentName, 40)
		if err != nil {
			pane = "(pane unreadable: " + err.Error() + ")"
		}
		msg := blockedMessage(e.Label, countBlock(filepath.Join(statusDir, e.Label+".blocks")), pane)
		if e.Kind == eventIdleNoTurnEnd {
			msg = e.Label + " went idle without finishing its turn: it may be waiting for an approval or an answer that herdr doesn't see as a block. " + msg
		}
		return msg
	case eventSilent:
		return silentMessage(e.Label, e.For)
	default:
		return pingMessage(e.Label)
	}
}

// countBlock adds one to the worker's block count and returns it. Kept on
// disk, in the status dir, so another process can start it over.
func countBlock(path string) int {
	content, _ := os.ReadFile(path)
	n, _ := strconv.Atoi(strings.TrimSpace(string(content)))
	n++
	if err := os.WriteFile(path, []byte(strconv.Itoa(n)+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "block counter:", err)
	}
	return n
}

// deliver sends msg to the master the way it reads acw's messages: its
// inbox, or its input when it has none.
func deliver(inbox, masterName, msg string) error {
	if inbox != "" {
		return appendLine(inbox, msg)
	}
	return herdr.AgentPrompt(masterName, msg)
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
	msg := fmt.Sprintf("%s is blocked: probably waiting on a tool approval or a question. Last lines of its pane:\n%s",
		label, strings.Join(lines, "\n"))
	if count >= 2 {
		msg = fmt.Sprintf("Block #%d for this worker since its last context reset: it may be running into a prohibition. ", count) + msg
	}
	return msg
}

func silentMessage(label string, d time.Duration) string {
	return fmt.Sprintf("%s has been working for %d min with no visible activity (no turn end, no status, no file changed in its worktree). "+
		"Check its worktree or its pane.", label, int(d.Minutes()))
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
	// TurnEnd is when its Stop hook last ran (workerN.turn), zero for a
	// worker without the hook.
	TurnEnd time.Time
}

type eventKind int

const (
	eventBlocked eventKind = iota
	eventSilent
	eventTurnEnd
	// eventIdleNoTurnEnd is a hooked worker gone idle without its Stop
	// hook running: some prompts (a question, some approvals) show as
	// idle rather than blocked in herdr, and would go unreported.
	eventIdleNoTurnEnd
)

// idleGrace is how long a hooked worker may sit idle before its Stop hook
// is taken as not coming: the hook runs as the turn ends, a few seconds
// around herdr seeing idle.
const idleGrace = 15 * time.Second

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
	turnEnd       time.Time // last turn end seen while working
	idleSince     time.Time // a hooked worker went idle, its turn end not seen yet
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
			m = &workerMemory{activity: v.Activity, silentSince: now, turnEnd: v.TurnEnd}
			w.workers[v.Label] = m
		}
		idle := v.Status == "idle" || v.Status == "done"
		if v.Hooked {
			switch {
			// The turn end to beat is the one from before this working
			// stretch: under load the Stop hook can run while herdr still
			// says working, and that turn end is the one the idle answers to.
			case v.Status == "working":
				if m.status != "working" {
					m.turnEnd = v.TurnEnd
				}
				m.idleSince = time.Time{}
			case !idle || v.TurnEnd.After(m.turnEnd):
				m.idleSince = time.Time{}
			case seen && m.status == "working":
				m.idleSince = now
			case !m.idleSince.IsZero() && now.Sub(m.idleSince) > idleGrace:
				events = append(events, watchEvent{Label: v.Label, Kind: eventIdleNoTurnEnd})
				m.idleSince = time.Time{}
			}
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

// rearm starts label's quiet stretch over: its silence was explained, a
// new one is counted from now.
func (w *watcher) rearm(label string, now time.Time) {
	if m := w.workers[label]; m != nil {
		m.silentSince, m.reportedQuiet = now, false
	}
}

var (
	// backgroundCount is a count in Claude Code's footer: "1 shell", "3 monitors".
	backgroundCount = regexp.MustCompile(`\b\d+ (?:shells?|monitors?)\b`)
	// toolRunning is a tool still running in the foreground: "Running… (3s · timeout 9m 50s)".
	toolRunning = regexp.MustCompile(`Running… \([^)]*timeout`)
)

// busyOnScreen says what an agent's screen shows it waiting on, "" for
// nothing: a tool running in the foreground, or what runs in the
// background, read in the footer under the input line (the last line that
// starts with ❯), not in the conversation above it.
// ponytail: matches Claude Code's rendering as of 2026-10, like typedInput;
// another rendering reads as nothing, and the alert goes out as before.
func busyOnScreen(screen string) string {
	plain := ansiCode.ReplaceAllString(screen, "")
	lines := strings.Split(plain, "\n")
	var found []string
	if toolRunning.MatchString(plain) {
		found = append(found, "a tool running")
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimLeft(lines[i], " │"), "❯") {
			found = append(found, backgroundCount.FindAllString(strings.Join(lines[i+1:], "\n"), -1)...)
			break
		}
	}
	return strings.Join(found, ", ")
}

// forget drops what the watcher remembers of a worker no longer open: a
// worker opened again under the same label starts with a clean slate.
func (w *watcher) forget(open []string) {
	for label := range w.workers {
		if !slices.Contains(open, label) {
			delete(w.workers, label)
		}
	}
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
