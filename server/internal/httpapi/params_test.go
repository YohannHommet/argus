package httpapi

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
)

// These tests live in package httpapi (not httpapi_test) because
// parseLimit/parseTimeParam/parseRelativeShorthand/repeatedParam/
// decodeCursorParam/castSessionStatuses/castKinds are all unexported —
// they are httpapi's own internal binding/validation helpers, not part of
// its public surface.

func TestParseLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{"absent defaults to 50", "", defaultLimit, false},
		{"explicit zero defaults to 50", "0", defaultLimit, false},
		{"in range", "25", 25, false},
		{"exactly max", "500", maxLimit, false},
		{"above max clamps silently", "9999", maxLimit, false},
		{"negative is an error", "-1", 0, true},
		{"non-numeric is an error", "abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseLimit(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				var pe *paramError
				require.ErrorAs(t, err, &pe)
				require.Equal(t, "limit", pe.param)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseRelativeShorthand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want time.Duration
		ok   bool
	}{
		{"hours, native ParseDuration", "-24h", -24 * time.Hour, true},
		{"days, hand-rolled", "-7d", -7 * 24 * time.Hour, true},
		{"fractional days", "-1.5d", -36 * time.Hour, true},
		{"not relative at all", "garbage", 0, false},
		{"RFC3339 is not relative shorthand", "2026-08-11T09:00:00Z", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseRelativeShorthand(tt.raw)
			require.Equal(t, tt.ok, ok)
			if ok {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestParseTimeParam(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	t.Run("absent is nil, no error", func(t *testing.T) {
		t.Parallel()
		got, err := parseTimeParam("from", "", now)
		require.NoError(t, err)
		require.Nil(t, got)
	})

	t.Run("RFC 3339 parses", func(t *testing.T) {
		t.Parallel()
		got, err := parseTimeParam("from", "2026-08-11T09:00:00Z", now)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.True(t, got.Equal(time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)))
	})

	t.Run("RFC 3339 with fractional seconds parses", func(t *testing.T) {
		t.Parallel()
		got, err := parseTimeParam("from", "2026-08-11T09:02:11.412Z", now)
		require.NoError(t, err)
		require.NotNil(t, got)
	})

	t.Run("relative shorthand resolves against the given now", func(t *testing.T) {
		t.Parallel()
		got, err := parseTimeParam("from", "-7d", now)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.True(t, got.Equal(now.Add(-7*24*time.Hour)))
	})

	t.Run("garbage is a paramError naming the parameter", func(t *testing.T) {
		t.Parallel()
		got, err := parseTimeParam("from", "garbage", now)
		require.Nil(t, got)
		require.Error(t, err)
		var pe *paramError
		require.ErrorAs(t, err, &pe)
		require.Equal(t, "from", pe.param)
		require.Contains(t, err.Error(), `"garbage"`)
	})
}

func TestParseTimeWindow_SharesOneNowAcrossFromAndTo(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/tool-calls?from=-7d&to=-1d", nil)
	from, to, err := parseTimeWindow(req)
	require.NoError(t, err)
	require.NotNil(t, from)
	require.NotNil(t, to)
	// If from/to were resolved against two different clock reads, this
	// difference could drift from exactly 6 days by the (tiny, but
	// nonzero) time between the two calls; asserting the exact duration
	// proves they shared one "now".
	require.Equal(t, 6*24*time.Hour, to.Sub(*from))
}

func TestParseTimeWindow_GarbageNamesTheParameter(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/tool-calls?to=garbage", nil)
	_, _, err := parseTimeWindow(req)
	require.Error(t, err)
	var pe *paramError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, "to", pe.param)
}

func TestRepeatedParam(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/sessions?project=a&project=b", nil)
	require.Equal(t, []string{"a", "b"}, repeatedParam(req, "project"))
	require.Nil(t, repeatedParam(req, "vendor"))
}

func TestDecodeCursorParam(t *testing.T) {
	t.Parallel()

	t.Run("absent is empty, no error", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/sessions", nil)
		cur, err := decodeCursorParam(req, "last_event_at")
		require.NoError(t, err)
		require.Empty(t, cur)
	})

	t.Run("valid cursor passes through as an opaque store.Cursor", func(t *testing.T) {
		t.Parallel()
		encoded, err := EncodeCursor("last_event_at", "2026-08-11T09:00:00Z", "session-1")
		require.NoError(t, err)
		req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/sessions?cursor="+encoded, nil)
		cur, err := decodeCursorParam(req, "last_event_at")
		require.NoError(t, err)
		require.Equal(t, encoded, string(cur))
	})

	t.Run("tampered cursor is ErrInvalidCursor", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/sessions?cursor=not-valid-base64!!!", nil)
		_, err := decodeCursorParam(req, "last_event_at")
		require.ErrorIs(t, err, ErrInvalidCursor)
	})

	t.Run("wrong sort-key binding is ErrInvalidCursor", func(t *testing.T) {
		t.Parallel()
		encoded, err := EncodeCursor("cost_usd", 4.27, "session-1")
		require.NoError(t, err)
		req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/sessions?cursor="+encoded, nil)
		_, err = decodeCursorParam(req, "last_event_at")
		require.ErrorIs(t, err, ErrInvalidCursor)
	})
}

func TestCastSessionStatuses(t *testing.T) {
	t.Parallel()
	require.Nil(t, castSessionStatuses(nil))
	require.Equal(t, []model.SessionStatus{"active", "ended"}, castSessionStatuses([]string{"active", "ended"}))
}

func TestCastKinds(t *testing.T) {
	t.Parallel()
	require.Nil(t, castKinds(nil))
	require.Equal(t, []model.Kind{"tool.decision"}, castKinds([]string{"tool.decision"}))
}

func TestPageInfoFrom(t *testing.T) {
	t.Parallel()

	t.Run("no next page renders null cursor", func(t *testing.T) {
		t.Parallel()
		info := pageInfoFrom(query.Page{})
		require.Nil(t, info.NextCursor)
		require.False(t, info.HasMore)
	})

	t.Run("next page renders the cursor string", func(t *testing.T) {
		t.Parallel()
		info := pageInfoFrom(query.Page{NextCursor: "abc", HasMore: true})
		require.NotNil(t, info.NextCursor)
		require.Equal(t, "abc", *info.NextCursor)
		require.True(t, info.HasMore)
	})
}
