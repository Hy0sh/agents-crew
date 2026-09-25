// Package wtm wraps the parts of the `wtm` CLI (worktree-manager) that
// agents-crew needs: giving a freshly created worktree a Docker stack,
// and tearing that stack down again. wtm itself decides whether a project
// is registered; a missing binary or an unregistered project is not an
// error here, callers just get a stack-less worktree.
package wtm

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
)

// Available reports whether the wtm binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("wtm")
	return err == nil
}

func run(dir string, args ...string) error {
	cmd := exec.Command("wtm", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wtm %v (in %s): %w: %s", args, dir, err, stderr.String())
	}
	return nil
}

// Adopt gives the worktree at dir a Docker stack, restoring the
// pre-migrated database dump. dir must already be on the branch to adopt.
// profile names one of the project's wtm profiles; empty starts the whole
// stack.
func Adopt(dir, profile string) error {
	return run(dir, adoptArgs(profile)...)
}

func adoptArgs(profile string) []string {
	args := []string{"adopt", "-y"}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	return args
}

// Start starts the stack of a worktree whose stack was stopped, on
// profile ("" for the whole stack). wtm's own output goes to out as it
// comes: without a terminal wtm asks nothing and starts even when memory
// is tight, so its warning is the only thing telling the user so.
func Start(dir, branch, profile string, out io.Writer) error {
	cmd := exec.Command("wtm", startArgs(branch, profile)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, io.MultiWriter(out, &stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wtm %v (in %s): %w: %s", startArgs(branch, profile), dir, err, stderr.String())
	}
	return nil
}

func startArgs(branch, profile string) []string {
	args := []string{"start", branch}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	return args
}

// Stop stops the worktree's stack without removing it. Unlike Adopt, wtm
// does not infer the branch from dir for `stop` — it must be given
// explicitly (verified against `wtm stop --help`: "Usage: wtm stop
// [project] <branch>").
func Stop(dir, branch string) error {
	return run(dir, "stop", branch)
}

// Remove stops the stack and removes the worktree (its branch is kept).
// Same requirement as Stop: the branch is a mandatory argument, not
// inferred from dir.
func Remove(dir, branch string) error {
	return run(dir, "remove", branch)
}
