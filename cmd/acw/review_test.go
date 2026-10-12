package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/board"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// acw done keeps what the review needs once the worker moved on, and
// acw queue review makes the brief of it: the branch's plan, not the
// ticket's other one, the ticket's decisions, the briefs, the proof.
func TestQueueReview(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := testSwarm(t, 2,
		poolWorker{Index: 1, State: workerBusy, Task: 1, Current: &queuedTask{ID: 1, Brief: "Build the price per site for SHOP-9.\nAdjustment: keep the old column."}},
		poolWorker{Index: 2, State: workerBusy, Task: 2, Current: &queuedTask{ID: 2, Brief: "no branch here"}})
	writeJSON(names.QueueFile(repo), taskQueue{NextID: 3, Tasks: []queuedTask{{ID: 3, Brief: "later"}}})
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init"}, {"branch", "feat/price"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head, _ := exec.Command("git", "-C", repo, "rev-parse", "feat/price").Output()
	status := filepath.Join(names.StatusDir(repo), "worker1.json")
	os.WriteFile(status, []byte(`{"state":"pr_open","tache":"SHOP-9 price per site","summary":"PR open","branch":"feat/price","base_branch":"origin/feat/score","proof_path":"/proofs/price","pr_url":"https://github.com/o/r/pull/12"}`), 0o644)
	if err := finishWorker(repo, 1, "worker1", 1, io.Discard); err != nil {
		t.Fatal(err)
	}
	withBoard(func(b *board.DB) error {
		price, _ := b.Park(board.Parked{Repo: repo, Ticket: "SHOP-9", On: "me", Text: "price", Kind: "plan", DocPath: "/plans/price.md", Branch: "feat/price", CreatedAt: time.Now()})
		score, _ := b.Park(board.Parked{Repo: repo, Ticket: "SHOP-9", On: "me", Text: "score", Kind: "plan", DocPath: "/plans/score.md", Branch: "feat/score", CreatedAt: time.Now()})
		b.CloseParked(price, "accepted", time.Now())
		b.CloseParked(score, "accepted", time.Now()) // accepted last, on the ticket's other branch
		b.AddDecision(board.Decision{Repo: repo, At: time.Now(), Subject: "SHOP-9", Text: "Round to the cent", Why: "the client asked"})
		b.AddDecision(board.Decision{Repo: repo, At: time.Now(), Subject: "SHOP-90", Text: "Another ticket's"})
		return nil
	})

	for id, want := range map[int]string{2: "not over", 3: "not over", 4: "was ever queued"} {
		if err := queueReview(repo, id, reviewOptions{}, time.Now(), io.Discard); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("review of #%d = %v, want %q", id, err, want)
		}
	}
	var out strings.Builder
	if err := queueReview(repo, 1, reviewOptions{Note: "the VAT rounding"}, time.Now(), &out); err != nil {
		t.Fatal(err)
	}
	_, q, _ := readPool(repo)
	got := q.Tasks[len(q.Tasks)-1]
	if got.Branch != "feat/price" || !strings.Contains(out.String(), "task #4 queued") {
		t.Errorf("queued = %+v, out %q; want #4 on feat/price", got, out.String())
	}
	for _, want := range []string{
		"for SHOP-9, https://github.com/o/r/pull/12",
		"none of it is the scope of your review",
		strings.TrimSpace(string(head)),
		"git diff origin/feat/score...HEAD",
		"/plans/price.md",
		"Round to the cent (the client asked)",
		"Adjustment: keep the old column.",
		"/proofs/price. A proof older than the branch's last commit proves nothing",
		"on top of your method",
		"the VAT rounding",
	} {
		if !strings.Contains(got.Brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, got.Brief)
		}
	}
	for _, unwanted := range []string{"/plans/score.md", "Another ticket's", "previous review"} {
		if strings.Contains(got.Brief, unwanted) {
			t.Errorf("brief has %q:\n%s", unwanted, got.Brief)
		}
	}

	// A task ended without a branch has nothing to review.
	os.WriteFile(filepath.Join(names.StatusDir(repo), "worker2.json"), []byte(`{"state":"done","tache":"SHOP-9 notes"}`), 0o644)
	finishWorker(repo, 2, "worker2", 2, io.Discard)
	if err := queueReview(repo, 2, reviewOptions{}, time.Now(), io.Discard); err == nil || !strings.Contains(err.Error(), "without a branch") {
		t.Errorf("review of a task without a branch = %v", err)
	}
}

// A second pass after a fix: the previous review's file and the head it
// read, for an incremental diff; the reviews' own briefs are not the
// work's.
func TestReviewBriefSecondPass(t *testing.T) {
	at := time.Date(2026, 10, 12, 10, 0, 0, 0, time.Local)
	dev := board.Handled{Task: 9, Worker: "worker1", Branch: "feat/price", Base: "feat/score", Head: "aaa", Brief: "Build it", Summary: "PR open", At: at}
	review := board.Handled{Task: 10, Worker: "reviewer1", Branch: "feat/price", Head: "bbb", Kind: "need-review", Proof: "/tmp/rv1/review.md", Brief: "Review it", At: at.Add(time.Hour)}
	fix := board.Handled{Task: 12, Worker: "worker2", Branch: "feat/price", Head: "ccc", Brief: "Fix the findings", Summary: "fixed", At: at.Add(2 * time.Hour)}
	got := reviewData{Task: fix, History: []board.Handled{dev, review, fix}, Kind: "need-review"}.brief()
	for _, want := range []string{"git diff bbb..HEAD", "/tmp/rv1/review.md. Check that each of its findings", "Build it", "Fix the findings", "No accepted plan", "No proof path"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Review it") {
		t.Errorf("brief has the review's own brief:\n%s", got)
	}
}
