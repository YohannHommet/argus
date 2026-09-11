package sim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeterminism_ByteIdenticalOutput asserts AC1: same --seed ⇒ byte-identical
// output files (requires --clock-origin default to fixed epoch under --out).
// Diffs two runs against each other, then first run against committed golden.
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
