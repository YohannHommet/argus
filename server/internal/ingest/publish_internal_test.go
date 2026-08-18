package ingest

// White-box tests: this file lives in package ingest (not ingest_test) so
// it can reach projectCache directly — an unexported implementation detail
// of HubPublisher's project-resolution cache, not part of the Publisher
// seam's public contract, but its bounded-eviction behavior (publish.go's
// defaultProjectCacheCap doc comment) is load-bearing enough to pin
// directly rather than only indirectly through HubPublisher's own tests.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectCache_EvictsOldestInsertedBeyondCap(t *testing.T) {
	c := newProjectCache(3)
	c.set("a", "proj-a")
	c.set("b", "proj-b")
	c.set("c", "proj-c")
	c.set("d", "proj-d") // must evict "a", the oldest inserted

	require.Equal(t, "", c.get("a"), "the oldest-inserted entry must be evicted once the cap is hit")
	require.Equal(t, "proj-b", c.get("b"))
	require.Equal(t, "proj-c", c.get("c"))
	require.Equal(t, "proj-d", c.get("d"))
}

func TestProjectCache_UpdatingExistingKeyDoesNotConsumeCapacity(t *testing.T) {
	c := newProjectCache(2)
	c.set("a", "proj-a")
	c.set("a", "proj-a2") // same key again: must not push "a" onto the eviction queue twice
	c.set("b", "proj-b")

	require.Equal(t, "proj-a2", c.get("a"), "re-setting an existing key must update its value in place")
	require.Equal(t, "proj-b", c.get("b"), "a genuinely new key must still fit under the cap after a same-key re-set")
}

func TestProjectCache_MissReturnsEmptyString(t *testing.T) {
	c := newProjectCache(10)
	require.Equal(t, "", c.get("never-seen"))
}
