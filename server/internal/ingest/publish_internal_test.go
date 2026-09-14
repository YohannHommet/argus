package ingest

// White-box tests: verify projectCache bounded-eviction (load-bearing behavior).

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectCache_EvictsOldestInsertedBeyondCap(t *testing.T) {
	c := newProjectCache(3)
	c.set("a", "proj-a")
	c.set("b", "proj-b")
	c.set("c", "proj-c")
	c.set("d", "proj-d")

	require.Empty(t, c.get("a"), "the oldest-inserted entry must be evicted once the cap is hit")
	require.Equal(t, "proj-b", c.get("b"))
	require.Equal(t, "proj-c", c.get("c"))
	require.Equal(t, "proj-d", c.get("d"))
}

func TestProjectCache_UpdatingExistingKeyDoesNotConsumeCapacity(t *testing.T) {
	c := newProjectCache(2)
	c.set("a", "proj-a")
	c.set("a", "proj-a2")
	c.set("b", "proj-b")

	require.Equal(t, "proj-a2", c.get("a"), "re-setting an existing key must update its value in place")
	require.Equal(t, "proj-b", c.get("b"), "a genuinely new key must still fit under the cap after a same-key re-set")
}

func TestProjectCache_MissReturnsEmptyString(t *testing.T) {
	c := newProjectCache(10)
	require.Empty(t, c.get("never-seen"))
}
