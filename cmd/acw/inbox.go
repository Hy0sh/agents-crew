package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The inbox is how workers reach a claude master without typing into its
// input. `herdr agent prompt` types the text into the master's input box,
// so a ping landing while the user was typing to the master merged with
// their half-written message. Instead, the Stop hook appends a line to
// the inbox and the master watches it with a Claude Code Monitor, whose
// events start a turn without touching the input.

const inboxWatchUse = "__inbox-watch"

// drainSettle is how long drainOnce waits after taking the inbox, so an
// append that opened the file just before the rename still lands in what
// gets read.
var drainSettle = 100 * time.Millisecond

// inboxWatchCommand is the command the master arms its Monitor on, or ""
// when pings must keep being typed into it: a master other than claude
// has no Monitor, and a custom brief that doesn't mention {{.InboxWatch}}
// never tells the master to watch — pings would pile up in a file nobody
// reads. warn reports that last case, which the user should hear about.
func inboxWatchCommand(masterKind, customBrief, exe, inbox string) (cmd string, warn bool) {
	if masterKind != "claude" {
		return "", false
	}
	if customBrief != "" && !strings.Contains(customBrief, ".InboxWatch") {
		return "", true
	}
	return shellWord(exe) + " " + inboxWatchUse + " " + shellWord(inbox), false
}

// masterArgs is what gets forwarded to the master's CLI: its model and,
// when it watches an inbox, the permission to arm that one Monitor.
// Claude Code asks approval for a Monitor it has no rule for, with no
// "don't ask again", so without it the master would stall on a prompt at
// every re-arm. Scoped to acw's own watch command, not Monitor in
// general, which would run any command unasked. Same for the decision
// queue: a master asking approval to file a question for the user would
// be asking the question anyway. sessionID, "" for none, fixes the
// session so the page finds its transcript (see readConversation).
func masterArgs(model, exe, inboxWatch, decisionCmd, sessionID string) []string {
	args := modelArgs(model)
	if sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	if inboxWatch == "" {
		return args
	}
	args = append(args, "--allowedTools", "Bash("+shellWord(exe)+" "+inboxWatchUse+":*)")
	if decisionCmd != "" {
		args = append(args, "Bash("+shellWord(exe)+" "+decisionUse+":*)")
	}
	return args
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
		if err := drainOnce(inbox, w); err != nil {
			return err
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
