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
	maxStacks := opts.maxStacks
	if maxStacks <= 0 {
		maxStacks = opts.workers
	}
	if maxStacks > opts.workers {
		maxStacks = opts.workers
	}

	slug := names.Slug(repo)
	masterName := names.Master(slug)

	for _, kind := range distinctKinds(workers) {
		preflight.WarnIfAgentsFileMissing(repo, kind, func(format string, a ...any) { fmt.Fprintf(out, format, a...) })
	}

	// Scoped to this directory, not global: two different repos each get
	// their own master/worker names (see internal/names), and a swarm
	// already running for a DIFFERENT repo never blocks this one — only an
	// agent whose own pane cwd is exactly this repo does.
	agents, err := herdr.AgentList()
	if err != nil {
		return fmt.Errorf("herdr agent list: %w", err)
	}
	for _, a := range agents {
		if a.Cwd == repo && names.IsMaster(a.Name) {
			return fmt.Errorf("un master tourne déjà dans le workspace %s pour ce répertoire. Attache-toi-y (herdr workspace focus %s) "+
				"au lieu d'en relancer un — ou ferme-le d'abord (acw stop)", a.WorkspaceID, a.WorkspaceID)
		}
	}

	// Before anything is created: a custom brief with a typo'd variable
	// must fail here, not after a workspace and a master agent were started
	// for nothing.
	masterBrief, err := buildBrief(opts.briefPath, brief.Params{
		RepoPath:  repo,
		Slug:      slug,
		MaxStacks: maxStacks,
		Profile:   opts.profile,
		Notes:     readNotes(out, repo, opts.notesPath),
		Workers:   briefWorkers(workers),
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(names.StatusDir(repo), 0o755); err != nil {
		return err
	}

	workspaceID, masterPane, err := herdr.WorkspaceCreate(repo, names.Label(repo), true)
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
	if err := herdr.AgentStart(masterName, opts.masterKind, masterPane, modelArgs(opts.masterModel)...); err != nil {
		return fmt.Errorf("herdr agent start master: %w", err)
	}

	if err := herdr.AgentPrompt(masterName, masterBrief); err != nil {
		return fmt.Errorf("herdr agent prompt master: %w", err)
	}
	if err := herdr.WorkspaceFocus(workspaceID); err != nil {
		return err
	}

	stamp := time.Now().Format("20060102150405")
	plan := provisionPlan{Repo: repo, MasterPane: masterPane, Stamp: stamp, MaxStacks: maxStacks, Profile: opts.profile, Workers: workers}
	if err := launchBackgroundProvisioning(plan); err != nil {
		return fmt.Errorf("lancement du provisioning des workers: %w", err)
	}

	success = true
	fmt.Fprintf(out, "→ master (%s) prêt, tu peux déjà lui parler. %d worker(s) (%s) en provisionnement en tâche de fond.\n",
		brief.DescribeAgent(opts.masterKind, opts.masterModel), len(workers), describeWorkers(workers))

	// Replace this process with the Herdr TUI, attaching to the workspace just built.
	return syscall.Exec(mustLookPath("herdr"), []string{"herdr"}, os.Environ())
}

// buildBrief uses a custom template file if path is non-empty, the
// built-in one otherwise.
func buildBrief(path string, p brief.Params) (string, error) {
	if path == "" {
		return brief.Build(p), nil
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("lecture du brief personnalisé %s: %w", path, err)
	}
	return brief.BuildFromSource(string(source), p)
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
	self, err := os.Executable()
	if err != nil {
		return err
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	logPath := filepath.Join(os.TempDir(), fmt.Sprintf("acw-workers-%s.log", plan.Stamp))
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}

	cmd := exec.Command(self, provisionUse, string(planJSON))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Intentionally not waited on: it must keep running after this process execs into herdr.
	return nil
}

func mustLookPath(bin string) string {
	p, err := exec.LookPath(bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s introuvable dans le PATH: %v\n", bin, err)
		os.Exit(1)
	}
	return p
}
