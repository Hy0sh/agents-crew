package journal

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRecordOnlyWhenTaskOrStateChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	now := time.Now()
	steps := []string{
		`{"tache":"ticket 131","state":"working","summary":"repro"}`,
		`{"tache":"ticket 131","state":"working","summary":"fix en cours"}`, // same task and state
		`{"tache":"ticket 131","state":"pr_open","pr_url":"https://x/pr/41"}`,
	}
	for _, s := range steps {
		if err := Record(dir, "worker1", []byte(s), now); err != nil {
			t.Fatal(err)
		}
	}
	// Another worker's line in between must not hide worker1's last one.
	Record(dir, "worker2", []byte(`{"tache":"ticket 140","state":"working"}`), now)
	Record(dir, "worker1", []byte(`{"tache":"ticket 131","state":"pr_open"}`), now)

	got, err := Read(dir, Day(now))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].State != "pr_open" || got[1].PRURL != "https://x/pr/41" || got[2].Worker != "worker2" {
		t.Errorf("journal = %+v, want working, pr_open (worker1) then worker2", got)
	}
	if days, _ := Days(dir); len(days) != 1 || days[0] != Day(now) {
		t.Errorf("Days() = %v", days)
	}
}

func TestRecordToleratesOddFields(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := Record(dir, "worker1", []byte(`{"tache":142,"state":"working"}`), now); err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(dir, Day(now)); len(got) != 1 || got[0].Tache != "142" {
		t.Errorf("journal = %+v, a number where a string was expected must still be recorded", got)
	}
	if err := Record(dir, "worker2", []byte(`{}`), now); err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(dir, Day(now)); len(got) != 1 {
		t.Errorf("an empty status must write nothing, got %+v", got)
	}
}
