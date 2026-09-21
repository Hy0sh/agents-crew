// Package names derives the Herdr agent names for a run, scoped to the
// repo it targets so several swarms — one per project directory — can run
// at the same time without colliding. Herdr requires every live agent
// name to be globally unique and match [a-z][a-z0-9_-]{0,31}; a plain
// "master"/"worker1" only ever supported one swarm system-wide.
package names

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var invalidChar = regexp.MustCompile(`[^a-z0-9-]`)

// Slug derives a short, herdr-safe, collision-resistant identifier for
// repo: a readable basename plus a hash of the full path, so two
// different directories sharing a basename never collide, and repeated
// runs in the same directory always agree on the same names.
func Slug(repo string) string {
	base := strings.ToLower(filepath.Base(repo))
	base = invalidChar.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if len(base) > 15 {
		base = base[:15]
	}
	sum := sha256.Sum256([]byte(repo))
	hash := hex.EncodeToString(sum[:])[:6]
	if base == "" {
		return hash
	}
	return base + "-" + hash
}

// Master is the master agent's name for a run identified by slug.
func Master(slug string) string {
	return "master-" + slug
}

// Worker is worker i's agent name for a run identified by slug.
func Worker(slug string, i int) string {
	return fmt.Sprintf("worker%d-%s", i, slug)
}
