package normalize

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// TestWiredHookEventsMapToKnownKind reads the event set from argus_hook.py's
// WIRING table rather than restating it, so wiring a name the normalizer cannot
// map fails here instead of landing as `unknown` in production (SPEC §1.5.2).
func TestWiredHookEventsMapToKnownKind(t *testing.T) {
	t.Parallel()

	script, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "argus_hook.py"))
	require.NoError(t, err)

	block := regexp.MustCompile(`(?ms)^WIRING\s*=\s*\((.*?)^\)`).FindSubmatch(script)
	require.NotNil(t, block, "WIRING table not found in argus_hook.py")

	var wired []string
	for _, m := range regexp.MustCompile(`\(\s*"([A-Za-z]+)"\s*,`).FindAllSubmatch(block[1], -1) {
		wired = append(wired, string(m[1]))
	}

	// Kept as a literal so an empty or partial regexp match fails instead of passing vacuously;
	// ElementsMatch counts multiplicity, so a duplicated WIRING row fails too.
	require.ElementsMatch(t, []string{
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"PostToolUseFailure", "PostToolBatch", "SubagentStart", "SubagentStop",
		"Stop", "SessionEnd", "InstructionsLoaded",
	}, wired)

	n := newTestHookNormalizer(false)
	for _, name := range wired {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// No hook_event_name in the body: a self-naming payload would win and prove the wrong route.
			events, err := n.FromHookPayloadWithEvent([]byte(`{"session_id":"s1"}`), name)
			require.NoError(t, err)
			require.Len(t, events, 1)
			require.Equal(t, name, events[0].EventName)
			require.NotEqual(t, model.KindUnknown, events[0].Kind)
		})
	}
}
