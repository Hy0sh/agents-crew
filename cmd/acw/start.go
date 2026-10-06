package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Hy0sh/agents-crew/internal/brief"
	"github.com/Hy0sh/agents-crew/internal/gitutil"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/preflight"
	"github.com/Hy0sh/agents-crew/internal/wtm"
)

// runStart creates the master, hands it its brief, writes the pool its
// watcher opens workers from, then execs into the Herdr TUI so the caller
// can start talking to the master immediately. No worker is opened here:
// the watcher opens min-workers at once, the others as tasks are queued.
func runStart(out io.Writer, repo string, opts *startOptions, workers []workerSpec) error {
	// Only coders need an environment; a worker outside the code has none.
	coders := coderCount(workers)
	maxStacks := opts.maxStacks
	if maxStacks <= 0 || maxStacks > coders {
		maxStacks = coders
	}

	slug := names.Slug(repo)
	masterName := names.Master(slug)

	for _, kind := range distinctKinds(workers) {
		preflight.WarnIfAgentsFileMissing(repo, kind, func(format string, a ...any) { fmt.Fprintf(out, format, a...) })
	}

	// Scoped to this directory, not global: two different repos each get
	// their own master/worker names (see internal/names), and a swarm
	// already running for a DIFFERENT repo never blocks this one. Found by
	// name, not by its pane's cwd: with master-dir it runs elsewhere.
	agents, err := herdr.AgentList()
	if err != nil {
		return fmt.Errorf("herdr agent list: %w", err)
	}
	if a, ok := herdr.FindAgent(agents, masterName); ok {
		return fmt.Errorf("a master is already running in workspace %s for this directory. Attach to it (herdr workspace focus %s) "+
			"instead of starting another one, or close it first (acw stop)", a.WorkspaceID, a.WorkspaceID)
	}

	// Before anything is created: a custom brief with a typo'd variable
	// must fail here, not after a workspace and a master agent were started
	// for nothing.
	customBrief, err := readCustomBrief(opts.briefPath)
	if err != nil {
		return err
	}
	extra, err := readCustomBrief(opts.extraPath)
	if err != nil {
		return err
	}
	var prWatchRepo string
	if opts.prWatch {
		if prWatchRepo, err = resolvePRWatch(repo); err != nil {
			return err
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	inbox := names.Inbox(repo)
	inboxWatch, warn := inboxWatchCommand(opts.masterKind, customBrief, self, inbox)
	if warn {
		fmt.Fprintln(out, "⚠ the custom brief doesn't contain {{.InboxWatch}}: workers' pings will be typed into the master's input, as before")
	}
	if inboxWatch == "" {
		inbox = ""
	}
	var inboxNext string
	if inbox != "" {
		inboxNext = inboxNextCommand(self, inbox)
	}
	notes := readNotes(out, repo, opts.notesPath)
	// A repo wtm doesn't know gets no stack: its adopts all fail, and its
	// workers must not be told about wtm switch.
	stacks := coders > 0 && wtm.Available() && wtm.Registered(repo)
	var switchCommand string
	if stacks && wtm.SwitchAvailable() {
		switchCommand = switchCommandFor(opts.profile)
	}
	masterBrief, err := buildBrief(briefSource(customBrief, extra), brief.Params{
		RepoPath:         repo,
		Slug:             slug,
		Stacks:           stacks,
		MaxStacks:        maxStacks,
		MinWorkers:       opts.minWorkers,
		IdleCloseMinutes: opts.idleCloseMinutes,
		Profile:          opts.profile,
		Notes:            notes,
		Workers:          briefWorkers(workers),
		InboxWatch:       inboxWatch,
		InboxNext:        inboxNext,
		SilenceMinutes:   opts.silenceMinutes,
		StatusCommand:    shellWord(self) + " status --repo " + shellWord(repo),
		QueueCommand:     shellWord(self) + " queue --repo " + shellWord(repo),
		DoneCommand:      shellWord(self) + " done --repo " + shellWord(repo),
		TellCommand:      shellWord(self) + " tell --repo " + shellWord(repo),
		DecisionCommand:  shellWord(self) + " board decision --repo " + shellWord(repo),
		SwitchCommand:    switchCommand,
		PRWatch:          prWatchRepo != "",
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		return err
	}
	if err := writeSystemPrompts(names.StatusDir(repo), notes, workers); err != nil {
		return err
	}
	stamp := time.Now().Format("20060102150405")

	masterDir := repo
	if opts.masterDir != "" {
		masterDir = opts.masterDir
	}
	workspaceID, masterPane, err := herdr.WorkspaceCreate(masterDir, names.Label(repo), true)
	if err != nil {
		return fmt.Errorf("herdr workspace create: %w", err)
	}
	// From here on, clean up the half-built workspace on any failure.
	success := false
	defer func() {
		if !success {
			_ = herdr.WorkspaceClose(workspaceID, true)
		}
	}()

	if err := herdr.PaneRename(masterPane, "master"); err != nil {
		return err
	}
	if err := herdr.AgentStart(masterName, opts.masterKind, masterPane, masterArgs(opts.masterKind, opts.masterModel, self, inboxWatch)...); err != nil {
		return fmt.Errorf("herdr agent start master: %w", explainStart(err, masterDir))
	}

	if err := herdr.AgentPrompt(masterName, masterBrief); err != nil {
		return fmt.Errorf("herdr agent prompt master: %w", err)
	}
	if err := herdr.WorkspaceFocus(workspaceID); err != nil {
		return err
	}

	plan := provisionPlan{Repo: repo, MasterPane: masterPane, Stamp: stamp, Stacks: stacks, MaxStacks: maxStacks, Profile: opts.profile, Workers: workers, Inbox: inbox, SwitchAllowed: switchCommand != ""}
	pool := poolState{Plan: plan, MinWorkers: opts.minWorkers, IdleCloseMinutes: opts.idleCloseMinutes}
	if err := writeJSON(names.PoolFile(repo), pool); err != nil {
		return err
	}
	// A run that ended without acw stop left its queue behind.
	if err := writeJSON(names.QueueFile(repo), taskQueue{}); err != nil {
		return err
	}
	// The watcher is what opens the workers: without it there are none.
	watch := watchPlan{Repo: repo, MasterName: masterName, Inbox: inbox, InboxNext: inboxNext,
		SilenceMinutes: opts.silenceMinutes, PRWatchRepo: prWatchRepo, Stamp: stamp}
	if err := launchBackgroundWatch(watch); err != nil {
		return fmt.Errorf("starting acw's watcher, which opens the workers: %w", err)
	}

	success = true
	fmt.Fprintf(out, "→ master (%s) ready, you can talk to it now. Up to %d worker(s) (%s), %d opened at once, the others as tasks are queued.\n",
		brief.DescribeAgent(opts.masterKind, opts.masterModel), len(workers), describeWorkers(workers), opts.minWorkers)

	// Replace this process with the Herdr TUI, attaching to the workspace just built.
	return syscall.Exec(mustLookPath("herdr"), []string{"herdr"}, os.Environ())
}

// switchCommandFor is the wtm switch the brief hands the workers: with
// the swarm's profile, which wtm does not remember, or a worker's first
// switch would bring its stack back up whole. The workers' permission,
// Bash(wtm switch:*), covers it.
func switchCommandFor(profile string) string {
	if profile == "" {
		return "wtm switch"
	}
	return "wtm switch --profile " + shellWord(profile)
}

// readCustomBrief returns the custom template's source, "" when path is.
func readCustomBrief(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading custom brief %s: %w", path, err)
	}
	return string(source), nil
}

// briefSource is the template the master's brief is built from: the custom
// one, else the built-in one, with extra appended when there is one. ""
// means the built-in one untouched.
func briefSource(custom, extra string) string {
	if extra == "" {
		return custom
	}
	if custom == "" {
		custom = brief.MasterSource()
	}
	return strings.TrimRight(custom, "\n") + "\n\n" + extra
}

// buildBrief uses the custom template source if non-empty, the built-in
// one otherwise.
func buildBrief(source string, p brief.Params) (string, error) {
	if source == "" {
		return brief.Build(p), nil
	}
	return brief.BuildFromSource(source, p)
}

// readNotes returns the per-project notes file's content, or "" when none
// is configured. A relative path is read from repo, so a team can commit
// the file and each member point their config at it. A missing file only
// warns: the swarm is still useful without the notes, and the user sees
// at once which path is wrong.
func readNotes(out io.Writer, repo, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(out, "⚠ project notes unreadable, starting without them: %v\n", err)
		return ""
	}
	return string(content)
}

// modelArgs is the `--model X` forwarded to an agent's own CLI, or nothing
// when model is empty — the escape hatch for a CLI that has no --model.
func modelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", model}
}

