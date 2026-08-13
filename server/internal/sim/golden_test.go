package sim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeterminism_ByteIdenticalOutput is the ticket's AC1: "argusd sim
// --out=/tmp/f --seed=7 --sessions=3 twice produces byte-identical files
// (golden test in CI over a 1-session run) — which is only true because
// --clock-origin defaults to a fixed epoch under --out (review M7)".
//
// It runs RunCLI twice into two fresh temp directories with the same
// --seed/--sessions/--flush-immediately, using seed 193 (testdata/README.md:
// chosen for a small session so the committed golden stays small) and
// diffs the two output trees byte-for-byte, then diffs the first tree
// against the committed golden fixture so a determinism regression is
// caught even if it happens to be self-consistent within a single CI run.
func TestDeterminism_ByteIdenticalOutput(t *testing.T) {
	t.Parallel()

	dirA := t.TempDir()
	dirB := t.TempDir()

	args := []string{"--out=" + dirA, "--seed=193", "--sessions=1", "--flush-immediately"}
	code := RunCLI(args, os.Stdout, os.Stderr)
	require.Equal(t, 0, code)

	args2 := []string{"--out=" + dirB, "--seed=193", "--sessions=1", "--flush-immediately"}
	code = RunCLI(args2, os.Stdout, os.Stderr)
	require.Equal(t, 0, code)

	requireDirsByteIdentical(t, dirA, dirB)
	requireDirsByteIdentical(t, dirA, filepath.Join("testdata", "golden"))
}

// requireDirsByteIdentical shells out to `diff -r` rather than
// reimplementing a recursive tree comparison: it is the same check the
// ticket's own manual verification command uses
// ("diff -r /tmp/simA /tmp/simB && echo BYTE-IDENTICAL OK"), so a failure
// here reproduces character-for-character under the same command a human
// would run by hand.
func requireDirsByteIdentical(t *testing.T, a, b string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "diff", "-r", a, b) //nolint:gosec // test-only: a/b are always this test's own t.TempDir()/testdata paths, never external input
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "diff -r %s %s:\n%s", a, b, out)
}
