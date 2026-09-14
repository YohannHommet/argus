package normalize

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// The ?event= query param on POST /ingest/hook is the transport's statement
// of which hook fired. It is a *fallback*: a payload that names itself wins,
// so replaying a captured body through a mislabelled URL can never silently
// relabel it.

func TestFromHookPayloadWithEvent_FillsMissingHookEventName(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	events, err := n.FromHookPayloadWithEvent(
		[]byte(`{"session_id":"sess-1","cwd":"/home/u/proj","source":"startup"}`), "SessionStart")
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, model.KindSessionStart, events[0].Kind)
	require.Equal(t, "SessionStart", events[0].EventName)
	require.Equal(t, "SessionStart", events[0].Attrs["hook_event_name"],
		"the injected name must land in attrs too, so the stored event explains its own classification")
}

func TestFromHookPayloadWithEvent_PayloadNameWinsOverQueryParam(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	events, err := n.FromHookPayloadWithEvent(
		[]byte(`{"session_id":"sess-1","hook_event_name":"SessionEnd","reason":"clear"}`), "SessionStart")
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, model.KindSessionEnd, events[0].Kind)
	require.Equal(t, "SessionEnd", events[0].EventName)
}

func TestFromHookPayloadWithEvent_EmptyFallbackLeavesPayloadUnclassified(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	events, err := n.FromHookPayloadWithEvent([]byte(`{"session_id":"sess-1"}`), "")
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, model.KindUnknown, events[0].Kind)
	require.Empty(t, events[0].EventName)
	require.NotContains(t, events[0].Attrs, "hook_event_name",
		"an absent fallback must not invent an empty hook_event_name key")
}

func TestFromHookPayloadWithEvent_AppliesToEveryArrayElement(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	events, err := n.FromHookPayloadWithEvent(
		[]byte(`[{"session_id":"s1"},{"session_id":"s2","hook_event_name":"SessionEnd"}]`), "SessionStart")
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, model.KindSessionStart, events[0].Kind)
	require.Equal(t, model.KindSessionEnd, events[1].Kind, "a self-naming element still wins per element")
}

func TestFromHookPayloadWithEvent_BlankHookEventNameFallsBackToQueryParam(t *testing.T) {
	t.Parallel()
	n := newTestHookNormalizer(false)

	events, err := n.FromHookPayloadWithEvent(
		[]byte(`{"session_id":"sess-1","hook_event_name":""}`), "SessionStart")
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, model.KindSessionStart, events[0].Kind)
}

// Backward compatibility for replay (argus-sim posts self-naming bodies to
// the bare path): injecting must be a true no-op there, dedup key included,
// so a resend still collapses onto the row the pre-?event wiring wrote.
func TestFromHookPayloadWithEvent_SelfNamingPayloadKeepsItsDedupKey(t *testing.T) {
	t.Parallel()
	body := []byte(`{"session_id":"sess-1","hook_event_name":"SessionEnd","reason":"clear"}`)

	bare, err := newTestHookNormalizer(false).FromHookPayload(body)
	require.NoError(t, err)
	matching, err := newTestHookNormalizer(false).FromHookPayloadWithEvent(body, "SessionEnd")
	require.NoError(t, err)
	conflicting, err := newTestHookNormalizer(false).FromHookPayloadWithEvent(body, "SessionStart")
	require.NoError(t, err)

	require.NotEmpty(t, bare[0].DedupKey)
	require.Equal(t, bare[0].DedupKey, matching[0].DedupKey)
	require.Equal(t, bare[0].DedupKey, conflicting[0].DedupKey,
		"a mislabelled URL must not fork the dedup key of a payload that names itself")
}

// MessageDisplay is dropped before classification; the gate must see the
// injected name too, or a ?event=MessageDisplay URL would bypass it.
func TestFromHookPayloadWithEvent_InjectedMessageDisplayIsStillGated(t *testing.T) {
	t.Parallel()

	events, err := newTestHookNormalizer(false).
		FromHookPayloadWithEvent([]byte(`{"session_id":"sess-1"}`), "MessageDisplay")
	require.NoError(t, err)
	require.Empty(t, events)

	events, err = newTestHookNormalizer(true).
		FromHookPayloadWithEvent([]byte(`{"session_id":"sess-1"}`), "MessageDisplay")
	require.NoError(t, err)
	require.Len(t, events, 1)
}