// launchBackgroundWatch starts acw's watcher (see runWatch) as a detached
// copy of this same binary, so it keeps running, and opening workers,
// after this process execs into the Herdr TUI.
func launchBackgroundWatch(plan watchPlan) error {
	return launchDetached(fmt.Sprintf("acw-watch-%s.log", plan.Stamp), watchUse, plan)
}

// launchDetached starts this same binary as `<use> <plan as JSON>` in its
// own session, logging to logName in the temp dir, and does not wait: it
// must keep running after this process execs into herdr.
func launchDetached(logName, use string, plan any) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	logFile, err := os.Create(filepath.Join(os.TempDir(), logName))
	if err != nil {
		return err
	}

	cmd := exec.Command(self, use, string(planJSON))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// resolvePRWatch is the GitHub repo pr-watch follows, checked before
// anything is started: gh logged in, origin on GitHub.
func resolvePRWatch(repo string) (string, error) {
	if err := preflight.CheckPRWatch(); err != nil {
		return "", err
	}
	remote, err := gitutil.OriginURL(repo)
	if err != nil {
		return "", fmt.Errorf("pr-watch: %w", err)
	}
	gh, err := githubRepo(remote)
	if err != nil {
		return "", fmt.Errorf("pr-watch: %w", err)
	}
	return gh, nil
}

func mustLookPath(bin string) string {
	p, err := exec.LookPath(bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s not found in PATH: %v\n", bin, err)
		os.Exit(1)
	}
	return p
}
