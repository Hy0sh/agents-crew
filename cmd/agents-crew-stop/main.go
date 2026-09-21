// agents-crew-stop tears down a swarm started by agents-crew: each
// worker's environment (via wtm), the shared status directory, and the
// Herdr workspace itself. Closing the terminal alone does nothing — Herdr
// is a persistent server that outlives it, and so do the environments.
//
// Equivalent to `agents-crew stop`; kept as a standalone binary too for
// convenience.
package main

import (
	"fmt"
	"os"

	"agents-crew/internal/teardown"
)

func main() {
	if err := teardown.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
