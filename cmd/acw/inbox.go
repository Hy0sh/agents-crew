package main

import (
	"bytes"
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

const (
	inboxWatchUse = "__inbox-watch"
	inboxNextUse  = "__inbox-next"
)

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
// when it watches an inbox, the permission to run acw's two inbox
// commands: the Monitor one (__inbox-watch, for a custom brief that still
// arms one) and the background one (__inbox-next). Claude Code asks
// approval for a Monitor it has no rule for, with no "don't ask again",
// and a prompt at every re-run would stall the master just the same.
// Scoped to acw's own commands, not Bash in general.
func masterArgs(model, exe, inboxWatch string) []string {
	args := modelArgs(model)
	if inboxWatch == "" {
		return args
	}
	return append(args, "--allowedTools",
		"Bash("+shellWord(exe)+" "+inboxWatchUse+":*)",
		"Bash("+shellWord(exe)+" "+inboxNextUse+":*)")
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

// nextInbox waits until at least one line is in the inbox, writes out
// every line waiting there, and returns. The master runs it as a
// background command and runs it again after each return: unlike a
// Monitor, a background command has no 30-minute expiry, so a quiet
// swarm costs the master nothing, and the end of the command is what
// wakes it. A removed status dir (acw stop) ends it with nothing written.
func nextInbox(inbox string, w io.Writer, interval time.Duration) error {
	for {
		if _, err := os.Stat(filepath.Dir(inbox)); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		var got bytes.Buffer
		if err := drainOnce(inbox, &got); err != nil {
			return err
		}
		if got.Len() > 0 {
			_, err := w.Write(got.Bytes())
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
