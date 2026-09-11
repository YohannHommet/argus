// Command argus-sim is the standalone traffic-generator binary (SPEC §7).
// It wires the same internal/sim.RunCLI as `argusd sim`, so the two
// binaries can never drift ("Two binaries, one implementation").
package main

import (
	"os"

	"github.com/YohannHommet/argus/server/internal/sim"
)

func main() {
	os.Exit(sim.RunCLI(os.Args[1:], os.Stdout, os.Stderr))
}
