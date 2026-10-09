package board

import (
	"slices"
	"testing"
	"time"
)

func TestKeyIn(t *testing.T) {
	for _, c := range []struct {
		texts []string
		want  string
	}{
		{[]string{"feat/SHOP-142-discounts", "title SHOP-9"}, "SHOP-142"},
		{[]string{"perf/lists", "feat: export as CSV #SHOP-12"}, "SHOP-12"},
		{[]string{"", "fix: deliver OPS-95, OPS-96 to staging"}, "OPS-95"},
		{[]string{"perf/lists", "perf: paginate the list"}, ""},
	} {
		if got := keyIn(c.texts...); got != c.want {
			t.Errorf("keyIn(%q) = %q, want %q", c.texts, got, c.want)
		}
	}
	// A stacked PR without a key of its own takes its base's.
	if got := prKey(PR{Head: "refactor/csv", Title: "refactor: align CSV", Base: "feat/SHOP-12-export"}); got != "SHOP-12" {
		t.Errorf("stacked = %q", got)
	}
}

func viewDay() Day {
	return Day{Live: true, Marks: map[string]time.Time{},
		Workers: []Worker{
			{Worker: "worker1", State: "plan_ready", Branch: "feat/SHOP-7-reissue", Subject: "Bound the reads", Since: noon.Add(-time.Hour)},
			{Worker: "worker2", State: "review_ready", Branch: "perf/lists", Subject: "Review PR #20", PRURL: "u/20", Since: noon.Add(-5 * time.Minute)},
			{Worker: "worker3", State: "free"},
		},
		PRs: []PR{
			{Number: 10, URL: "u/10", Title: "feat: reissue SHOP-7", Head: "feat/SHOP-7-reissue", Base: "main", Status: "open", CI: "green", Review: "DISMISSED by a"},
			{Number: 11, URL: "u/11", Title: "feat: tell the family", Head: "feat/SHOP-7-tell", Base: "feat/SHOP-7-reissue", Status: "open", CI: "green", Review: "APPROVED by a", SinceReady: noon.Add(-10 * time.Minute)},
			{Number: 12, URL: "u/12", Title: "feat: print the town text #SHOP-9", Head: "fix/town-text", Base: "main", Status: "open", CI: "green", Review: "COMMENTED by b · 3 threads open", SinceHole: noon.Add(-2 * time.Hour)},
			{Number: 13, URL: "u/13", Title: "feat: admit a child SHOP-3", Base: "main", Status: "merged", CI: "green"},
			{Number: 20, URL: "u/20", Title: "perf: paginate", Head: "perf/lists", Base: "main", Status: "open", CI: "green"},
		},
		Parked: []Parked{
			{ID: 7, Ticket: "SHOP-5", On: "client", Text: "Which address?", CreatedAt: noon.Add(-5 * time.Hour)},
			{ID: 8, Ticket: "SHOP-9", On: "me", Text: "Answer the reviewer yourself\non the separator", CreatedAt: noon.Add(-3 * time.Hour)},
		},
		Decisions: []Decision{
			{Subject: "SHOP-7 / #10", Text: "keep both PRs"},
			{Subject: "#12 review", Text: "safe for a town source only"},
			{Subject: "auto-merge develop", Text: "on for every PR"},
		},
	}
}

// What waits on the user, oldest first, each with its ticket.
func TestViewWaits(t *testing.T) {
	v := BuildView(viewDay(), noon)
	var got []string
	for _, w := range v.Waits {
		got = append(got, w.Ticket+" "+w.Text)
	}
	want := []string{
		"SHOP-9 Answer the reviewer yourself",
		"SHOP-9 #12: COMMENTED by b · 3 threads open, and no worker or task on it",
		"SHOP-7 Approve worker1's plan",
		"SHOP-7 #11 is ready for your merge",
		" Read worker2's review draft",
	}
	if !slices.Equal(got, want) {
		t.Errorf("waits =\n%q\nwant\n%q", got, want)
	}
	if len(v.Parked) != 1 || v.Parked[0].ID != 7 {
		t.Errorf("parked on a third party = %+v, want #7 only", v.Parked)
	}
	if !v.PRWatch {
		t.Error("PRs with CI: the PR watch runs")
	}
}

// A parked document reads as what to read, from its row: its worker is
// free by then. Refused, it stays, flagged.
func TestViewParkedDocument(t *testing.T) {
	d := Day{Live: true, Parked: []Parked{
		{ID: 3, Ticket: "SHOP-9", Worker: "worker2", On: "me", Text: "Approve the plan", Kind: "plan", DocPath: "/p.md", CreatedAt: noon},
		{ID: 4, Worker: "reviewer1", On: "me", Text: "Read the verdict", Kind: "verdict", DocPath: "/v.md", CreatedAt: noon, Refused: true},
	}}
	v := BuildView(d, noon)
	if len(v.Waits) != 2 || v.Waits[0].Text != "Read worker2's plan" || !v.Waits[0].Doc || v.Waits[0].Refused || v.Waits[0].Detail != "Approve the plan" {
		t.Errorf("waits = %+v", v.Waits)
	}
	if !v.Waits[1].Refused || v.Waits[1].Text != "Read reviewer1's verdict" {
		t.Errorf("refused wait = %+v", v.Waits[1])
	}
}

