package main

import "time"

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
