package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The PR watch: acw's watcher follows the user's open pull requests on the
// repo and tells the master what changed on them, as lines in the same
// inbox, so the master keeps a single listener. A poll where nothing
// changed sends nothing. What is worth a line is decided by diffPRs, a
// pure function of two polls; prWatcher makes the calls.

// prWatchEvery is how often the watch polls GitHub: a review wave moves
// in minutes, and one GraphQL call per cycle stays far under the rate
// limit.
const prWatchEvery = 2 * time.Minute

var githubRemote = regexp.MustCompile(`^(?:git@github\.com:|ssh://git@github\.com/|https://github\.com/)([^/]+)/([^/]+?)(?:\.git)?/?$`)

// githubRepo is "owner/name" for a GitHub remote URL.
func githubRepo(remote string) (string, error) {
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil {
		return "", fmt.Errorf("origin is not a GitHub remote: %q", remote)
	}
	return m[1] + "/" + m[2], nil
}

// prState is one pull request as one poll sees it.
type prState struct {
	Number    int
	URL       string
	Title     string
	Base      string
	Mergeable string // MERGEABLE, CONFLICTING, or UNKNOWN while GitHub computes it
	CI        string // red, green or running
	// LastFinal is the last red or green seen, kept across running, so
	// green after a red can be told from green after a green.
	LastFinal   string
	Reviews     int
	LastReview  string // "APPROVED by alice"
	OpenThreads int
	HeadOID     string
	// Committer is the head commit's committer login, or its name when no
	// GitHub account is linked to it.
	Committer string
}

const prSearchQuery = `query($q: String!) {
  viewer { login }
  search(query: $q, type: ISSUE, first: 50) {
    nodes {
      ... on PullRequest {
        number url title baseRefName mergeable
        reviews(last: 100) { nodes { state author { login } } }
        reviewThreads(first: 100) { nodes { isResolved } }
        commits(last: 1) { nodes { commit { oid committer { name user { login } } statusCheckRollup { state } } } }
      }
    }
  }
}`

const prFateQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) { state isDraft } }
}`

type prSearchResponse struct {
	Data struct {
		Viewer struct{ Login string }
		Search struct {
			Nodes []struct {
				Number      int
				URL         string
				Title       string
				BaseRefName string
				Mergeable   string
				Reviews     struct {
					Nodes []struct {
						State  string
						Author struct{ Login string }
					}
				}
				ReviewThreads struct {
					Nodes []struct{ IsResolved bool }
				}
				Commits struct {
					Nodes []struct {
						Commit struct {
							OID       string
							Committer struct {
								Name string
								User *struct{ Login string }
							}
							StatusCheckRollup *struct{ State string }
						}
					}
				}
			}
		}
	}
}

func parsePRSearch(data []byte) (viewer string, prs []prState, err error) {
	var r prSearchResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return "", nil, err
	}
	for _, n := range r.Data.Search.Nodes {
		if n.Number == 0 {
			continue
		}
		pr := prState{Number: n.Number, URL: n.URL, Title: n.Title, Base: n.BaseRefName, Mergeable: n.Mergeable, CI: "running"}
		// The viewer's own reviews are left out: replying to a thread
		// submits one, and it must not wake the master.
		for _, rv := range n.Reviews.Nodes {
			if rv.Author.Login == r.Data.Viewer.Login {
				continue
			}
			pr.Reviews++
			pr.LastReview = rv.State + " by " + rv.Author.Login
		}
		for _, t := range n.ReviewThreads.Nodes {
			if !t.IsResolved {
				pr.OpenThreads++
			}
		}
		if c := n.Commits.Nodes; len(c) > 0 {
			commit := c[0].Commit
			pr.HeadOID, pr.Committer = commit.OID, commit.Committer.Name
			if commit.Committer.User != nil {
				pr.Committer = commit.Committer.User.Login
			}
			if commit.StatusCheckRollup != nil {
				pr.CI = ciVerdict(commit.StatusCheckRollup.State)
			}
		}
		prs = append(prs, pr)
	}
	return r.Data.Viewer.Login, prs, nil
}

func ciVerdict(state string) string {
	switch state {
	case "FAILURE", "ERROR":
		return "red"
	case "SUCCESS":
		return "green"
	}
	return "running"
}

// parsePRFate says what became of a PR gone from the search: "merged",
// "closed", "back to draft", or "" when it is still open and the search
// index only lags behind.
func parsePRFate(data []byte) (string, error) {
	var r struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					State   string
					IsDraft bool
				}
			}
		}
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", err
	}
	switch pr := r.Data.Repository.PullRequest; {
	case pr.State == "MERGED":
		return "merged", nil
	case pr.State == "CLOSED":
		return "closed", nil
	case pr.IsDraft:
		return "back to draft", nil
	}
	return "", nil
}

// prChange is one PR worth a line, with what changed on it.
type prChange struct {
	PR     prState
	Events []string
}

// diffPRs compares a poll with the previous one and returns the new
// snapshot, the PRs that changed, and the numbers gone from the search.
// prev nil is the first poll: the baseline, nothing to report.
func diffPRs(prev map[int]prState, cur []prState, viewer string) (next map[int]prState, changes []prChange, gone []int) {
	next = map[int]prState{}
	for _, pr := range cur {
		old, seen := prev[pr.Number]
		if seen && pr.Mergeable != "MERGEABLE" && pr.Mergeable != "CONFLICTING" {
			pr.Mergeable = old.Mergeable
		}
		pr.LastFinal = old.LastFinal
		if pr.CI != "running" {
			pr.LastFinal = pr.CI
		}
		next[pr.Number] = pr
		if prev == nil {
			continue
		}
		events := []string{"new PR"}
		if seen {
			events = prEvents(old, pr, viewer)
		}
		if len(events) > 0 {
			changes = append(changes, prChange{PR: pr, Events: events})
		}
	}
	for n := range prev {
		if _, ok := next[n]; !ok {
			gone = append(gone, n)
		}
	}
	slices.SortFunc(changes, func(a, b prChange) int { return a.PR.Number - b.PR.Number })
	slices.Sort(gone)
	return next, changes, gone
}

// prEvents is what changed on one PR between two polls, as the master
// should hear it. A push by the viewer is left out: workers push under the
// user's account, and the master hears about their pushes from them.
func prEvents(old, pr prState, viewer string) []string {
	var events []string
	if pr.Mergeable == "CONFLICTING" && old.Mergeable != "CONFLICTING" {
		events = append(events, "conflict with base")
	}
	if pr.Mergeable == "MERGEABLE" && old.Mergeable == "CONFLICTING" {
		events = append(events, "conflict resolved")
	}
	if pr.Reviews > old.Reviews {
		review := "new review"
		if pr.LastReview != "" {
			review = "review " + pr.LastReview
		}
		events = append(events, review)
	}
	if pr.OpenThreads > old.OpenThreads {
		events = append(events, fmt.Sprintf("open threads %d → %d", old.OpenThreads, pr.OpenThreads))
	}
	if pr.CI == "red" && old.CI != "red" {
		events = append(events, "CI red")
	}
	if pr.CI == "green" && old.CI != "green" && old.LastFinal == "red" {
		events = append(events, "CI green again")
	}
	if pr.HeadOID != old.HeadOID && pr.Committer != viewer {
		events = append(events, "new head commit by "+pr.Committer)
	}
	return events
}

// prLine is the inbox line for one PR.
func prLine(pr prState, worker string, events []string) string {
	who := ""
	if worker != "" {
		who = " (" + worker + ")"
	}
	line := fmt.Sprintf("PR #%d%s: %s", pr.Number, who, strings.Join(events, "; "))
	if pr.URL != "" {
		line += " | " + pr.URL
	}
	return line
}

// prOwners maps each PR url found in a worker's status file to that
// worker's label.
func prOwners(statusDir string, labels []string) map[string]string {
	owners := map[string]string{}
	for _, l := range labels {
		s, _ := readWorkerStatus(filepath.Join(statusDir, l+".json"))
		if s.PRURL != "" {
			owners[normalizePRURL(s.PRURL)] = l
		}
	}
	return owners
}

func normalizePRURL(u string) string {
	return strings.TrimSuffix(strings.TrimSpace(u), "/")
}

// prFailuresBeforeAlert is how many polls in a row may fail before the
// master hears that PR signals stopped coming.
const prFailuresBeforeAlert = 3

// prWatcher runs the PR watch's polls. fetch and fate are the two GitHub
// calls, replaced in tests.
type prWatcher struct {
	fetch    func() ([]byte, error)
	fate     func(number int) (string, error)
	prev     map[int]prState // nil until a poll succeeds
	failures int
	last     time.Time // when runWatch last polled
	// closed is what the last poll learned of PRs gone from the search:
	// merged, closed, back to draft. For the board.
	closed []prFate
}

type prFate struct {
	PR   prState
	Fate string
}

// newPRWatcher follows repo, "owner/name", through gh.
func newPRWatcher(repo string) *prWatcher {
	owner, name, _ := strings.Cut(repo, "/")
	return &prWatcher{
		fetch: func() ([]byte, error) {
			return gh("api", "graphql", "-f", "query="+prSearchQuery,
				"-f", "q=repo:"+repo+" is:pr is:open author:@me draft:false")
		},
		fate: func(number int) (string, error) {
			data, err := gh("api", "graphql", "-f", "query="+prFateQuery,
				"-f", "owner="+owner, "-f", "name="+name, "-F", fmt.Sprintf("number=%d", number))
			if err != nil {
				return "", err
			}
			return parsePRFate(data)
		},
	}
}

// ghTimeout bounds each gh call: the watch runs in the watcher's loop, and
// a gh stuck on the network would stop block and silence alerts with it.
var ghTimeout = 30 * time.Second

func gh(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	// Without it, a child of gh still holding the pipes would keep Output
	// waiting after the kill.
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args[:2], " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// poll runs one cycle and returns the lines for the master, none when
// nothing changed. owners maps a PR url to its worker (see prOwners).
func (p *prWatcher) poll(owners map[string]string) []string {
	data, err := p.fetch()
	var viewer string
	var cur []prState
	if err == nil {
		viewer, cur, err = parsePRSearch(data)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "PR watch:", err)
		p.failures++
		if p.failures == prFailuresBeforeAlert {
			return []string{"PR watch failing: " + err.Error()}
		}
		return nil
	}
	p.failures = 0
	p.closed = nil

	next, changes, gone := diffPRs(p.prev, cur, viewer)
	var lines []string
	for _, c := range changes {
		lines = append(lines, prLine(c.PR, owners[normalizePRURL(c.PR.URL)], c.Events))
	}
	for _, n := range gone {
		old := p.prev[n]
		fate, err := p.fate(n)
		if err != nil {
			fmt.Fprintln(os.Stderr, "PR watch:", err)
			fate = "gone (state unknown)"
		}
		if fate == "" {
			// Still open: the search dropped it for a moment. Kept, so it
			// does not come back as a new PR.
			next[n] = old
			continue
		}
		p.closed = append(p.closed, prFate{old, fate})
		lines = append(lines, prLine(old, owners[normalizePRURL(old.URL)], []string{fate}))
	}
	p.prev = next
	return lines
}
