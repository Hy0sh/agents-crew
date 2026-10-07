package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// gh resolves the repo (remotes, hosts it knows); acw only takes the host
// from the URL it answers, GitHub Enterprise included.
func TestParseGHRepo(t *testing.T) {
	for data, want := range map[string]string{
		`{"url": "https://github.com/some-org/some-repo"}`:      "github.com/some-org/some-repo",
		`{"url": "https://some-co.ghe.com/SOME-ORG/some-repo"}`: "some-co.ghe.com/SOME-ORG/some-repo",
	} {
		if got, err := parseGHRepo([]byte(data)); err != nil || got != want {
			t.Errorf("parseGHRepo(%s) = %q, %v; want %q", data, got, err, want)
		}
	}
	for _, data := range []string{`{"url": "https://github.com/some-org"}`, `{}`, ``} {
		if got, err := parseGHRepo([]byte(data)); err == nil {
			t.Errorf("parseGHRepo(%s) = %q; want an error", data, got)
		}
	}
}

const searchFixture = `{"data": {
  "viewer": {"login": "me"},
  "search": {"nodes": [
    {"number": 12, "url": "https://github.com/some-org/some-repo/pull/12", "title": "Export orders as CSV", "baseRefName": "main", "mergeable": "CONFLICTING",
     "reviews": {"nodes": [{"state": "CHANGES_REQUESTED", "author": {"login": "alice"}},
                           {"state": "APPROVED", "author": {"login": "bob"}},
                           {"state": "COMMENTED", "author": {"login": "me"}}]},
     "reviewThreads": {"nodes": [{"isResolved": false}, {"isResolved": true}, {"isResolved": false}]},
     "commits": {"nodes": [{"commit": {"oid": "abc", "committer": {"name": "Me", "user": {"login": "me"}}, "statusCheckRollup": {"state": "FAILURE"}}}]}},
    {"number": 13, "url": "https://github.com/some-org/some-repo/pull/13", "mergeable": "UNKNOWN",
     "reviews": {"nodes": []}, "reviewThreads": {"nodes": []},
     "commits": {"nodes": [{"commit": {"oid": "def", "committer": {"name": "Someone", "user": null}, "statusCheckRollup": null}}]}}
  ]}
}}`

func TestParsePRSearch(t *testing.T) {
	viewer, prs, err := parsePRSearch([]byte(searchFixture))
	if err != nil || viewer != "me" || len(prs) != 2 {
		t.Fatalf("parsePRSearch() = %q, %d PRs, %v", viewer, len(prs), err)
	}
	// The viewer's own review (a reply to a thread is one) is not counted:
	// it must not wake the master.
	want12 := prState{Number: 12, URL: "https://github.com/some-org/some-repo/pull/12", Title: "Export orders as CSV", Base: "main", Mergeable: "CONFLICTING", CI: "red",
		Reviews: 2, LastReview: "APPROVED by bob", OpenThreads: 2, HeadOID: "abc", Committer: "me"}
	if prs[0] != want12 {
		t.Errorf("PR 12 = %+v, want %+v", prs[0], want12)
	}
	if prs[0].Title != "Export orders as CSV" || prs[0].Base != "main" {
		t.Errorf("title/base = %q, %q", prs[0].Title, prs[0].Base)
	}
	// No checks at all stays running; a committer with no linked account
	// is named by the commit.
	want13 := prState{Number: 13, URL: "https://github.com/some-org/some-repo/pull/13", Mergeable: "UNKNOWN", CI: "running",
		HeadOID: "def", Committer: "Someone"}
	if prs[1] != want13 {
		t.Errorf("PR 13 = %+v, want %+v", prs[1], want13)
	}
}

func TestParsePRFate(t *testing.T) {
	for data, want := range map[string]string{
		`{"data": {"repository": {"pullRequest": {"state": "MERGED", "isDraft": false}}}}`: "merged",
		`{"data": {"repository": {"pullRequest": {"state": "CLOSED", "isDraft": false}}}}`: "closed",
		`{"data": {"repository": {"pullRequest": {"state": "OPEN", "isDraft": true}}}}`:    "back to draft",
		`{"data": {"repository": {"pullRequest": {"state": "OPEN", "isDraft": false}}}}`:   "",
	} {
		if got, err := parsePRFate([]byte(data)); err != nil || got != want {
			t.Errorf("parsePRFate(%s) = %q, %v; want %q", data, got, err, want)
		}
	}
}

