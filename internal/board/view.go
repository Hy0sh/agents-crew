package board

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The page answers three questions, in this order: what waits on a gesture
// of the user's, where each ticket stands, and whether the swarm runs
// well. View builds those answers from a Day; nothing it shows is stored.

// View is what the page renders for one repo and one day.
type View struct {
	Live  bool       `json:"live"`
	Seen  *time.Time `json:"seen"`
	Queue int        `json:"queue"`
	Inbox int        `json:"inbox"`
	// PRWatch says whether the PRs carry CI and reviews: without the PR
	// watch, "ready for your merge" and holes can't be seen.
	PRWatch bool `json:"pr_watch"`
	// Waits is what waits on the user, oldest first; Marked, the lines the
	// user marked done less than markGrace ago.
	Waits    []Wait     `json:"waits"`
	Marked   []Wait     `json:"marked"`
	Parked   []Parked   `json:"parked"`
	Tickets  []Ticket   `json:"tickets"`
	NoTicket []PR       `json:"no_ticket"`
	Done     []Ticket   `json:"done"`
	Loose    []Decision `json:"loose"`
	Workers  []Worker   `json:"workers"`
}

// Wait is one line of "Waiting on you". ID names the wait itself, its
// start included: a new wait of the same kind is a new line.
type Wait struct {
	ID     string    `json:"id"`
	Ticket string    `json:"ticket"`
	Text   string    `json:"text"`
	Who    string    `json:"who"`
	Detail string    `json:"detail"`
	URL    string    `json:"url"`
	Since  time.Time `json:"since"`
	// MarkedAt is set on a line marked done whose wait is still there.
	MarkedAt *time.Time `json:"marked_at"`
	// Doc is a parked document to read (GET /api/parked/<id>/doc), with
	// accept and refuse in place of done; Refused, one the user refused,
	// waiting for the master to discuss it.
	Doc     bool `json:"doc"`
	Refused bool `json:"refused"`
}

// Ticket is a ticket key and what the swarm holds of it.
type Ticket struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Phase string `json:"phase"`
	// Waiting is whom it waits on: "you", "the client", "nobody", "reviewers", "CI", a worker.
	Waiting   string     `json:"waiting"`
	WaitSince *time.Time `json:"wait_since"`
	PRs       []PR       `json:"prs"`
	Workers   []string   `json:"workers"`
	Parked    []Parked   `json:"parked"`
	Decisions []Decision `json:"decisions"`
}

// markGrace is how long a line marked done stays aside before it comes
// back, flagged, if its wait is still there: the master needs a turn or
// two to act on it. A PR's line is the exception: its ID holds the PR's
// state, and a mark keeps it aside until that state changes.
const markGrace = 2 * time.Minute

// isPRWait says the wait comes from a PR's state on GitHub, which the
// user often answers elsewhere (a reviewer seen on chat): only a change
// on the PR brings it back.
func isPRWait(id string) bool { return strings.HasPrefix(id, "pr-") }

// ticketKey is a Jira-like key: PROJ-123.
// ponytail: any WORD-123 matches, "UTF-8" in a PR title included; the
// branch is read first, where such words are rare.
var (
	ticketKey = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b`)
	prRef     = regexp.MustCompile(`#(\d+)\b`)
)

// keyIn is the first ticket key found in the texts, in their order.
func keyIn(texts ...string) string {
	for _, t := range texts {
		if k := ticketKey.FindString(t); k != "" {
			return k
		}
	}
	return ""
}

// prKey is a PR's ticket: from its branch, its title, then the branch it
// stacks on.
func prKey(p PR) string { return keyIn(p.Head, p.Title, p.Base) }

func isOpen(p PR) bool { return p.Status == "open" || p.Status == "conflicting" }

