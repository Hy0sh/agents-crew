package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const turnEndUse = "__turn-end"

// normalizeStatus is what a worker's Stop hook runs before pinging the
// master (see turnEndCommand): it puts in the status file what the worker
// was trusted with and got wrong in practice. updated_at comes from the
// file's own mtime (a worker wrote its local time with a Z), last_turn_end
// from the clock, blocked_on is cleared once state is no longer blocked
// (one stayed set long after the answer came in), and state_since says
// since when the state holds (see stateSince).
//
// It only ever touches a JSON object: a missing file, one the worker left
// halfway, or anything else is left exactly as it is. Safe to rewrite
// here because the hook runs once the turn is over, when the worker is
// not writing.
func normalizeStatus(path string, now time.Time) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var status map[string]any
	if json.Unmarshal(content, &status) != nil || status == nil {
		return nil
	}

	written := info.ModTime()
	status["updated_at"] = written.UTC().Format(time.RFC3339)
	status["last_turn_end"] = now.UTC().Format(time.RFC3339)
	if _, ok := status["blocked_on"]; ok && !blockedState(status["state"]) {
		status["blocked_on"] = ""
	}
	state, _ := status["state"].(string)
	status["state_since"] = stateSince(strings.TrimSuffix(path, ".json")+".since", state, now)

	out, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(path, append(out, '\n'), info.Mode().Perm()); err != nil {
		return err
	}
	// Given back, so the next turn's updated_at is still the worker's own
	// last write and not this rewrite.
	return os.Chtimes(path, written, written)
}

// stateSince is the turn end at which acw first saw this state, for a
// queue of workers in the same state to be ordered from the files. What
// acw saw is kept in workerN.since and not in the status: the worker
// rewrites its file whole, and would drop it at every write.
func stateSince(path, state string, now time.Time) string {
	type seen struct{ State, Since string }
	var s seen
	if content, err := os.ReadFile(path); err == nil && json.Unmarshal(content, &s) == nil && s.State == state && s.Since != "" {
		return s.Since
	}
	s = seen{State: state, Since: now.UTC().Format(time.RFC3339)}
	if out, err := json.Marshal(s); err == nil {
		_ = writeAtomic(path, out, 0o644)
	}
	return s.Since
}

// markTurnEnd stamps workerN.turn with the turn's end, whether or not the
// worker wrote a status file yet: acw's watcher tells a finished turn
// from a worker left idle on a prompt by it (see eventIdleNoTurnEnd).
func markTurnEnd(statusDir, label string, at time.Time) error {
	path := filepath.Join(statusDir, label+".turn")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		return err
	}
	return os.Chtimes(path, at, at)
}

// turnEnd is what a claude worker's Stop hook runs (see turnEndCommand).
// A message the master left for the worker (see acw tell) goes first: it
// is printed as the hook's block decision, Claude Code then hands it to
// the worker as its next input instead of ending the turn, and nothing
// was typed into a pane the user or a popup may hold. Otherwise the turn
// did end: acw stamps it, fixes the status file, and pings the master
// when there is something to say (see statusDelta).
func turnEnd(statusDir, label, inbox, masterName string, now time.Time, out io.Writer) error {
	var held bytes.Buffer
	if err := drainOnce(filepath.Join(statusDir, label+".tell"), &held); err != nil {
		fmt.Fprintln(os.Stderr, "master's message:", err)
	}
	if held.Len() > 0 {
		markTold(statusDir, label)
		return json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": masterMessage(held.String())})
	}
	if err := markTurnEnd(statusDir, label, now); err != nil {
		fmt.Fprintln(os.Stderr, "turn end mark:", err)
	}
	err := normalizeStatus(filepath.Join(statusDir, label+".json"), now)
	if delta, ping := statusDelta(statusDir, label, now); ping {
		if derr := deliver(inbox, masterName, label+pingHead+delta+pingTail); err == nil {
			err = derr
		}
	}
	return err
}

// masterMessage is how a message from the master reaches a worker; its
// system prompt tells it what the prefix means (see workerRole).
func masterMessage(text string) string {
	return "Message from the master:\n" + strings.TrimSpace(text)
}

