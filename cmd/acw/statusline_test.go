package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Trimmed from what Claude Code actually sent during the spike.
const spikeStatusJSON = `{"session_id":"b9d48445","context_window":{"context_window_size":200000,"used_percentage":23,"remaining_percentage":77},"rate_limits":{"five_hour":{"used_percentage":8,"resets_at":1790373600},"seven_day":{"used_percentage":62,"resets_at":1790672400}}}`

func TestRecordUsageKeepsTheJSONAndShowsContextAndQuota(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker1.usage.json")
	var out bytes.Buffer
	if err := recordUsage(path, strings.NewReader(spikeStatusJSON), &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != spikeStatusJSON {
		t.Errorf("usage file = %q, want the JSON exactly as received", got)
	}
	if out.String() != "ctx 23% · 5h 8%\n" {
		t.Errorf("status line = %q", out.String())
	}

	u, err := readUsage(path)
	if err != nil || u.SessionID != "b9d48445" || u.Context == nil || *u.Context != 23 || u.FiveHour == nil || *u.FiveHour != 8 {
		t.Errorf("readUsage() = %+v, %v", u, err)
	}
}

// Right after /clear the context is null, and a user without a
// subscription has no rate_limits at all.
func TestRecordUsageShowsUnknownValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker1.usage.json")
	var out bytes.Buffer
	in := `{"session_id":"a6025ebc","context_window":{"used_percentage":null}}`
	if err := recordUsage(path, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ctx ?% · 5h ?%\n" {
		t.Errorf("status line = %q, want ? for unknown values", out.String())
	}
	if u, _ := readUsage(path); u.SessionID != "a6025ebc" || u.Context != nil {
		t.Errorf("readUsage() = %+v, want the new session and no context", u)
	}
}