func TestDiffPRsFirstPollIsTheBaseline(t *testing.T) {
	pr := prState{Number: 1, Mergeable: "CONFLICTING", CI: "red"}
	next, changes, gone := diffPRs(nil, []prState{pr}, "me")
	if len(changes) != 0 || len(gone) != 0 {
		t.Errorf("first poll reported %v, gone %v; want nothing", changes, gone)
	}
	if next[1].LastFinal != "red" {
		t.Errorf("baseline LastFinal = %q, want red", next[1].LastFinal)
	}
}

func TestDiffPRs(t *testing.T) {
	base := prState{Number: 1, URL: "u1", Mergeable: "MERGEABLE", CI: "green", LastFinal: "green", HeadOID: "a", Committer: "me"}
	for _, c := range []struct {
		name     string
		old, cur func(*prState)
		want     []string
	}{
		{"nothing moved", nil, nil, nil},
		{"conflict", nil, func(p *prState) { p.Mergeable = "CONFLICTING" }, []string{"conflict with base"}},
		{"unknown baseline then conflict", func(p *prState) { p.Mergeable = "UNKNOWN" }, func(p *prState) { p.Mergeable = "CONFLICTING" }, []string{"conflict with base"}},
		{"conflict resolved", func(p *prState) { p.Mergeable = "CONFLICTING" }, nil, []string{"conflict resolved"}},
		{"mergeability being computed", nil, func(p *prState) { p.Mergeable = "UNKNOWN" }, nil},
		{"new review", nil, func(p *prState) { p.Reviews, p.LastReview = 1, "APPROVED by bob" }, []string{"review APPROVED by bob"}},
		{"more open threads", nil, func(p *prState) { p.OpenThreads = 2 }, []string{"open threads 0 → 2"}},
		{"fewer open threads", func(p *prState) { p.OpenThreads = 2 }, func(p *prState) { p.OpenThreads = 1 }, nil},
		{"CI red", nil, func(p *prState) { p.CI = "red" }, []string{"CI red"}},
		{"CI running", nil, func(p *prState) { p.CI = "running" }, nil},
		{"CI green after red", func(p *prState) { p.CI, p.LastFinal = "running", "red" }, nil, []string{"CI green again"}},
		{"CI green after green", func(p *prState) { p.CI, p.LastFinal = "running", "green" }, nil, nil},
		{"head commit by me", nil, func(p *prState) { p.HeadOID = "b" }, nil},
		{"head commit by another", nil, func(p *prState) { p.HeadOID, p.Committer = "b", "web-flow" }, []string{"new head commit by web-flow"}},
	} {
		old, cur := base, base
		if c.old != nil {
			c.old(&old)
		}
		if c.cur != nil {
			c.cur(&cur)
		}
		_, changes, _ := diffPRs(map[int]prState{1: old}, []prState{cur}, "me")
		var got []string
		if len(changes) == 1 {
			got = changes[0].Events
		}
		if len(changes) > 1 || !slices.Equal(got, c.want) {
			t.Errorf("%s: events = %q, want %q", c.name, got, c.want)
		}
	}
}

// UNKNOWN is GitHub recomputing, not a state: the last known one is kept,
// so the next real value is compared against it.
func TestDiffPRsKeepsTheLastKnownMergeability(t *testing.T) {
	old := prState{Number: 1, Mergeable: "MERGEABLE", CI: "green"}
	next, _, _ := diffPRs(map[int]prState{1: old}, []prState{{Number: 1, Mergeable: "UNKNOWN", CI: "green"}}, "me")
	if next[1].Mergeable != "MERGEABLE" {
		t.Errorf("next Mergeable = %q, want the last known MERGEABLE", next[1].Mergeable)
	}
}

func TestDiffPRsNewAndGone(t *testing.T) {
	_, changes, gone := diffPRs(map[int]prState{1: {Number: 1}}, []prState{{Number: 2, CI: "running"}}, "me")
	if len(changes) != 1 || changes[0].PR.Number != 2 || !slices.Equal(changes[0].Events, []string{"new PR"}) {
		t.Errorf("changes = %+v, want PR 2 as new", changes)
	}
	if !slices.Equal(gone, []int{1}) {
		t.Errorf("gone = %v, want [1]", gone)
	}
}

func TestPRLine(t *testing.T) {
	pr := prState{Number: 3, URL: "https://github.com/some-org/some-repo/pull/3"}
	if got, want := prLine(pr, "worker2", []string{"CI red", "conflict with base"}),
		"PR #3 (worker2): CI red; conflict with base | https://github.com/some-org/some-repo/pull/3"; got != want {
		t.Errorf("prLine() = %q, want %q", got, want)
	}
	if got, want := prLine(pr, "", []string{"merged"}), "PR #3: merged | https://github.com/some-org/some-repo/pull/3"; got != want {
		t.Errorf("prLine() without worker = %q, want %q", got, want)
	}
}

