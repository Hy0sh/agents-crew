package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Hy0sh/agents-crew/internal/brief"
	"github.com/Hy0sh/agents-crew/internal/claudecode"
	"github.com/Hy0sh/agents-crew/internal/harness"
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

	kinds := distinctKinds(workers)
	for _, kind := range kinds {
		preflight.WarnIfAgentsFileMissing(repo, kind, func(format string, a ...any) { fmt.Fprintf(out, format, a...) })
	}
	if harness.Of(opts.masterKind).ShowsUntrackedDiffs || slices.ContainsFunc(kinds, func(k string) bool { return harness.Of(k).ShowsUntrackedDiffs }) {
		if err := claudecode.ExcludeWorktrees(repo); err != nil {
			fmt.Fprintf(out, "⚠ .claude/worktrees/ not excluded from git, Claude Code will show its state files changing: %v\n", err)
		}
	}

	// Scoped to this directory, not global: two different repos each get
	// their own master/worker names (see internal/names), and a swarm
	// already running for a DIFFERENT repo never blocks this one. Found by
	// name, not by its pane's cwd: with master-dir it runs elsewhere.
	if claudecode.IsDesktop(names.StatusDir(repo)) {
		return errors.New("a swarm is already running for this directory, its master in Claude Desktop: go on there, or stop it first (acw stop)")
	}
	if opts.masterApp != harness.DesktopApp {
		agents, err := herdr.AgentList()
		if err != nil {
			return fmt.Errorf("herdr agent list: %w", err)
		}
		if a, ok := herdr.FindAgent(agents, masterName); ok {
			return fmt.Errorf("a master is already running in workspace %s for this directory. Attach to it (herdr workspace focus %s) "+
				"instead of starting another one, or close it first (acw stop)", a.WorkspaceID, a.WorkspaceID)
		}
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
	kindMethods, kindsWhen, err := readKinds(repo, opts.kinds, workers, out)
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
		switchCommand = workerSwitch
	}
	masterBrief, err := buildBrief(briefSource(customBrief, extra), brief.Params{
		RepoPath:         repo,
		Slug:             slug,
		Stacks:           stacks,
		MaxStacks:        maxStacks,
		MinWorkers:       minOpen(workers),
		IdleCloseMinutes: opts.idleCloseMinutes,
		Profile:          opts.profile,
		Notes:            notes,
		Workers:          briefWorkers(workers),
		Kinds:            kindsWhen,
		InboxWatch:       inboxWatch,
		InboxNext:        inboxNext,
		SilenceMinutes:   opts.silenceMinutes,
		StatusCommand:    shellWord(self) + " status --repo " + shellWord(repo),
		QueueCommand:     shellWord(self) + " queue --repo " + shellWord(repo),
		DoneCommand:      shellWord(self) + " done --repo " + shellWord(repo),
		CloseCommand:     shellWord(self) + " close --repo " + shellWord(repo),
		TellCommand:      shellWord(self) + " tell --repo " + shellWord(repo),
		BriefsDir:        claudecode.BriefsDir(names.StatusDir(repo)),
		DecisionCommand:  shellWord(self) + " board decision --repo " + shellWord(repo),
		ParkCommand:      shellWord(self) + " board park --repo " + shellWord(repo),
		ParkedCommand:    shellWord(self) + " board parked --repo " + shellWord(repo),
		ResumeCommand:    shellWord(self) + " board resume",
		ReassignCommand:  shellWord(self) + " board edit",
		SwitchCommand:    switchCommand,
		PRWatch:          prWatchRepo != "",
	})
	if err != nil {
		return err
	}

	// Briefs, system prompts and the master's inbox: the user's only.
	if err := os.MkdirAll(claudecode.BriefsDir(names.StatusDir(repo)), 0o700); err != nil {
		return err
	}
	stackCmd := ""
	if stacks {
		stackCmd = shellWord(self) + " stack"
	}
	if err := writeSystemPrompts(names.StatusDir(repo), notes, stackCmd, workers); err != nil {
		return err
	}
	stamp := time.Now().Format("20060102150405")

	masterDir := repo
	if opts.masterDir != "" {
		masterDir = opts.masterDir
	}
	// A master in Claude Desktop runs its workers as background sessions:
	// no herdr workspace at all.
	desktop := opts.masterApp == harness.DesktopApp
	success := false
	var workspaceID, masterPane string
	if !desktop {
		if workspaceID, masterPane, err = herdr.WorkspaceCreate(masterDir, names.Label(repo), true); err != nil {
			return fmt.Errorf("herdr workspace create: %w", err)
		}
		// From here on, clean up the half-built workspace on any failure.
		defer func() {
			if !success {
				_ = herdr.WorkspaceClose(workspaceID, true)
			}
		}()
	}

	// A claude master gets its brief as a system prompt (see masterArgs),
	// or reads it from this file in Claude Desktop, and only a kickoff as
	// its first prompt.
	firstPrompt := masterBrief
	briefPath := claudecode.BriefFile(names.StatusDir(repo))
	if harness.Of(opts.masterKind).SystemPrompt {
		if err := os.WriteFile(briefPath, []byte(masterBrief+"\n"), 0o644); err != nil {
			return err
		}
		firstPrompt = masterKickoff
	}
	previous, delivered := startPrompt(repo)
	if previous != "" {
		firstPrompt += "\n\n" + previous
	}
	if !desktop {
		if err := herdr.PaneRename(masterPane, "master"); err != nil {
			return err
		}
		if err := herdr.AgentStart(masterName, opts.masterKind, masterPane, masterArgs(opts.masterKind, opts.masterModel, self, inboxWatch, names.StatusDir(repo), opts.masterAutocompact)...); err != nil {
			return fmt.Errorf("herdr agent start master: %w", explainStart(err, masterDir))
		}
		if err := herdr.AgentPrompt(masterName, firstPrompt); err != nil {
			return fmt.Errorf("herdr agent prompt master: %w", err)
		}
		delivered()
		if err := herdr.WorkspaceFocus(workspaceID); err != nil {
			return err
		}
	}

	plan := provisionPlan{Repo: repo, MasterPane: masterPane, Stamp: stamp, Stacks: stacks, MaxStacks: maxStacks, Profile: opts.profile, Workers: workers, Inbox: inbox, SwitchAllowed: switchCommand != "", PRWatch: prWatchRepo != "", Kinds: kindMethods}
	if desktop {
		plan.Engine = bgEngine
	}
	pool := poolState{Plan: plan, IdleCloseMinutes: opts.idleCloseMinutes}
	if err := writeJSON(names.PoolFile(repo), pool); err != nil {
		return err
	}
	// A run that ended without acw stop left its queue behind.
	if err := writeJSON(names.QueueFile(repo), taskQueue{}); err != nil {
		return err
	}
	// The watcher is what opens the workers: without it there are none.
	watch := watchPlan{Repo: repo, MasterName: masterName, MasterApp: opts.masterApp, Inbox: inbox, InboxNext: inboxNext,
		SilenceMinutes: opts.silenceMinutes, PRWatchRepo: prWatchRepo, Stamp: stamp}
	if err := launchBackgroundWatch(watch); err != nil {
		return fmt.Errorf("starting acw's watcher, which opens the workers: %w", err)
	}
	// Not fatal: the swarm runs without it, acw board opens it by hand.
	var boardURL string
	if opts.board {
		boardURL = startBoard(out, repo, stamp, desktop)
	}

	if desktop {
		d := claudecode.Desktop{Dir: masterDir, Session: claudecode.NewSessionID(),
			Hook:  shellWord(self) + " " + desktopHookUse + " " + shellWord(names.StatusDir(repo)),
			Board: boardURL, InboxNext: inboxNext}
		// Its name is its address for the message that starts it: the
		// start time keeps it apart from an earlier run's, still listed in
		// Claude Desktop after acw stop.
		name := "acw master " + names.Label(repo) + " " + time.Now().Format("15:04")
		if err := claudecode.CreateSession(masterDir, d.Session, name, opts.masterModel); err != nil {
			return fmt.Errorf("creating the master's session: %w", err)
		}
		if err := claudecode.InstallDesktop(names.StatusDir(repo), d, append(masterAllow(self, inboxWatch), claudecode.WriteRule(claudecode.BriefsDir(names.StatusDir(repo)))), firstPrompt); err != nil {
			return fmt.Errorf("installing the master's settings for Claude Desktop: %w", err)
		}
		delivered()
		success = true
		if err := claudecode.OpenDesktop(masterDir, d.Session); err != nil {
			return err
		}
		fmt.Fprintln(out, "→ waiting for Claude Desktop to start the master...")
		if err := claudecode.WakeSession(masterDir, d.Session, name, 2*time.Minute); err != nil {
			fmt.Fprintf(out, "⚠ the master was not started for you (%v): write to it in Claude Desktop to start it\n", err)
		}
		fmt.Fprintf(out, "→ master (%s) running in Claude Desktop. Up to %d worker(s) (%s), %d opened at once, the others as tasks are queued.\n",
			brief.DescribeAgent(opts.masterKind, opts.masterModel), len(workers), describeWorkers(workers), minOpen(workers))
		return nil
	}

	success = true
	fmt.Fprintf(out, "→ master (%s) ready, you can talk to it now. Up to %d worker(s) (%s), %d opened at once, the others as tasks are queued.\n",
		brief.DescribeAgent(opts.masterKind, opts.masterModel), len(workers), describeWorkers(workers), minOpen(workers))

	// Replace this process with the Herdr TUI, attaching to the workspace just built.
	return syscall.Exec(mustLookPath("herdr"), []string{"herdr"}, os.Environ())
}

