package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hy0sh/agents-crew/internal/names"
)

// testSwarm is a repo with a pool of n workers and an empty queue.
func testSwarm(t *testing.T, n int, open ...poolWorker) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(names.PoolFile(repo), testPool(n, n, open...)); err != nil {
		t.Fatal(err)
	}
	return repo
}

func briefFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func queueIDs(t *testing.T, repo string) []int {
	t.Helper()
	_, q, err := readPool(repo)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, task := range q.Tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func TestQueueAddMoveRemove(t *testing.T) {
	repo := testSwarm(t, 2)
	for _, text := range []string{"one", "two", "three"} {
		if err := queueAdd(repo, briefFile(t, text), branchRequest{}, "", false, t0, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if err := queueAdd(repo, briefFile(t, "urgent"), branchRequest{Branch: "fix/x"}, "worker2", true, t0, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := queueIDs(t, repo); !slices.Equal(got, []int{4, 1, 2, 3}) {
		t.Fatalf("after add --top: %v, want [4 1 2 3]", got)
	}
	_, q, _ := readPool(repo)
	if q.Tasks[0].Brief != "urgent" || q.Tasks[0].Worker != 2 || q.Tasks[0].Branch != "fix/x" {
		t.Errorf("queued task = %+v, want the brief copied in, worker2, fix/x", q.Tasks[0])
	}

	if err := queueMove(repo, 3, 1, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := queueRemove(repo, 1, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := queueIDs(t, repo); !slices.Equal(got, []int{3, 4, 2}) {
		t.Errorf("after move and remove: %v, want [3 4 2]", got)
	}
	if err := queueRemove(repo, 42, io.Discard); err == nil {
		t.Error("removing a task that is not there should fail")
	}
	if err := queueAdd(repo, briefFile(t, "x"), branchRequest{}, "worker3", false, t0, io.Discard); err == nil {
		t.Error("a task for a worker beyond the count should be refused")
	}
}

// A branch or a base starting with a dash would reach git as an option.
func TestQueueAddRefusesOptionLikeBranches(t *testing.T) {
	repo := testSwarm(t, 1)
	for _, br := range []branchRequest{{Branch: "--orphan=x"}, {Branch: "fix/x", Base: "-d"}} {
		if err := queueAdd(repo, briefFile(t, "x"), br, "", false, t0, io.Discard); err == nil {
			t.Errorf("queueAdd(%+v) = nil, want a refusal", br)
		}
	}
	if got := queueIDs(t, repo); len(got) != 0 {
		t.Errorf("queue = %v, nothing should have been queued", got)
	}
}

// Moving a held task is the master letting it go again.
func TestQueueMoveClearsTheHold(t *testing.T) {
	repo := testSwarm(t, 1)
	if err := writeJSON(names.QueueFile(repo), taskQueue{NextID: 1, Tasks: []queuedTask{{ID: 1, Error: "branch held"}}}); err != nil {
		t.Fatal(err)
	}
	if err := queueMove(repo, 1, 1, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, q, _ := readPool(repo); q.Tasks[0].Error != "" {
		t.Errorf("held task still held after a move: %+v", q.Tasks[0])
	}
}

// The master's acw queue and the watcher change the same files: under the
// lock, no write is lost.
func TestQueueConcurrentAddsAllLand(t *testing.T) {
	repo := testSwarm(t, 1)
	path := briefFile(t, "task")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := queueAdd(repo, path, branchRequest{}, "", false, t0, io.Discard); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got := queueIDs(t, repo)
	slices.Sort(got)
	if len(got) != 20 || got[0] != 1 || got[19] != 20 {
		t.Errorf("queue after 20 concurrent adds = %v, want ids 1 to 20", got)
	}
}

func TestMarkDone(t *testing.T) {
	repo := testSwarm(t, 2, poolWorker{Index: 1, State: workerBusy, Task: 3})
	msg, err := markDone(repo, 1, t0)
	if err != nil || !strings.Contains(msg, "free") {
		t.Fatalf("markDone() = %q, %v", msg, err)
	}
	p, _, _ := readPool(repo)
	if w := p.worker(1); w.State != workerFree || w.Task != 0 || !w.Since.Equal(t0) {
		t.Errorf("worker1 = %+v, want free since now, no task", w)
	}
	if msg, err := markDone(repo, 1, t0); err != nil || !strings.Contains(msg, "already free") {
		t.Errorf("markDone() twice = %q, %v; want no error, already free", msg, err)
	}
	if _, err := markDone(repo, 2, t0); err == nil {
		t.Error("markDone() on a worker that is not open should fail")
	}
}

func TestPoolCommandsOutsideASwarm(t *testing.T) {
	if err := queueRemove(t.TempDir(), 1, io.Discard); err == nil || !strings.Contains(err.Error(), "no acw swarm") {
		t.Errorf("queueRemove() outside a swarm = %v, want no acw swarm", err)
	}
}

func TestRenderQueue(t *testing.T) {
	p := testPool(3, 3, poolWorker{Index: 1, State: workerBusy, Task: 2, Since: t0.Add(-5 * time.Minute)})
	q := tasks(queuedTask{ID: 3, Brief: "Fix the export\nmore", Worker: 1, AddedAt: t0, Error: "branch held"})
	got := renderQueue(t0, p, q)
	for _, want := range []string{"1 open of 3", "worker1  busy #2 since 5 min ago", "1. #3  Fix the export · for worker1", "held: branch held"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderQueue() missing %q in:\n%s", want, got)
		}
	}
}
