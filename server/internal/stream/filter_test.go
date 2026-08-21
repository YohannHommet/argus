package stream_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// TestFilter_MatchEvent covers Filter's event-side semantics in isolation
// from the Hub: Kinds ORs within the field, the three fields AND across
// each other, and the "" project/vendor rules SPEC §5.3 spells out
// (ticket AC: "?project= filter matches on Envelope.Project and an
// envelope with "" matches no project filter").
func TestFilter_MatchEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		filter stream.Filter
		env    stream.Envelope
		want   bool
	}{
		{
			name:   "zero filter matches everything",
			filter: stream.Filter{},
			env:    stream.Envelope{Event: model.Event{Kind: model.KindToolResult, Vendor: "claude_code"}, Project: "acme"},
			want:   true,
		},
		{
			name:   "kinds ORs within the field",
			filter: stream.Filter{Kinds: []model.Kind{model.KindLLMRequest, model.KindToolResult}},
			env:    stream.Envelope{Event: model.Event{Kind: model.KindToolResult}},
			want:   true,
		},
		{
			name:   "kinds excludes a kind not in the list",
			filter: stream.Filter{Kinds: []model.Kind{model.KindLLMRequest}},
			env:    stream.Envelope{Event: model.Event{Kind: model.KindToolResult}},
			want:   false,
		},
		{
			name:   "project filter matches its exact envelope project",
			filter: stream.Filter{Project: "acme"},
			env:    stream.Envelope{Project: "acme"},
			want:   true,
		},
		{
			name:   "project filter excludes a different project",
			filter: stream.Filter{Project: "acme"},
			env:    stream.Envelope{Project: "other"},
			want:   false,
		},
		{
			name:   "an envelope with empty project matches NO project filter",
			filter: stream.Filter{Project: "acme"},
			env:    stream.Envelope{Project: ""},
			want:   false,
		},
		{
			name:   "empty filter project matches an envelope with empty project too",
			filter: stream.Filter{Project: ""},
			env:    stream.Envelope{Project: ""},
			want:   true,
		},
		{
			name:   "vendor filter matches",
			filter: stream.Filter{Vendor: "codex"},
			env:    stream.Envelope{Event: model.Event{Vendor: "codex"}},
			want:   true,
		},
		{
			name:   "fields AND across: project matches but vendor does not",
			filter: stream.Filter{Project: "acme", Vendor: "codex"},
			env:    stream.Envelope{Event: model.Event{Vendor: "claude_code"}, Project: "acme"},
			want:   false,
		},
		{
			name:   "fields AND across: all three must pass",
			filter: stream.Filter{Kinds: []model.Kind{model.KindToolResult}, Project: "acme", Vendor: "codex"},
			env:    stream.Envelope{Event: model.Event{Kind: model.KindToolResult, Vendor: "codex"}, Project: "acme"},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.filter.MatchEvent(tt.env))
		})
	}
}

// TestFilter_MatchSession covers the session-frame side: per the
// prescribed API's doc comment, Kinds does not apply (a SessionSummary has
// no Kind), only Project/Vendor do, matched directly off the summary
// (SessionSummary carries both fields itself, no Envelope wrapper needed).
func TestFilter_MatchSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		filter  stream.Filter
		session model.SessionSummary
		want    bool
	}{
		{
			name:    "zero filter matches everything",
			filter:  stream.Filter{},
			session: model.SessionSummary{Project: "acme", Vendor: "claude_code"},
			want:    true,
		},
		{
			name:    "kinds field is ignored for session frames",
			filter:  stream.Filter{Kinds: []model.Kind{model.KindLLMRequest}},
			session: model.SessionSummary{Project: "acme"},
			want:    true,
		},
		{
			name:    "project filter excludes a mismatched session",
			filter:  stream.Filter{Project: "acme"},
			session: model.SessionSummary{Project: "other"},
			want:    false,
		},
		{
			name:    "an empty session project matches no project filter, same rule as events",
			filter:  stream.Filter{Project: "acme"},
			session: model.SessionSummary{Project: ""},
			want:    false,
		},
		{
			name:    "vendor filter excludes a mismatched session",
			filter:  stream.Filter{Vendor: "codex"},
			session: model.SessionSummary{Vendor: "claude_code"},
			want:    false,
		},
		{
			name:    "fields AND across: project matches but vendor does not",
			filter:  stream.Filter{Project: "acme", Vendor: "codex"},
			session: model.SessionSummary{Project: "acme", Vendor: "claude_code"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.filter.MatchSession(tt.session))
		})
	}
}