// A line marked done steps aside, then comes back flagged if its wait is
// still there.
func TestViewMarks(t *testing.T) {
	d := viewDay()
	id := BuildView(d, noon).Waits[2].ID
	d.Marks[id] = noon.Add(-time.Minute)
	v := BuildView(d, noon)
	if len(v.Marked) != 1 || v.Marked[0].ID != id || len(v.Waits) != 4 {
		t.Fatalf("just marked: %d waits, marked %+v", len(v.Waits), v.Marked)
	}
	v = BuildView(d, noon.Add(2*time.Minute))
	if len(v.Marked) != 0 || len(v.Waits) != 5 || v.Waits[2].MarkedAt == nil {
		t.Errorf("still waiting after the grace: marked %+v, waits %+v", v.Marked, v.Waits)
	}
}

// A PR's line marked done stays aside until the PR's state changes, and
// its ticket then waits on the reviewers.
func TestViewMarksAPR(t *testing.T) {
	d := viewDay()
	d.Parked = nil // #8 would keep SHOP-9 on the user
	id := BuildView(d, noon).Waits[0].ID
	d.Marks[id] = noon.Add(-time.Minute)
	v := BuildView(d, noon.Add(time.Hour))
	if len(v.Marked) != 1 || v.Marked[0].ID != id || len(v.Waits) != 3 {
		t.Fatalf("an hour later: waits %+v, marked %+v", v.Waits, v.Marked)
	}
	if i := slices.IndexFunc(v.Tickets, func(tk Ticket) bool { return tk.Key == "SHOP-9" }); v.Tickets[i].Waiting != "reviewers" {
		t.Errorf("SHOP-9 = %+v", v.Tickets[i])
	}
	d.PRs[2].Review = "COMMENTED by b · 4 threads open"
	v = BuildView(d, noon.Add(time.Hour))
	if len(v.Marked) != 0 || len(v.Waits) != 4 || v.Waits[0].MarkedAt != nil {
		t.Errorf("a new thread: waits %+v, marked %+v", v.Waits, v.Marked)
	}
}

func TestViewTickets(t *testing.T) {
	v := BuildView(viewDay(), noon)
	byKey := map[string]Ticket{}
	var order []string
	for _, tk := range v.Tickets {
		byKey[tk.Key] = tk
		order = append(order, tk.Key)
	}
	// Oldest wait first: SHOP-5 waits on the client for 5 h.
	if want := []string{"SHOP-5", "SHOP-9", "SHOP-7"}; !slices.Equal(order, want) {
		t.Errorf("tickets = %v, want %v", order, want)
	}
	if tk := byKey["SHOP-7"]; tk.Phase != "ready to merge" || tk.Waiting != "you" || len(tk.PRs) != 2 || tk.Title != "feat: reissue SHOP-7" || len(tk.Decisions) != 1 {
		t.Errorf("SHOP-7 = %+v", tk)
	}
	if tk := byKey["SHOP-9"]; tk.Phase != "changes asked" || tk.Waiting != "you" || len(tk.Decisions) != 1 {
		t.Errorf("SHOP-9 = %+v (its decision came through #12)", tk)
	}
	if tk := byKey["SHOP-5"]; tk.Waiting != "client" {
		t.Errorf("SHOP-5 = %+v", tk)
	}
	if len(v.Done) != 1 || v.Done[0].Key != "SHOP-3" {
		t.Errorf("done = %+v", v.Done)
	}
	if len(v.NoTicket) != 1 || v.NoTicket[0].Number != 20 {
		t.Errorf("no ticket = %+v", v.NoTicket)
	}
	if len(v.Loose) != 1 || v.Loose[0].Subject != "auto-merge develop" {
		t.Errorf("loose decisions = %+v", v.Loose)
	}
}

// Review asks wait on whoever holds the PR, never on the reviewers.
func TestWaitingOnReviewAsks(t *testing.T) {
	asks := Ticket{Key: "SHOP-9", PRs: []PR{{Status: "open", CI: "running", Review: "COMMENTED by b · 3 threads open"}}}
	if who, _ := waiting(asks, nil, nil); who != "nobody" {
		t.Errorf("asks, nobody on it = %q", who)
	}
	asks.Workers = []string{"worker2"}
	if who, _ := waiting(asks, nil, nil); who != "worker2" {
		t.Errorf("asks, worker2 on it = %q", who)
	}
	if who, _ := waiting(Ticket{PRs: []PR{{Status: "open", CI: "green"}}}, nil, nil); who != "reviewers" {
		t.Errorf("no review yet = %q", who)
	}
}

// A past day shows the tickets its decisions name, without a stage.
func TestViewPastDay(t *testing.T) {
	v := BuildView(Day{Decisions: []Decision{{Subject: "SHOP-7 / #10", Text: "a"}, {Subject: "#12", Text: "b"}}}, noon)
	if len(v.Tickets) != 1 || v.Tickets[0].Key != "SHOP-7" || v.Tickets[0].Phase != "" || len(v.Loose) != 1 {
		t.Errorf("tickets %+v, loose %+v", v.Tickets, v.Loose)
	}
}

func TestPhase(t *testing.T) {
	w := []Worker{{Worker: "worker1", State: "plan_ready"}, {Worker: "worker2", State: "coding", PRURL: "u/99"}}
	for _, c := range []struct {
		t    Ticket
		want string
	}{
		{Ticket{PRs: []PR{{Status: "merged"}}}, "merged"},
		{Ticket{PRs: []PR{{Status: "open", CI: "green", Review: "APPROVED by a"}}}, "ready to merge"},
		{Ticket{PRs: []PR{{Status: "open", Review: "CHANGES_REQUESTED by a"}}}, "changes asked"},
		{Ticket{PRs: []PR{{Status: "conflicting"}}}, "in review"},
		{Ticket{Workers: []string{"worker2"}}, "draft PR"},
		{Ticket{Workers: []string{"worker1"}}, "plan to approve"},
		{Ticket{}, "in progress"},
	} {
		if got := phase(c.t, w); got != c.want {
			t.Errorf("phase(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}
