package sim

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunCLI_OutModeExitsZero smoke-tests the sim subcommand's flag surface
// (SPEC §7.2): --out run should exit 0 (no HTTP histogram path).
func TestRunCLI_OutModeExitsZero(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{
		"--out=" + dir,
		"--seed=42",
		"--sessions=2",
		"--cost-mode=omit",
		"--tool-use-id-in-hooks=true",
		"--tool-use-id-in-decision=false",
		"--otlp-protocol=http/json",
	}, &stdout, &stderr)

	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	require.Contains(t, stdout.String(), "sessions")
}

// TestRunCLI_RejectsUnknownMode covers flag validation (SPEC §7.2): unknown
// --mode must be usage error, not silent fallback.
func TestRunCLI_RejectsUnknownMode(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--mode=bogus"}, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "unknown --mode")
}

// TestRunCLI_LoadModeRequiresRateAndDuration covers SPEC §7.2's load-mode
// required knobs.
func TestRunCLI_LoadModeRequiresRateAndDuration(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--mode=load"}, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "--rate")
}