// markTold records that the master spoke to the worker since its last
// ping (a message, a new brief): the end of the turn that follows is its
// answer, and goes to the master even with nothing moved in the status.
// It also clears a <what>_ready state (see clearReady).
func markTold(statusDir, label string) {
	if err := os.WriteFile(filepath.Join(statusDir, label+".told"), nil, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "told mark:", err)
	}
	if err := clearReady(filepath.Join(statusDir, label+".json")); err != nil {
		fmt.Fprintln(os.Stderr, "ready state:", err)
	}
}

// clearReady turns a <what>_ready state into working: the master's message
// is its answer, and workers left plan_ready set while coding the go, on
// the user's page as still to approve. A worker that still waits writes
// it again (its brief says so), and gets a new state_since. Safe to write
// here: markTold runs when the worker is idle or at the end of its turn.
// The mtime is given back, as normalizeStatus does.
func clearReady(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var status map[string]any
	if json.Unmarshal(content, &status) != nil || status == nil {
		return nil
	}
	if state, _ := status["state"].(string); !strings.HasSuffix(state, "_ready") {
		return nil
	}
	status["state"] = "working"
	out, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(path, append(out, '\n'), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(path, info.ModTime(), info.ModTime())
}

// unchangedEvery is how long the end of turns with nothing moved stay
// quiet: past it, one ping says so, for a worker looping or stuck.
const unchangedEvery = 15 * time.Minute

// statusDelta says what moved in a worker's status file since its last
// ping, for the ping to carry, and whether to ping at all. Most pings
// were for nothing new, and each cost the master a wake-up: a turn end
// with nothing moved stays quiet, unless the master spoke to the worker
// since (see markTold) or nothing moved for unchangedEvery. It keeps what
// it saw in workerN.ping for the next turn. With no status to read, the
// ping goes out and says nothing about it.
func statusDelta(statusDir, label string, at time.Time) (delta string, ping bool) {
	toldPath := filepath.Join(statusDir, label+".told")
	told := os.Remove(toldPath) == nil
	content, err := os.ReadFile(filepath.Join(statusDir, label+".json"))
	var status map[string]any
	if err != nil || json.Unmarshal(content, &status) != nil || status == nil {
		return "", true
	}
	type seen struct {
		State, UpdatedAt string
		// Pinged is when a ping last went out.
		Pinged time.Time
	}
	now := seen{State: oneLine(status["state"]), UpdatedAt: oneLine(status["updated_at"]), Pinged: at}

	seenPath := filepath.Join(statusDir, label+".ping")
	var before *seen
	if content, err := os.ReadFile(seenPath); err == nil {
		var s seen
		if json.Unmarshal(content, &s) == nil {
			before = &s
		}
	}
	defer func() {
		if !ping {
			now.Pinged = before.Pinged
		}
		if out, err := json.Marshal(now); err == nil {
			_ = writeAtomic(seenPath, out, 0o644)
		}
	}()

	switch {
	case before == nil:
		return " (state: “" + now.State + "”)", true
	case before.State != now.State:
		return " (state: “" + before.State + "” → “" + now.State + "”)", true
	case before.UpdatedAt != now.UpdatedAt:
		return " (state unchanged, status rewritten: “" + now.State + "”)", true
	case told:
		return " (status unchanged since its previous ping)", true
	case at.Sub(before.Pinged) >= unchangedEvery:
		return fmt.Sprintf(" (status unchanged for %d min although it keeps ending turns: check it is not looping or stuck)", int(at.Sub(before.Pinged).Minutes())), true
	}
	return "", false
}

// oneLine is a status field as the ping can carry it: the inbox is read
// line by line, and state is free text of any length.
func oneLine(v any) string {
	s, _ := v.(string)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

// blockedState reports whether a worker's state says it is blocked, in
// whatever words it chose: state is free text, often French. Erring this
// way keeps a stale blocked_on at worst; erring the other way would wipe
// the very question the master was pinged to read.
func blockedState(state any) bool {
	s, _ := state.(string)
	s = strings.ToLower(s)
	return strings.Contains(s, "block") || strings.Contains(s, "bloq")
}
