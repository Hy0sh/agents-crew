package layout

import (
	"math"
	"testing"
)

func TestWorkerSplitsThreeWorkers(t *testing.T) {
	// Matches the ratios verified against a live Herdr workspace on 2026-09-21:
	// master/worker column split at 0.6, then two down-splits at 1/3 and 1/2
	// produce three equal-height worker panes.
	got := WorkerSplits(3)
	want := []Split{
		{Direction: "right", Ratio: 0.6},
		{Direction: "down", Ratio: 1.0 / 3.0},
		{Direction: "down", Ratio: 0.5},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d splits, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Direction != want[i].Direction {
			t.Errorf("split %d: direction = %q, want %q", i, got[i].Direction, want[i].Direction)
		}
		if math.Abs(got[i].Ratio-want[i].Ratio) > 1e-9 {
			t.Errorf("split %d: ratio = %v, want %v", i, got[i].Ratio, want[i].Ratio)
		}
	}
}

func TestWorkerSplitsOneWorker(t *testing.T) {
	got := WorkerSplits(1)
	if len(got) != 1 || got[0].Direction != "right" || got[0].Ratio != 0.6 {
		t.Fatalf("WorkerSplits(1) = %+v, want single right/0.6 split", got)
	}
}

func TestWorkerSplitsFiveWorkers(t *testing.T) {
	got := WorkerSplits(5)
	if len(got) != 5 {
		t.Fatalf("got %d splits, want 5", len(got))
	}
	wantRatios := []float64{0.6, 1.0 / 5.0, 1.0 / 4.0, 1.0 / 3.0, 1.0 / 2.0}
	for i, want := range wantRatios {
		if math.Abs(got[i].Ratio-want) > 1e-9 {
			t.Errorf("split %d: ratio = %v, want %v", i, got[i].Ratio, want)
		}
	}
}

func TestWorkerSplitsZero(t *testing.T) {
	if got := WorkerSplits(0); got != nil {
		t.Fatalf("WorkerSplits(0) = %+v, want nil", got)
	}
}
