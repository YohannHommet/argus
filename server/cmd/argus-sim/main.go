// Command argus-sim is the standalone traffic-generator binary SPEC §7 and
// DECISIONS.md name explicitly ("argus-sim, the traffic generator"). It is
// a thin shim over internal/sim: `argusd sim` (cmd/argusd/main.go) wires
// the exact same internal/sim.RunCLI, so the two binaries can never drift
// (SPEC lead note 7: "Two binaries, one implementation").
package main

import (
	"os"

	"github.com/YohannHommet/argus/server/internal/sim"
)

func main() {
	os.Exit(sim.RunCLI(os.Args[1:], os.Stdout, os.Stderr))
}
