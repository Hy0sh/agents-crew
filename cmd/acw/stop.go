package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Hy0sh/agents-crew/internal/board"
	"github.com/Hy0sh/agents-crew/internal/names"
	"github.com/Hy0sh/agents-crew/internal/teardown"
)

// stopHere is acw stop: the swarm of the current directory, its tasks
// kept for the next master.
func stopHere(out io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := saveInterrupted(cwd, out); err != nil {
		return err
	}
	if err := teardown.Run(); err != nil {
		return err
	}
	record("stop", func(b *board.DB) error { return b.KeepWorkers(cwd, nil) })
	return nil
}

// stopEvery is acw stop --all: every repo whose watcher ever reported to
// the board and that still has a status dir, the mark of a swarm acw
// stop has not torn down. One that fails is said, and the others are
// still stopped.
func stopEvery(out io.Writer) error {
	var repos []string
	if err := withBoard(func(b *board.DB) (err error) { repos, err = b.Watched(); return err }); err != nil {
		return fmt.Errorf("the board lists the swarms: %w", err)
	}
	back, err := os.Getwd()
	if err != nil {
		return err
	}
	// ponytail: teardown works on the current directory, so each stop
	// moves into its repo; one at a time, never concurrently.
	defer os.Chdir(back)
	var errs []error
	stopped := 0
	for _, repo := range repos {
		if _, err := os.Stat(names.StatusDir(repo)); err != nil {
			continue
		}
		stopped++
		fmt.Fprintf(out, "== %s\n", repo)
		err := os.Chdir(repo)
		if err == nil {
			err = stopHere(out)
		}
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", repo, err)
			errs = append(errs, fmt.Errorf("%s: %w", repo, err))
		}
	}
	if stopped == 0 {
		fmt.Fprintln(out, "No acw swarm running.")
	}
	return errors.Join(errs...)
}
