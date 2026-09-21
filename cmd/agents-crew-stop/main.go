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

	"github.com/Hy0sh/agents-crew/internal/teardown"
	"github.com/Hy0sh/agents-crew/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("agents-crew-stop " + version.String())
		return
	}
	if err := teardown.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
