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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Hy0sh/agents-crew/internal/claudecode"
	"github.com/Hy0sh/agents-crew/internal/harness"
)

// The inbox is how workers reach a claude master without typing into its
// input. `herdr agent prompt` types the text into the master's input box,
// so a ping landing while the user was typing to the master merged with
// their half-written message. Instead, the Stop hook appends a line to
// the inbox and the master watches it with a Claude Code Monitor, whose
// events start a turn without touching the input.

const (
	inboxWatchUse  = "__inbox-watch"
	inboxNextUse   = "__inbox-next"
	masterWriteUse = "__master-write"
)

// masterKickoff is the first prompt of a claude master, whose brief is
// in its system prompt.
const masterKickoff = "Your brief is in your system prompt: start as it says."

// drainSettle is how long drainOnce waits after taking the inbox, so an
// append that opened the file just before the rename still lands in what
// gets read.
var drainSettle = 100 * time.Millisecond

// inboxBatch is how long nextInbox waits after a first message for the
// ones right behind it.
var inboxBatch = 2 * time.Second

// inboxWatchCommand is the command the master arms its Monitor on, or ""
// when pings must keep being typed into it: a master other than claude
// has no Monitor, and a custom brief that doesn't mention {{.InboxWatch}}
// never tells the master to watch — pings would pile up in a file nobody
// reads. warn reports that last case, which the user should hear about.
func inboxWatchCommand(masterKind, customBrief, exe, inbox string) (cmd string, warn bool) {
	if !harness.Of(masterKind).InboxMonitor {
		return "", false
	}
	if customBrief != "" && !strings.Contains(customBrief, ".InboxWatch") && !strings.Contains(customBrief, ".InboxNext") {
		return "", true
	}
	return shellWord(exe) + " " + inboxWatchUse + " " + shellWord(inbox), false
}

// inboxNextCommand is what the master runs in the background to get its
// next messages (see nextInbox).
func inboxNextCommand(exe, inbox string) string {
	return shellWord(exe) + " " + inboxNextUse + " " + shellWord(inbox)
}

// masterArgs is what gets forwarded to the master's CLI: its model and,
// for a claude master, the permission to run the acw commands it is
// handed: status, queue, done and tell, its view of the swarm, its hold
// on the queue, the end of a task and its word to a worker (not dispatch
// nor clear: acw does both when it hands a task out); and, when it
// watches an inbox, the two inbox
// commands: the Monitor one (__inbox-watch, for a custom brief that still
// arms one) and the background one (__inbox-next). Claude Code asks
// approval for a Monitor it has no rule for, with no "don't ask again",
// and a prompt at every re-run would stall the master just the same.
// Scoped to acw's own commands, not Bash in general; --allowedTools is
// Claude Code's own flag, so no other kind gets it.
// A claude master also compacts at autocompact tokens, unless 0: it lives
// all day, and every turn re-reads its whole context (left at the model's
// 1M window, a day-long master ended past 800K). Its brief, in statusDir,
// goes in as a system prompt, which a compaction does not summarize away.
// It is denied the file tools but Write, which a hook keeps to its briefs
// folder: it dispatches, it does not code (see claudecode.BriefsDir).
func masterArgs(kind, model, exe, inboxWatch, statusDir string, autocompact int) []string {
	args := modelArgs(model)
	a := harness.Of(kind)
	if a.Settings && autocompact > 0 {
		args = append(args, "--autocompact", strconv.Itoa(autocompact))
	}
	if a.SystemPrompt {
		args = append(args, "--append-system-prompt-file", claudecode.BriefFile(statusDir))
	}
	if !a.Settings {
		return args
	}
	briefs := claudecode.BriefsDir(statusDir)
	args = append(args, "--disallowedTools", "Edit", "NotebookEdit", "EnterPlanMode", "ExitPlanMode")
	args = append(args, "--settings", masterSettings(exe, briefs))
	return append(append(args, "--allowedTools"), append(masterAllow(exe, inboxWatch), claudecode.WriteRule(briefs))...)
}

// masterSettings is the --settings JSON of a claude master: the hook that
// refuses its Write outside briefs, and the focus view. A Write showed the
// first ten lines of every brief, a Bash command its whole output: in
// focus, a turn's tool calls are one summary line before its answer, the
// detail one /focus or ctrl+o away. Focus needs the fullscreen renderer.
func masterSettings(exe, briefs string) string {
	out, _ := json.Marshal(map[string]any{
		"viewMode": "focus",
		"tui":      "fullscreen",
		"hooks": map[string]any{"PreToolUse": []any{map[string]any{
			"matcher": "Write",
			"hooks":   []any{map[string]string{"type": "command", "command": shellWord(exe) + " " + masterWriteUse + " " + shellWord(briefs)}},
		}}},
	})
	return string(out)
}