// BuildView turns d into the page's answers, at now.
func BuildView(d Day, now time.Time) View {
	v := View{Live: d.Live, Seen: d.Seen, Queue: d.Queue, Inbox: d.Inbox,
		Waits: []Wait{}, Marked: []Wait{}, Parked: []Parked{}, Tickets: []Ticket{}, NoTicket: []PR{}, Done: []Ticket{}, Loose: []Decision{},
		Workers: d.Workers}

	tickets := map[string]*Ticket{}
	ticket := func(key string) *Ticket {
		t, ok := tickets[key]
		if !ok {
			t = &Ticket{Key: key, PRs: []PR{}, Workers: []string{}, Parked: []Parked{}, Decisions: []Decision{}}
			tickets[key] = t
		}
		return t
	}

	// PRs, oldest first: the oldest gives the ticket its title.
	prs := slices.Clone(d.PRs)
	slices.SortFunc(prs, func(a, b PR) int { return a.Number - b.Number })
	byNumber, byURL := map[int]string{}, map[string]string{}
	for _, p := range prs {
		if p.CI != "" {
			v.PRWatch = true
		}
		k := prKey(p)
		byNumber[p.Number], byURL[p.URL] = k, k
		if k == "" {
			if isOpen(p) {
				v.NoTicket = append(v.NoTicket, p)
			}
			continue
		}
		t := ticket(k)
		if t.Title == "" {
			t.Title = p.Title
		}
		t.PRs = append(t.PRs, p)
	}
	workerKey := map[string]string{}
	for _, w := range d.Workers {
		k := keyIn(w.Branch, w.Subject)
		if k == "" {
			k = byURL[w.PRURL]
		}
		workerKey[w.Worker] = k
		if k == "" || w.State == "free" {
			continue
		}
		t := ticket(k)
		if t.Title == "" {
			t.Title = w.Subject
		}
		t.Workers = append(t.Workers, w.Worker)
	}
	for _, p := range d.Parked {
		if p.On != "me" {
			v.Parked = append(v.Parked, p)
		}
		if p.Ticket != "" {
			ticket(p.Ticket).Parked = append(ticket(p.Ticket).Parked, p)
		}
	}
	for _, dc := range d.Decisions {
		k := keyIn(dc.Subject)
		if k == "" {
			for _, m := range prRef.FindAllStringSubmatch(dc.Subject, -1) {
				n, _ := strconv.Atoi(m[1])
				if k = byNumber[n]; k != "" {
					break
				}
			}
		}
		// A past day has no PRs nor workers: its tickets are the ones its
		// decisions name.
		if _, ok := tickets[k]; !ok && k != "" && !d.Live {
			ticket(k)
		}
		if t, ok := tickets[k]; ok {
			t.Decisions = append(t.Decisions, dc)
		} else {
			v.Loose = append(v.Loose, dc)
		}
	}

	waits := userWaits(d, workerKey, byURL)
	// acked are the PRs whose wait the user marked done, state unchanged:
	// they wait on reviewers, not on the user nor on nobody.
	var open []Wait
	acked := map[string]bool{}
	for _, w := range waits {
		at, marked := d.Marks[w.ID]
		if marked && isPRWait(w.ID) {
			acked[w.URL] = true
		} else {
			open = append(open, w)
		}
		switch {
		case marked && (now.Sub(at) < markGrace || isPRWait(w.ID)):
			w.MarkedAt = &at
			v.Marked = append(v.Marked, w)
		case marked:
			w.MarkedAt = &at
			v.Waits = append(v.Waits, w)
		default:
			v.Waits = append(v.Waits, w)
		}
	}

	for _, t := range tickets {
		if !d.Live {
			v.Tickets = append(v.Tickets, *t)
			continue
		}
		t.Phase = phase(*t, d.Workers)
		t.Waiting, t.WaitSince = waiting(*t, open, acked)
		if t.Phase == "merged" && len(t.Workers) == 0 && len(t.Parked) == 0 {
			v.Done = append(v.Done, *t)
		} else {
			v.Tickets = append(v.Tickets, *t)
		}
	}
	slices.SortFunc(v.Tickets, func(a, b Ticket) int {
		switch {
		case a.WaitSince != nil && b.WaitSince != nil && !a.WaitSince.Equal(*b.WaitSince):
			return a.WaitSince.Compare(*b.WaitSince)
		case a.WaitSince != nil && b.WaitSince == nil:
			return -1
		case a.WaitSince == nil && b.WaitSince != nil:
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	slices.SortFunc(v.Done, func(a, b Ticket) int { return strings.Compare(a.Key, b.Key) })
	return v
}

// userWaits lists what waits on the user, oldest first.
func userWaits(d Day, workerKey, prKeyByURL map[string]string) []Wait {
	var out []Wait
	for _, w := range d.Workers {
		wait := Wait{Ticket: workerKey[w.Worker], Who: w.Worker + " · " + w.State, Detail: w.Subject, URL: w.PRURL, Since: w.Since}
		switch {
		case w.State == "blocked":
			wait.Text = w.Worker + " is stopped on a prompt: a tool approval or a question"
		case strings.HasSuffix(w.State, "_ready"):
			wait.Text = readyText(w)
		case w.BlockedOn != "":
			wait.Text = w.Worker + " waits: " + w.BlockedOn
		default:
			continue
		}
		wait.ID = fmt.Sprintf("worker:%s:%s:%d", w.Worker, w.State, w.Since.Unix())
		out = append(out, wait)
	}
	for _, p := range d.PRs {
		if !isOpen(p) {
			continue
		}
		base := Wait{Ticket: prKeyByURL[p.URL], Who: p.Worker, Detail: p.Title, URL: p.URL}
		// ponytail: the state is what the PR watch keeps (status, CI, last
		// review, open threads); a second review of the same kind by the
		// same reviewer, or a plain comment, doesn't change it.
		state := p.Status + "|" + p.CI + "|" + p.Review
		if !p.SinceReady.IsZero() {
			w := base
			w.ID, w.Since = fmt.Sprintf("pr-ready:%d:%d:%s", p.Number, p.SinceReady.Unix(), state), p.SinceReady
			w.Text = fmt.Sprintf("#%d is ready for your merge", p.Number)
			out = append(out, w)
		}
		if !p.SinceHole.IsZero() {
			w := base
			w.ID, w.Since = fmt.Sprintf("pr-hole:%d:%d:%s", p.Number, p.SinceHole.Unix(), state), p.SinceHole
			w.Text = fmt.Sprintf("#%d: %s, and no worker or task on it", p.Number, p.Review)
			w.Who = "nobody"
			out = append(out, w)
		}
	}
	for _, p := range d.Parked {
		if p.On != "me" {
			continue
		}
		w := Wait{ID: fmt.Sprintf("parked:%d", p.ID), Ticket: p.Ticket, Text: firstLine(p.Text),
			Who: fmt.Sprintf("parked #%d", p.ID), Detail: p.Text, Since: p.CreatedAt, Refused: p.Refused}
		if p.DocPath != "" {
			// From the row: its worker was freed when it was parked.
			w.Text, w.Doc = "Read "+p.Worker+"'s "+p.Kind, true
		}
		out = append(out, w)
	}
	slices.SortStableFunc(out, func(a, b Wait) int { return a.Since.Compare(b.Since) })
	return out
}

// readyText is the gesture a worker in state <what>_ready waits for.
func readyText(w Worker) string {
	switch strings.TrimSuffix(w.State, "_ready") {
	case "plan":
		return "Approve " + w.Worker + "'s plan"
	case "verdict":
		return "Rule on " + w.Worker + "'s verdict"
	case "review":
		return "Read " + w.Worker + "'s review draft"
	}
	return w.Worker + ": " + strings.ReplaceAll(w.State, "_", " ")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// phase is where a ticket stands: the furthest stage that applies.
func phase(t Ticket, workers []Worker) string {
	var anyOpen, ready, asks bool
	for _, p := range t.PRs {
		if !isOpen(p) {
			continue
		}
		anyOpen = true
		ready = ready || !p.SinceReady.IsZero() || readyNow(p)
		asks = asks || strings.Contains(p.Review, "CHANGES_REQUESTED") || strings.Contains(p.Review, "threads open")
	}
	states := map[string]bool{}
	draft := false
	for _, w := range workers {
		if slices.Contains(t.Workers, w.Worker) {
			states[w.State] = true
			if w.PRURL != "" && !slices.ContainsFunc(t.PRs, func(p PR) bool { return p.URL == w.PRURL }) {
				draft = true // the PR watch leaves drafts out
			}
		}
	}
	switch {
	case len(t.PRs) > 0 && !anyOpen && !draft:
		return "merged"
	case ready:
		return "ready to merge"
	case asks:
		return "changes asked"
	case anyOpen:
		return "in review"
	case draft:
		return "draft PR"
	case states["plan_ready"]:
		return "plan to approve"
	case states["verdict_ready"]:
		return "verdict to rule on"
	}
	return "in progress"
}

// readyNow is a PR approved, green and mergeable: a row written before the
// watcher dated it still shows as ready.
func readyNow(p PR) bool {
	return p.Status == "open" && p.CI == "green" && strings.HasPrefix(p.Review, "APPROVED")
}

// waiting says whom a ticket waits on, and since when when it is known.
// acked are the PR URLs whose review asks the user marked done.
func waiting(t Ticket, waits []Wait, acked map[string]bool) (string, *time.Time) {
	for _, w := range waits {
		if w.Ticket == t.Key && w.Who != "nobody" {
			since := w.Since
			return "you", &since
		}
	}
	for _, p := range t.Parked {
		if p.On != "me" {
			since := p.CreatedAt
			return p.On, &since
		}
	}
	for _, w := range waits {
		if w.Ticket == t.Key {
			since := w.Since
			return "nobody", &since
		}
	}
	// Review asks are answered by whoever holds the PR, not by reviewers.
	for _, p := range t.PRs {
		if isOpen(p) && !acked[p.URL] && (strings.Contains(p.Review, "CHANGES_REQUESTED") || strings.Contains(p.Review, "threads open")) {
			if len(t.Workers) > 0 {
				return strings.Join(t.Workers, ", "), nil
			}
			return "nobody", nil
		}
	}
	for _, p := range t.PRs {
		if isOpen(p) && p.CI == "running" {
			return "CI", nil
		}
	}
	for _, p := range t.PRs {
		if isOpen(p) {
			return "reviewers", nil
		}
	}
	if len(t.Workers) > 0 {
		return strings.Join(t.Workers, ", "), nil
	}
	return "", nil
}
