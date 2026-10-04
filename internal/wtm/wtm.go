// Package wtm wraps the parts of the `wtm` CLI (worktree-manager) that
// agents-crew needs: giving a freshly created worktree a Docker stack,
// and tearing that stack down again. wtm itself decides whether a project
// is registered; a missing binary or an unregistered project is not an
// error here, callers just get a stack-less worktree.
package wtm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// Available reports whether the wtm binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("wtm")
	return err == nil
}

// ErrNoStack is wtm finding no stack under the branch it was given: a
// worktree it never gave one to (a worker beyond max-stacks, a failed
// adopt), or one that changed branch without wtm switch.
var ErrNoStack = errors.New("no wtm stack for this worktree")

// ErrUnregistered is a project wtm does not know: it reaches none of its
// stacks, whatever the branch.
var ErrUnregistered = errors.New("project not registered in wtm")

func run(dir string, args ...string) error {
	return runTo(dir, io.Discard, args...)
}

// runTo runs wtm with its stdout and stderr copied to out, and recognizes
// ErrNoStack in what wtm says, so no caller has to read its wording.
func runTo(dir string, out io.Writer, args ...string) error {
	cmd := exec.Command("wtm", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, io.MultiWriter(out, &stderr)
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		switch {
		case strings.Contains(msg, "no worktree for branch"):
			err = fmt.Errorf("%w: %w", ErrNoStack, err)
		case strings.Contains(msg, "is not registered"):
			err = fmt.Errorf("%w: %w", ErrUnregistered, err)
		}
		return fmt.Errorf("wtm %v (in %s): %w: %s", args, dir, err, msg)
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
	return runTo(dir, out, startArgs(branch, profile)...)
}

func startArgs(branch, profile string) []string {
	args := []string{"start", branch}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	return args
}

// Doctor returns what `wtm doctor` reports, run from dir.
func Doctor(dir string) (string, error) {
	var out bytes.Buffer
	err := runTo(dir, &out, "doctor")
	return out.String(), err
}

// Stop stops the worktree's stack without removing it. Unlike Adopt, wtm
// does not infer the branch from dir for `stop` — it must be given
// explicitly (verified against `wtm stop --help`: "Usage: wtm stop
// [project] <branch>").
func Stop(dir, branch string) error {
	return run(dir, "stop", branch)
}

// Remove takes the stack down, running or stopped, and releases its index
// and its volumes, so wtm's list holds no stack acw is done with. Same
// requirement as Stop: the branch is a mandatory argument. A worktree wtm
// did not create (acw's) keeps its directory, only wtm's files go, and
// wtm never asks --force for one. A branch whose index no worktree holds
// any more is taken as stale: its stack goes, its index is released.
func Remove(dir, branch string) error {
	return run(dir, "remove", branch)
}

// StrandedHint says why a worktree wtm gave a stack to has none under its
// current branch, and how to get it back: wtm keeps a stack under the
// branch it was made for, and a plain git switch in the worktree left it
// there, running.
func StrandedHint(dir, branch string) string {
	hint := fmt.Sprintf("its stack was not found under its current branch %s: the worktree probably changed branch without wtm switch, "+
		"and its stack still runs under the old one. Run `wtm list` to find the branch it is registered under and `wtm remove <that branch>`", branch)
	if SwitchAvailable() {
		hint += fmt.Sprintf(", or, from %s, `wtm switch %s`, which takes the old stack down with its volumes and starts one on a fresh dump", dir, branch)
	}
	return hint
}

// Registered reports whether dir is a project in wtm's registry: an
// unregistered repo's adopts all fail, so its workers have no stack.
func Registered(dir string) bool {
	out, err := exec.Command("wtm", "project", "list").Output()
	return err == nil && listedProject(string(out), dir)
}

// listedProject reports whether dir is a project's directory in the
// output of `wtm project list` (a table: name, directory, base, dump).
// Paths are compared resolved, as wtm does: on macOS /tmp is /private/tmp,
// and a repo reached through a symlink is still the one registered.
func listedProject(list, dir string) bool {
	for _, line := range strings.Split(list, "\n") {
		if strings.Contains(" "+line+" ", " "+dir+" ") {
			return true
		}
		for _, field := range strings.Fields(line) {
			if filepath.IsAbs(field) && samePath(field, dir) {
				return true
			}
		}
	}
	return false
}

func samePath(a, b string) bool {
	return resolve(a) == resolve(b)
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// SwitchAvailable reports whether this wtm has `switch` (0.26.0 and
// later). Probed rather than read from --version, which a local build
// reports as devel; an older wtm answers "unknown command" with exit 1.
// Callers check Available first: no wtm call at all without wtm.
func SwitchAvailable() bool {
	return exec.Command("wtm", "switch", "--help").Run() == nil
}

// Switch moves the worktree at dir to branch, keeping its ports: on a
// fresh stack for another branch, the same stack restarted for the one
// it is on. from is where a branch that doesn't exist yet is cut from,
// "" for an existing one. wtm never asks anything without a terminal, and
// a failure is resumed by running the same command again.
func Switch(dir, branch, from, profile string, out io.Writer) error {
	return runTo(dir, out, switchArgs(branch, from, profile)...)
}

func switchArgs(branch, from, profile string) []string {
	args := []string{"switch", branch}
	if from != "" {
		args = append(args, "--from", from)
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	return args
}
