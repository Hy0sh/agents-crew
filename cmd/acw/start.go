package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Hy0sh/agents-crew/internal/brief"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/preflight"
)

// runStart creates the master, hands it its brief, backgrounds worker
// provisioning, then execs into the Herdr TUI so the caller can start
// talking to the master immediately.
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
		return fmt.Errorf("un master tourne déjà dans le workspace %s pour ce répertoire. Attache-toi-y (herdr workspace focus %s) "+
			"au lieu d'en relancer un — ou ferme-le d'abord (acw stop)", a.WorkspaceID, a.WorkspaceID)
	}

	// Before anything is created: a custom brief with a typo'd variable
	// must fail here, not after a workspace and a master agent were started
	// for nothing.
	customBrief, err := readCustomBrief(opts.briefPath)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	inbox := names.Inbox(repo)
	inboxWatch, warn := inboxWatchCommand(opts.masterKind, customBrief, self, inbox)
	if warn {
		fmt.Fprintln(out, "⚠ le brief personnalisé ne contient pas {{.InboxWatch}} : les pings des workers seront tapés dans la saisie du master, comme avant")
	}
	if inboxWatch == "" {
		inbox = ""
	}
	var inboxNext string
	if inbox != "" {
		inboxNext = inboxNextCommand(self, inbox)
	}
	masterBrief, err := buildBrief(customBrief, brief.Params{
		RepoPath:       repo,
		Slug:           slug,
		MaxStacks:      maxStacks,
		Profile:        opts.profile,
		Notes:          readNotes(out, repo, opts.notesPath),
		Workers:        briefWorkers(workers),
		InboxWatch:     inboxWatch,
		InboxNext:      inboxNext,
		SilenceMinutes: opts.silenceMinutes,
		StatusCommand:  shellWord(self) + " status --repo " + shellWord(repo),
		ClearCommand:   shellWord(self) + " clear --repo " + shellWord(repo),
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		return err
	}
	if err := writeRunInfo(repo, runInfo{Profile: opts.profile}); err != nil {
		return err
	}

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
	if err := herdr.AgentStart(masterName, opts.masterKind, masterPane, masterArgs(opts.masterModel, self, inboxWatch)...); err != nil {
		return fmt.Errorf("herdr agent start master: %w", explainStart(err, masterDir))
	}

	if err := herdr.AgentPrompt(masterName, masterBrief); err != nil {
		return fmt.Errorf("herdr agent prompt master: %w", err)
	}
	if err := herdr.WorkspaceFocus(workspaceID); err != nil {
		return err
	}

	stamp := time.Now().Format("20060102150405")
	plan := provisionPlan{Repo: repo, MasterPane: masterPane, Stamp: stamp, MaxStacks: maxStacks, Profile: opts.profile, Workers: workers, Inbox: inbox}
	if err := launchBackgroundProvisioning(plan); err != nil {
		return fmt.Errorf("lancement du provisioning des workers: %w", err)
	}
	// Not fatal: without it the swarm still runs, the master just hears
	// less. Said, so the user knows why.
	watch := watchPlanFor(repo, masterName, inbox, inboxNext, opts.silenceMinutes, workers)
	watch.Stamp = stamp
	err = os.WriteFile(filepath.Join(names.StatusDir(repo), "stamp"), []byte(stamp+"\n"), 0o644)
	if err == nil {
		err = launchBackgroundWatch(watch, stamp)
	}
	if err != nil {
		fmt.Fprintf(out, "⚠ veilleur acw non lancé, le master ne sera pas prévenu des blocages ni des silences : %v\n", err)
	}

	success = true
	fmt.Fprintf(out, "→ master (%s) prêt, tu peux déjà lui parler. %d worker(s) (%s) en provisionnement en tâche de fond.\n",
		brief.DescribeAgent(opts.masterKind, opts.masterModel), len(workers), describeWorkers(workers))

	// Replace this process with the Herdr TUI, attaching to the workspace just built.
	return syscall.Exec(mustLookPath("herdr"), []string{"herdr"}, os.Environ())
}

// runInfo is what a swarm was started with that acw resume needs again:
// the profile the config gave at launch, which a later config change or
// another --preset must not silently replace.
type runInfo struct {
	Profile string `json:"profile"`
}

func writeRunInfo(repo string, info runInfo) error {
	content, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(names.RunFile(repo), content, 0o644)
}

func readRunInfo(repo string) (runInfo, error) {
	var info runInfo
	content, err := os.ReadFile(names.RunFile(repo))
	if err != nil {
		return info, err
	}
	return info, json.Unmarshal(content, &info)
}

// readCustomBrief returns the custom template's source, "" when path is.
func readCustomBrief(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("lecture du brief personnalisé %s: %w", path, err)
	}
	return string(source), nil
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
		fmt.Fprintf(out, "⚠ notes du projet illisibles, lancement sans elles: %v\n", err)
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

// launchBackgroundProvisioning starts a detached copy of this same binary
// in provisioning mode, so worker setup (worktrees, environments, panes,
// agents) continues after this process execs into the Herdr TUI.
func launchBackgroundProvisioning(plan provisionPlan) error {
	return launchDetached(fmt.Sprintf("acw-workers-%s.log", plan.Stamp), provisionUse, plan)
}

// launchBackgroundWatch starts acw's watcher (see runWatch) the same way.
func launchBackgroundWatch(plan watchPlan, stamp string) error {
	return launchDetached(fmt.Sprintf("acw-watch-%s.log", stamp), watchUse, plan)
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

// watchPlanFor lists the workers the watcher follows, named as
// provisioning starts them. Only claude workers have the Stop hook.
func watchPlanFor(repo, masterName, inbox, inboxNext string, silenceMinutes int, workers []workerSpec) watchPlan {
	plan := watchPlan{Repo: repo, MasterName: masterName, Inbox: inbox, InboxNext: inboxNext, SilenceMinutes: silenceMinutes}
	slug := names.Slug(repo)
	for i, w := range workers {
		plan.Workers = append(plan.Workers, watchedWorker{
			Name:   names.Worker(slug, i+1),
			Label:  fmt.Sprintf("worker%d", i+1),
			Hooked: w.Kind == "claude",
		})
	}
	return plan
}

func mustLookPath(bin string) string {
	p, err := exec.LookPath(bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s introuvable dans le PATH: %v\n", bin, err)
		os.Exit(1)
	}
	return p
}
