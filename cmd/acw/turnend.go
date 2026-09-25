package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"
)

const turnEndUse = "__turn-end"

// normalizeStatus is what a worker's Stop hook runs before pinging the
// master (see stopCommand): it puts in the status file what the worker
// was trusted with and got wrong in practice. updated_at comes from the
// file's own mtime (a worker wrote its local time with a Z), last_turn_end
// from the clock, and blocked_on is cleared once state is no longer
// blocked (one stayed set long after the answer came in).
//
// It only ever touches a JSON object: a missing file, one the worker left
// halfway, or anything else is left exactly as it is. Safe to rewrite
// here because the hook runs once the turn is over, when the worker is
// not writing.
func normalizeStatus(path string, now time.Time) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var status map[string]any
	if json.Unmarshal(content, &status) != nil || status == nil {
		return nil
	}

	written := info.ModTime()
	status["updated_at"] = written.UTC().Format(time.RFC3339)
	status["last_turn_end"] = now.UTC().Format(time.RFC3339)
	if _, ok := status["blocked_on"]; ok && status["state"] != "blocked" {
		status["blocked_on"] = ""
	}

	out, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(out, '\n'), info.Mode().Perm()); err != nil {
		return err
	}
	// Given back, so the next turn's updated_at is still the worker's own
	// last write and not this rewrite.
	return os.Chtimes(path, written, written)
}
