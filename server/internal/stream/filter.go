package stream

import "github.com/YohannHommet/argus/server/internal/model"

// Filter is the firehose's `?kinds=&project=&vendor=` predicate (SPEC
// §4.1, §5.3). A zero Filter matches everything — Subscribe(AllTopic(),
// Filter{}) is the plain, unfiltered firehose. Kinds ORs within the field
// (any listed kind matches); the three fields AND across each other (SPEC
// §4.1: "repeated params OR within a field, AND across fields").
type Filter struct {
	Kinds   []model.Kind
	Project string
	Vendor  string
}

// MatchEvent reports whether env should reach a subscriber holding f.
//
// Project is matched against env.Project, not anything on env.Event —
// Envelope.Project carries the session-projection project the publisher
// just wrote (SPEC §5.3), because model.Event has no project field of its
// own. f.Project == "" means "no project filter", so it matches every
// envelope, INCLUDING one whose Project is itself "". But f.Project == "x"
// matches ONLY Project == "x": an envelope carrying "" (its session's
// project is not yet known) therefore matches NO non-empty project filter.
// SPEC §5.3 documents this as deliberate and self-correcting once the
// SessionStart hook resolves the session's project, not a bug to work
// around here.
func (f Filter) MatchEvent(env Envelope) bool {
	return matchKinds(f.Kinds, env.Event.Kind) &&
		matchExact(f.Project, env.Project) &&
		matchExact(f.Vendor, env.Event.Vendor)
}

// MatchSession decides whether a `session` frame (SPEC §5.1) reaches a
// subscriber holding f. Kinds does not apply — a model.SessionSummary
// carries no Kind — only Project and Vendor, which SessionSummary carries
// directly (unlike Event, it needs no Envelope wrapper to be filterable).
func (f Filter) MatchSession(s model.SessionSummary) bool {
	return matchExact(f.Project, s.Project) && matchExact(f.Vendor, s.Vendor)
}

// matchExact implements the wildcard rule shared by Project and Vendor
// filtering: an empty filter value means "no filter" and matches anything;
// a non-empty one requires an exact match — see MatchEvent's doc for why an
// empty actual value still fails a non-empty filter rather than matching
// it.
func matchExact(filter, actual string) bool {
	return filter == "" || filter == actual
}

// matchKinds implements Kinds' OR-within-field rule: an empty slice means
// "no kind filter" (matches everything), otherwise k must equal one of the
// listed entries.
func matchKinds(kinds []model.Kind, k model.Kind) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, want := range kinds {
		if want == k {
			return true
		}
	}
	return false
}
