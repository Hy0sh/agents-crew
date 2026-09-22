package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Hy0sh/agents-crew/internal/brief"
	"github.com/Hy0sh/agents-crew/internal/herdr"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/preflight"
)

const label = "acw"

// runStart creates the master, hands it its brief, backgrounds worker
// provisioning, then execs into the Herdr TUI so the caller can start
// talking to the master immediately.
func runStart(out io.Writer, opts *startOptions) error {
	maxStacks := opts.maxStacks
	if maxStacks <= 0 {
		maxStacks = opts.workers
	}
	if maxStacks > opts.workers {
		maxStacks = opts.workers
	}

	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	slug := names.Slug(repo)
	masterName := names.Master(slug)

	// Here rather than next to the other preflight warnings in main.go: it
	// needs the repo path, which is only resolved at this point.
	preflight.WarnIfAgentsFileMissing(repo, opts.workerKind, func(format string, a ...any) { fmt.Fprintf(out, format, a...) })

	// Scoped to this directory, not global: two different repos each get
	// their own master/worker names (see internal/names), and a swarm
	// already running for a DIFFERENT repo never blocks this one — only an
	// agent whose own pane cwd is exactly this repo does.
	agents, err := herdr.AgentList()
	if err != nil {
		return fmt.Errorf("herdr agent list: %w", err)
	}
	for _, a := range agents {
		if a.Cwd == repo && strings.HasPrefix(a.Name, "master-") {
			return fmt.Errorf("un master tourne déjà dans le workspace %s pour ce répertoire. Attache-toi-y (herdr workspace focus %s) "+
				"au lieu d'en relancer un — ou ferme-le d'abord (acw stop)", a.WorkspaceID, a.WorkspaceID)
		}
	}

	if err := os.MkdirAll(filepath.Join(repo, ".claude", "worktrees", ".acw-status"), 0o755); err != nil {
		return err
	}

	workspaceID, masterPane, err := herdr.WorkspaceCreate(repo, label, true)
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

	masterBrief, err := buildBrief(opts.briefPath, repo, slug, opts.workerKind, opts.workers, maxStacks)
	if err != nil {
		return err
	}
	if err := herdr.AgentPrompt(masterName, masterBrief); err != nil {
		return fmt.Errorf("herdr agent prompt master: %w", err)
	}
	if err := herdr.WorkspaceFocus(workspaceID); err != nil {
		return err
	}

	stamp := time.Now().Format("20060102150405")
	if err := launchBackgroundProvisioning(repo, masterPane, stamp, opts.workers, maxStacks, opts.workerModel, opts.workerKind); err != nil {
		return fmt.Errorf("lancement du provisioning des workers: %w", err)
	}

	success = true
	fmt.Fprintf(out, "→ master (%s) prêt, tu peux déjà lui parler. %d worker(s) (%s) en provisionnement en tâche de fond.\n",
		describeAgent(opts.masterKind, opts.masterModel), opts.workers, describeAgent(opts.workerKind, opts.workerModel))

	// Replace this process with the Herdr TUI, attaching to the workspace just built.
	return syscall.Exec(mustLookPath("herdr"), []string{"herdr"}, os.Environ())
}

// buildBrief uses a custom template file if path is non-empty, the
// built-in one otherwise.
func buildBrief(path, repo, slug, workerKind string, n, maxStacks int) (string, error) {
	if path == "" {
		return brief.Build(repo, slug, workerKind, n, maxStacks), nil
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("lecture du brief personnalisé %s: %w", path, err)
	}
	return brief.BuildFromSource(string(source), repo, slug, workerKind, n, maxStacks)
}

// modelArgs is the `--model X` forwarded to an agent's own CLI, or nothing
// when model is empty — the escape hatch for a CLI that has no --model.
func modelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", model}
}

// describeAgent names an agent the way the launch line reports it: the
// kind alone when no model was asked for.
func describeAgent(kind, model string) string {
	if model == "" {
		return kind
	}
	return kind + " " + model
}

// launchBackgroundProvisioning starts a detached copy of this same binary
// in provisioning mode, so worker setup (worktrees, environments, panes,
// agents) continues after this process execs into the Herdr TUI.
func launchBackgroundProvisioning(repo, masterPane, stamp string, n, maxStacks int, workerModel, workerKind string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(os.TempDir(), fmt.Sprintf("acw-workers-%s.log", stamp))
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}

	cmd := exec.Command(self, provisionUse, repo, masterPane, stamp, strconv.Itoa(n), strconv.Itoa(maxStacks), workerModel, workerKind)
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