func TestPROwners(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"worker1.json": `{"pr_url": "https://github.com/some-org/some-repo/pull/7/"}`,
		"worker2.json": `{"pr_url": ""}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := prOwners(dir, []string{"worker1", "worker2", "worker3"})
	want := map[string]string{"https://github.com/some-org/some-repo/pull/7": "worker1"}
	if !maps.Equal(got, want) {
		t.Errorf("prOwners() = %v, want %v", got, want)
	}
}

// A gh that hangs must not freeze the watcher: block and silence alerts
// run in the same loop.
func TestGhTimesOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(d time.Duration) { ghTimeout = d }(ghTimeout)
	ghTimeout = 200 * time.Millisecond

	start := time.Now()
	if _, err := gh("api", "graphql"); err == nil {
		t.Error("gh() = nil error, want a timeout")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("gh() returned after %v, want it cut at the timeout", elapsed)
	}
}

func searchResponse(nodes ...string) []byte {
	return []byte(`{"data": {"viewer": {"login": "me"}, "search": {"nodes": [` + strings.Join(nodes, ",") + `]}}}`)
}

func prNode(number int, mergeable string) string {
	return fmt.Sprintf(`{"number": %d, "url": "https://github.com/some-org/some-repo/pull/%d", "mergeable": %q,
		"commits": {"nodes": [{"commit": {"oid": "a", "committer": {"name": "Me", "user": {"login": "me"}}, "statusCheckRollup": {"state": "SUCCESS"}}}]}}`,
		number, number, mergeable)
}

func TestPRWatcherPoll(t *testing.T) {
	responses := [][]byte{
		searchResponse(prNode(1, "MERGEABLE"), prNode(2, "MERGEABLE")),   // baseline
		searchResponse(prNode(1, "CONFLICTING")),                         // 2 dropped by the search, still open
		searchResponse(prNode(1, "CONFLICTING"), prNode(2, "MERGEABLE")), // 2 back: not a new PR
		searchResponse(prNode(2, "MERGEABLE")),                           // 1 merged
		searchResponse(prNode(2, "MERGEABLE")),                           // nothing moved
	}
	fates := map[int]string{1: "merged", 2: ""}
	i := 0
	p := &prWatcher{
		fetch: func() ([]byte, error) { i++; return responses[i-1], nil },
		fate:  func(n int) (string, error) { return fates[n], nil },
	}
	owners := map[string]string{"https://github.com/some-org/some-repo/pull/1": "worker2"}
	want := [][]string{
		nil,
		{"PR #1 (worker2): conflict with base | https://github.com/some-org/some-repo/pull/1"},
		nil,
		{"PR #1 (worker2): merged | https://github.com/some-org/some-repo/pull/1"},
		nil,
	}
	for step, w := range want {
		if got := p.poll(owners); !slices.Equal(got, w) {
			t.Errorf("poll %d = %q, want %q", step, got, w)
		}
	}
}

func TestPRWatcherFateUnknown(t *testing.T) {
	responses := [][]byte{searchResponse(prNode(1, "MERGEABLE")), searchResponse()}
	i := 0
	p := &prWatcher{
		fetch: func() ([]byte, error) { i++; return responses[i-1], nil },
		fate:  func(int) (string, error) { return "", errors.New("HTTP 502") },
	}
	p.poll(nil)
	want := []string{"PR #1: gone (state unknown) | https://github.com/some-org/some-repo/pull/1"}
	if got := p.poll(nil); !slices.Equal(got, want) {
		t.Errorf("poll = %q, want %q", got, want)
	}
}

// The master hears once that PR signals stopped, not at every failed
// poll, and again only after a success broke the streak.
func TestPRWatcherAlertsOnceAfterRepeatedFailures(t *testing.T) {
	fail := true
	p := &prWatcher{
		fetch: func() ([]byte, error) {
			if fail {
				return nil, errors.New("HTTP 502")
			}
			return searchResponse(prNode(1, "MERGEABLE")), nil
		},
		fate: func(int) (string, error) { return "", nil },
	}
	alerts := 0
	for range 5 {
		for _, l := range p.poll(nil) {
			if strings.HasPrefix(l, "PR watch failing: ") {
				alerts++
			}
		}
	}
	if alerts != 1 {
		t.Errorf("%d alerts over 5 failed polls, want 1", alerts)
	}
	fail = false
	if got := p.poll(nil); got != nil {
		t.Errorf("first successful poll = %q, want the silent baseline", got)
	}
	fail = true
	p.poll(nil)
	p.poll(nil)
	if got := p.poll(nil); len(got) != 1 {
		t.Errorf("third failure after a success = %q, want a new alert", got)
	}
}
