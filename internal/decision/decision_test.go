package decision

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

func sample() Decision {
	return Decision{
		Task:     "ticket 142",
		Question: "Le CSV inclut-il les lignes archivées ?",
		Options:  []Option{{"Non", "export identique à l'écran"}, {"Oui", "+1 colonne"}},
		Reco:     1,
	}
}

func TestAddGivesSequentialIDsThatSurviveAReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p", "decisions.json")
	for _, want := range []string{"D1", "D2"} {
		d, err := Add(path, sample(), now)
		if err != nil {
			t.Fatal(err)
		}
		if d.ID != want || d.Status != Open {
			t.Errorf("Add() = %s %s, want %s open", d.ID, d.Status, want)
		}
	}
	got, err := Get(path, "D2")
	if err != nil || got.Question != sample().Question {
		t.Errorf("Get(D2) = %+v, %v", got, err)
	}
}

func TestAddRefusesAnOpenQuestion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.json")
	one := sample()
	one.Options = one.Options[:1]
	if _, err := Add(path, one, now); err == nil {
		t.Error("Add() with one option should be refused")
	}
	bad := sample()
	bad.Reco = 2
	if _, err := Add(path, bad, now); err == nil {
		t.Error("Add() with a reco outside the options should be refused")
	}
}

func TestCloseKeepsTheFirstAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.json")
	d, _ := Add(path, sample(), now)
	if _, err := Close(path, d.ID, "option 2", "ui", now); err != nil {
		t.Fatal(err)
	}
	got, err := Close(path, d.ID, "non finalement", "terminal", now)
	if !errors.Is(err, ErrClosed) || got.Answer != "option 2" || got.AnsweredVia != "ui" {
		t.Errorf("second Close() = %+v, %v; want ErrClosed and the page's answer", got, err)
	}
}

func TestAbandonOpenLeavesAnsweredOnesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.json")
	a, _ := Add(path, sample(), now)
	Add(path, sample(), now)
	Close(path, a.ID, "oui", "ui", now)

	n, err := AbandonOpen(path, now)
	if err != nil || n != 1 {
		t.Fatalf("AbandonOpen() = %d, %v; want 1", n, err)
	}
	ds, _ := List(path)
	if ds[0].Status != Answered || ds[1].Status != Abandoned {
		t.Errorf("statuses = %s, %s; want answered, abandoned", ds[0].Status, ds[1].Status)
	}
	if d, _ := Add(path, sample(), now); d.ID != "D3" {
		t.Errorf("next ID after abandon = %s, IDs are never reused", d.ID)
	}
}
