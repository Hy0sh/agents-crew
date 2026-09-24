package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Shaped on real Claude Code transcript lines.
const transcript = `{"type":"user","origin":{"kind":"human"},"timestamp":"2026-09-24T10:00:00Z","message":{"content":"Tu es la session master..."}}
{"type":"assistant","timestamp":"2026-09-24T10:00:05Z","message":{"content":[{"type":"thinking","thinking":"hmm"},{"type":"tool_use","name":"Monitor"}]}}
{"type":"user","isMeta":true,"timestamp":"2026-09-24T10:01:00Z","message":{"content":"<local-command-caveat>..."}}
{"type":"user","timestamp":"2026-09-24T10:01:30Z","message":{"content":[{"type":"tool_result","content":"ok"}]}}
{"type":"user","timestamp":"2026-09-24T10:01:40Z","message":{"content":"worker1 a rendu la main"}}
{"type":"user","origin":{"kind":"human"},"timestamp":"2026-09-24T10:02:00Z","message":{"content":"Prends le ticket 142"}}
{"type":"assistant","timestamp":"2026-09-24T10:02:10Z","message":{"content":[{"type":"text","text":"Dispatché sur worker2."}]}}
{"type":"assistant","timestamp":"2026-09-24T10:02:20Z","message":{"content":[{"type":"text","text":"Je lis le fil du ticket."}]}}
not json
`

func TestReadConversationKeepsOnlyTheExchange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readConversation(path, firstLine("Tu es la session master...\nRôle : ..."))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Role != "user" || got[0].Text != "Prends le ticket 142" ||
		got[1].Text != "Dispatché sur worker2.\n\nJe lis le fil du ticket." {
		t.Errorf("conversation = %+v, want the user's message and the merged reply, without the brief", got)
	}
}

func TestNewSessionIDIsAUUIDv4(t *testing.T) {
	id := newSessionID()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("newSessionID() = %q", id)
	}
	if strings.EqualFold(id, newSessionID()) {
		t.Error("two session IDs are equal")
	}
}