// masterAllow are the permission rules for the acw commands the master
// runs (see masterArgs), the same on its command line or in the settings
// of a master in Claude Desktop.
func masterAllow(exe, inboxWatch string) []string {
	var rules []string
	if inboxWatch != "" {
		rules = append(rules,
			"Bash("+shellWord(exe)+" "+inboxWatchUse+":*)",
			"Bash("+shellWord(exe)+" "+inboxNextUse+":*)")
	}
	for _, sub := range []string{"status", "queue", "done", "close", "tell", "board decision", "board park", "board parked", "board resume", "board edit"} {
		rules = append(rules, "Bash("+shellWord(exe)+" "+sub+":*)")
	}
	return rules
}

// watchInbox prints every line appended to inbox, forever, until the
// status directory holding it is removed (acw stop). It drains rather
// than tails: a Monitor expires after 30 minutes at most, and a line
// written before the master re-arms it must come out then, not be lost
// (tail -n 0) nor replay the whole history with it (tail -n +1).
func watchInbox(inbox string, w io.Writer, interval time.Duration) error {
	for {
		if _, err := os.Stat(filepath.Dir(inbox)); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		var got bytes.Buffer
		if err := drainOnce(inbox, &got); err != nil {
			return err
		}
		if err := withLater(inbox, w, got.Bytes()); err != nil {
			return err
		}
		time.Sleep(interval)
	}
}

// laterFile holds the lines that wait for the next one that wakes the
// master (see note).
func laterFile(inbox string) string { return inbox + ".later" }

// withLater writes out woke, the lines that wake the master, after those
// waiting in the later file; nothing at all without one that wakes it.
func withLater(inbox string, w io.Writer, woke []byte) error {
	if len(woke) == 0 {
		return nil
	}
	if err := drainOnce(laterFile(inbox), w); err != nil {
		return err
	}
	_, err := w.Write(woke)
	return err
}

// inboxQuiet is how long nextInbox waits before giving up: Claude Code
// kills a background command at its timeout, 30 minutes by default, and a
// master whose command was killed had to notice and re-run it by hand.
const inboxQuiet = 25 * time.Minute

const inboxStopped = "The swarm was stopped (acw stop): do NOT run this command again, nothing will come. Tell the user the swarm is over and stop there."

// nextInbox waits until at least one line is in the inbox, writes out
// every line waiting there, and returns. The master runs it as a
// background command and runs it again after each return: the end of the
// command is what wakes it. After quiet with no line it returns anyway,
// saying so, before the background timeout kills it. A removed status dir
// (acw stop) ends it saying so: ended with nothing written, it woke a
// master in Claude Desktop, which acw stop leaves open, that ran it again
// at once, turn after turn.
func nextInbox(inbox string, w io.Writer, interval, quiet time.Duration) error {
	for deadline := time.Now().Add(quiet); ; {
		if time.Now().After(deadline) {
			_, err := fmt.Fprintf(w, "nothing new for %d min: run the command again.\n", int(quiet.Minutes()))
			return err
		}
		if _, err := os.Stat(filepath.Dir(inbox)); errors.Is(err, fs.ErrNotExist) {
			_, err := fmt.Fprintln(w, inboxStopped)
			return err
		}
		var got bytes.Buffer
		if err := drainOnce(inbox, &got); err != nil {
			return err
		}
		if got.Len() > 0 {
			// Messages come in bursts (a turn end, the watcher's word on
			// it, a PR line): one wake-up for the burst, not one each.
			time.Sleep(inboxBatch)
			if err := drainOnce(inbox, &got); err != nil {
				return err
			}
			return withLater(inbox, w, got.Bytes())
		}
		time.Sleep(interval)
	}
}

// drainOnce writes out the lines waiting in inbox and consumes them. The
// rename is what makes a line come out exactly once: the next hook
// recreates the inbox with its >>, and nothing reads the taken file twice.
func drainOnce(inbox string, w io.Writer) error {
	taken := inbox + ".draining"
	if err := os.Rename(inbox, taken); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	time.Sleep(drainSettle)
	content, err := os.ReadFile(taken)
	if err != nil {
		return err
	}
	if _, err := w.Write(content); err != nil {
		return err
	}
	return os.Remove(taken)
}

// appendLine adds one line to the inbox, as the Stop hook does from the
// shell.
func appendLine(inbox, line string) error {
	f, err := os.OpenFile(inbox, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

// shellWord makes s one literal shell word, whatever it holds: a repo
// path with a space, a quote or a $ must not break a command. Left bare
// when it needs no quoting, because Claude Code matches the master's
// permission rule against the command as written, and a quoted path did
// not match in practice.
// ponytail: a repo path that does need quoting may still hit the approval
// prompt at each re-arm; test the rule's matching on quotes if one does.
func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