// startBoard starts acw board for the swarm, or joins the one another
// swarm opened, and returns its URL when it knows it. A master in Claude
// Desktop opens it in its Browser pane, so it needs the URL now: the
// board gets a free port picked here; a browser tab is opened otherwise.
func startBoard(out io.Writer, repo, stamp string, desktop bool) string {
	// One board for every swarm: it shows them all already.
	if url := sharedBoard(); url != "" {
		if err := appendLine(boardSwarms(), repo); err != nil {
			fmt.Fprintf(out, "⚠ acw board at %s not joined: %v\n", url, err)
			return ""
		}
		if !desktop {
			openBrowser(url)
		}
		fmt.Fprintf(out, "→ acw board already open at %s, it now stays open for this swarm too\n", url)
		return url
	}
	args := []string{"--until-stop", repo}
	var url string
	if desktop {
		port, err := freePort()
		if err != nil {
			fmt.Fprintf(out, "⚠ acw board not started: %v\n", err)
			return ""
		}
		args = append(args, "--port", strconv.Itoa(port), "--no-browser")
		url = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	boardLog := fmt.Sprintf("acw-board-%s.log", stamp)
	if err := launchDetached(boardLog, "board", args...); err != nil {
		fmt.Fprintf(out, "⚠ acw board not started: %v\n", err)
		return ""
	}
	if url == "" {
		fmt.Fprintf(out, "→ acw board opening in the browser, its URL in %s\n", filepath.Join(os.TempDir(), boardLog))
	}
	return url
}

// freePort is a port nothing listens on, at the time of asking.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// workerSwitch is the wtm switch the brief hands the workers: it starts
// no stack, a worker takes one with acw stack up when it needs it. The
// workers' permission, Bash(wtm switch:*), covers it.
const workerSwitch = "wtm switch --no-start"

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
// after this process execs into the Herdr TUI. The plan goes through a
// file in the status dir, kept for acw watch: on the command line, its
// commands (the master's __inbox-next among them) made the watcher a
// match for a `pkill -f` aimed at them, which killed it without a word.
func launchBackgroundWatch(plan watchPlan) error {
	if err := writeJSON(names.WatchPlan(plan.Repo), plan); err != nil {
		return err
	}
	return launchDetached(fmt.Sprintf("acw-watch-%s.log", plan.Stamp), watchUse, names.WatchPlan(plan.Repo))
}

// launchDetached starts this same binary as `<use> <args>` in its own
// session, logging to logName in the temp dir, appended to: a watcher
// started again keeps the log of the one before. It does not wait: it
// must keep running after this process execs into herdr.
func launchDetached(logName, use string, args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// Its name is predictable: a symlink planted in a shared temp dir must
	// not be followed, and its briefs are nobody else's to read.
	logFile, err := os.OpenFile(filepath.Join(os.TempDir(), logName), os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}

	cmd := exec.Command(self, append([]string{use}, args...)...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// resolvePRWatch is the GitHub repo pr-watch follows, "host/owner/name",
// as gh resolves it, checked before anything is started.
func resolvePRWatch(repo string) (string, error) {
	if err := preflight.CheckPRWatch(); err != nil {
		return "", err
	}
	cmd := exec.Command("gh", "repo", "view", "--json", "url")
	cmd.Dir = repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pr-watch: gh finds no GitHub repo here: %s\nLog gh in to the repo's host (gh auth login --hostname <host>), or turn pr-watch off for this repo (acw project edit --pr-watch=false)", strings.TrimSpace(stderr.String()))
	}
	gh, err := parseGHRepo(out)
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
