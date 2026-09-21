// agents-crew launches a dedicated Herdr workspace: 1 master (opus) + N
// workers (sonnet) to dispatch and supervise tasks in the current
// directory's repo, project-agnostic.
//
// Usage: agents-crew [N] [MAX_STACKS]
//
//	N          number of workers (default 3)
//	MAX_STACKS number of concurrent isolated environments allowed
//	           (default N; capped to N)
//
// agents-crew stop tears the whole thing down (see internal/teardown).
//
// The master is created and briefed synchronously so you can start talking
// to it immediately; workers are provisioned (worktree + environment) in a
// detached background process so that setup never delays opening the
// terminal.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"agents-crew/internal/brief"
	"agents-crew/internal/herdr"
	"agents-crew/internal/teardown"
)

const (
	label         = "agents-crew"
	provisionFlag = "__provision-workers"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == provisionFlag {
		provisionWorkers(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "stop" {
		if err := teardown.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func start() error {
	n, err := parseCount(argOr(1, "3"), "N")
	if err != nil {
		return usageErr(err)
	}
	maxStacks, err := parseCount(argOr(2, strconv.Itoa(n)), "MAX_STACKS")
	if err != nil {
		return usageErr(err)
	}
	if maxStacks > n {
		maxStacks = n
	}

	repo, err := os.Getwd()
	if err != nil {
		return err
	}

	agents, err := herdr.AgentList()
	if err != nil {
		return fmt.Errorf("herdr agent list: %w", err)
	}
	for _, a := range agents {
		if a.Name == "master" {
			return fmt.Errorf("un master tourne déjà dans le workspace %s. Attache-toi-y (herdr workspace focus %s) "+
				"au lieu d'en relancer un — ou ferme-le d'abord (herdr workspace close %s --group)", a.WorkspaceID, a.WorkspaceID, a.WorkspaceID)
		}
	}

	if err := os.MkdirAll(filepath.Join(repo, ".claude", "worktrees", ".agents-crew-status"), 0o755); err != nil {
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
	if err := herdr.AgentStart("master", masterPane, "--model", "opus"); err != nil {
		return fmt.Errorf("herdr agent start master: %w", err)
	}

	masterBrief := brief.Build(repo, n, maxStacks)
	if err := herdr.AgentPrompt("master", masterBrief); err != nil {
		return fmt.Errorf("herdr agent prompt master: %w", err)
	}
	if err := herdr.WorkspaceFocus(workspaceID); err != nil {
		return err
	}

	stamp := time.Now().Format("20060102150405")
	if err := launchBackgroundProvisioning(repo, masterPane, stamp, n, maxStacks); err != nil {
		return fmt.Errorf("lancement du provisioning des workers: %w", err)
	}

	success = true
	fmt.Fprintf(os.Stderr, "→ master (opus) prêt, tu peux déjà lui parler. %d worker(s) (sonnet) en provisionnement en tâche de fond.\n", n)

	// Replace this process with the Herdr TUI, attaching to the workspace just built.
	return syscall.Exec(mustLookPath("herdr"), []string{"herdr"}, os.Environ())
}

// launchBackgroundProvisioning starts a detached copy of this same binary
// in provisioning mode, so worker setup (worktrees, environments, panes,
// agents) continues after this process execs into the Herdr TUI.
func launchBackgroundProvisioning(repo, masterPane, stamp string, n, maxStacks int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(os.TempDir(), fmt.Sprintf("agents-crew-workers-%s.log", stamp))
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}

	cmd := exec.Command(self, provisionFlag, repo, masterPane, stamp, strconv.Itoa(n), strconv.Itoa(maxStacks))
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

func argOr(i int, def string) string {
	if i < len(os.Args) {
		return os.Args[i]
	}
	return def
}

func parseCount(s, name string) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%s doit être un entier > 0, reçu %q", name, s)
	}
	return v, nil
}

func usageErr(err error) error {
	return fmt.Errorf("%w\nUsage: agents-crew [N] [MAX_STACKS]", err)
}
