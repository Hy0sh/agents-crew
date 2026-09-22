package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The Stop hook is what replaces the master's polling discipline, and it
// is delivered as a JSON string on a command line — the one place a
// quoting slip would silently produce a worker that starts fine and never
// reports anything.
func TestStopHookSettingsIsValidAndAddressesTheMaster(t *testing.T) {
	got, err := stopHookSettings("master-3f9a1c", "worker2")
	if err != nil {
		t.Fatalf("stopHookSettings() error = %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("settings are not valid JSON: %v\n%s", err, got)
	}

	stop, ok := parsed.Hooks["Stop"]
	if !ok || len(stop) != 1 || len(stop[0].Hooks) != 1 {
		t.Fatalf("settings do not declare exactly one Stop hook: %s", got)
	}
	cmd := stop[0].Hooks[0]
	if cmd.Type != "command" {
		t.Errorf("hook type = %q, want %q", cmd.Type, "command")
	}
	if !strings.HasPrefix(cmd.Command, "herdr agent prompt master-3f9a1c ") {
		t.Errorf("hook command = %q, should prompt the master by name", cmd.Command)
	}
	if !strings.Contains(cmd.Command, "worker2") {
		t.Errorf("hook command = %q, should name the worker it fires for", cmd.Command)
	}
}

func TestWorkerArgsOnlyHooksClaudeWorkers(t *testing.T) {
	claude := workerArgs("sonnet", "claude", "master-3f9a1c", "worker1")
	if !slices.Contains(claude, "--settings") {
		t.Errorf("workerArgs(claude) = %v, want a --settings carrying the Stop hook", claude)
	}
	if !slices.Contains(claude, "--model") || !slices.Contains(claude, "sonnet") {
		t.Errorf("workerArgs(claude) = %v, dropped the model", claude)
	}

	codex := workerArgs("gpt-5", "codex", "master-3f9a1c", "worker1")
	if slices.Contains(codex, "--settings") {
		t.Errorf("workerArgs(codex) = %v, --settings is Claude Code's own flag and would break the CLI", codex)
	}
}
