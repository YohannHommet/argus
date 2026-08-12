// Package telemetry provides process-wide observability primitives: structured
// logging setup and build-info metadata stamped in at link time.
package telemetry

import "runtime"

// Version and Commit are set at build time via -ldflags -X, e.g.:
//
//	-X github.com/YohannHommet/argus/server/internal/telemetry.Version=1.2.3
//	-X github.com/YohannHommet/argus/server/internal/telemetry.Commit=abc1234
//
// They default to "dev" / "unknown" for unstamped `go run` / `go test` builds.
var (
	Version = "dev"
	Commit  = "unknown"
)

// GoVersion returns the Go runtime version used to build the binary (e.g. "go1.25.0").
func GoVersion() string {
	return runtime.Version()
}
