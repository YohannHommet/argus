package sim

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunCLI_OutModeExitsZero is a smoke test for the `sim` subcommand's
// flag surface (SPEC §7.2's whole flag list): a --out run with a handful
// of the ticket-named flags set should parse cleanly and exit 0, since
// --out never touches the exit-code-from-HTTP-histogram path.
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

// TestRunCLI_RejectsUnknownMode covers the flag-validation branch (SPEC
// §7.2's --mode=demo|load — anything else must be a usage error, not a
// silent fallback to demo).
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
