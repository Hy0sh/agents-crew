package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/board"
)

func boardDay(t *testing.T, repo string, day time.Time) board.Day {
	t.Helper()
	b, err := board.Open(board.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	d, err := b.Board(repo, day, day)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Text as arguments or on stdin, like acw tell; an empty one is refused.
func TestAddDecision(t *testing.T) {
	repo, now := t.TempDir(), time.Now()
	if err := addDecision(repo, "worker2", "SHOP-151", "accounting reconciles per line", []string{"VAT", "per", "line"}, strings.NewReader(""), now); err != nil {
		t.Fatal(err)
	}
	if err := addDecision(repo, "", "", "", nil, strings.NewReader("#418 stays stacked\n"), now); err != nil {
		t.Fatal(err)
	}
	if err := addDecision(repo, "", "", "", nil, strings.NewReader("  \n"), now); err == nil {
		t.Error("an empty decision should be refused")
	}
	d := boardDay(t, repo, now)
	if len(d.Decisions) != 2 || d.Decisions[1].Text != "VAT per line" || d.Decisions[1].Worker != "worker2" || d.Decisions[0].Text != "#418 stays stacked" {
		t.Errorf("decisions = %+v", d.Decisions)
	}
}

func TestDecisionWorkerIsChecked(t *testing.T) {
	cmd := boardCommand()
	cmd.SetArgs([]string{"decision", "--repo", t.TempDir(), "--worker", "bob", "text"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not a worker") {
		t.Errorf("--worker bob = %v, want a refusal", err)
	}
}
