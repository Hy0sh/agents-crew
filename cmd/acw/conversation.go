package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The page shows the conversation with a claude master from its Claude
// Code transcript, not from `herdr agent read`, which captures the TUI
// (truncated lines, spinners). acw fixes the master's session ID at
// launch (--session-id) so the transcript is found without guessing
// among the other sessions started in the same folder.
// ponytail: a /clear typed to the master starts a new session, and the
// page then stops following; look up the newest session in the master's
// folder if that happens in practice.

type chatMessage struct {
	Role string    `json:"role"` // "user" or "assistant"
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// newSessionID is a random UUID v4, the form --session-id requires.
func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// transcriptPath finds session id's transcript under Claude Code's
// projects dir, whatever folder the master was started in.
func transcriptPath(id string) string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "projects", "*", id+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// readConversation keeps what the user typed and the master's text
// replies: tool calls, thinking, the dispatch's own messages (pings,
// Monitor events) and Claude Code's meta entries are left out, and so is
// acw's brief, recognized by briefHead, its first line. Consecutive text
// blocks of one reply are merged.
func readConversation(path, briefHead string) ([]chatMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []chatMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 16<<20)
	for scanner.Scan() {
		var e struct {
			Type      string    `json:"type"`
			IsMeta    bool      `json:"isMeta"`
			Timestamp time.Time `json:"timestamp"`
			Origin    struct {
				Kind string `json:"kind"`
			} `json:"origin"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.IsMeta {
			continue
		}
		var role string
		switch {
		case e.Type == "user" && e.Origin.Kind == "human":
			role = "user"
		case e.Type == "assistant":
			role = "assistant"
		default:
			continue
		}
		text := contentText(e.Message.Content)
		if text == "" {
			continue
		}
		if role == "user" && briefHead != "" && strings.HasPrefix(text, briefHead) {
			continue
		}
		if n := len(out); n > 0 && role == "assistant" && out[n-1].Role == "assistant" {
			out[n-1].Text += "\n\n" + text
			continue
		}
		out = append(out, chatMessage{Role: role, At: e.Timestamp, Text: text})
	}
	return out, scanner.Err()
}

// firstLine is what readConversation recognizes the brief by.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// contentText is a message's text: the string itself, or its text blocks.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}
