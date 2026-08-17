package store

import "errors"

// ErrNotImplemented is returned by postgres.Store methods that P1-04
// declares on the Store interface but does not yet implement. Later
// tickets replace these bodies without touching the interface (SPEC §3.3).
var ErrNotImplemented = errors.New("store: not implemented")

// ErrNotAttributable is AnalyticsSeries/AnalyticsBreakdown's signal that the
// requested metric/dimension is not attributable to a model under an
// active `?model=` filter (SPEC §4.3: "Same rule for /breakdown and
// /timeseries: metric=sessions with a model filter is a 400
// (urn:argus:error:not-attributable) rather than a silently empty
// series"). A distinct sentinel rather than a generic error so P3-08's
// httpapi layer can map it onto that exact problem+json response without
// string-matching an error message.
var ErrNotAttributable = errors.New("store: metric not attributable under model filter")

// ErrSessionNotFound and ErrEventNotFound are the backend-independent
// not-found signals for Reader.GetSession and Reader.GetEvent, which return
// a single resource and so need to distinguish "no such row" (404) from a
// real failure (500).
//
// They live on the seam rather than in internal/store/postgres — where P3-02
// and P3-03 first declared them — because every Store implementation has to
// be able to produce them. With the sentinels owned by the concrete backend,
// internal/query had to import internal/store/postgres to recognise a
// missing session, which both inverts SPEC §3.1's dependency direction
// (httpapi -> query -> store, never past the interface to one of its
// implementations) and means storetest.Fake cannot signal 404 at all: a
// conformance test asking the Fake for an unknown id would get a 500 where
// the real server returns a 404, and the OpenAPI contract would be validated
// against behaviour production does not have. postgres keeps aliases so
// errors.Is holds either way.
var (
	ErrSessionNotFound = errors.New("store: session not found")
	ErrEventNotFound   = errors.New("store: event not found")
)

// ErrInvalidCursor is the backend-independent signal for "a `?cursor=`
// value that decoded past httpapi's own shallow shape check but failed a
// backend's stricter decode" (SPEC §4.1: "opaque, validated, 400 on
// tamper"; M14 audit finding).
//
// It lives on the seam for the same reason ErrSessionNotFound/
// ErrEventNotFound do (see their doc comment above): httpapi's list
// handlers sit behind internal/query, which must never import a concrete
// backend to recognise a tampered cursor, and storetest.Fake needs a way to
// signal the same failure the real postgres backend can produce so the
// conformance suite exercises the 400 path production actually has.
// postgres.ErrInvalidCursor is kept as an alias so errors.Is holds against
// either name.
var ErrInvalidCursor = errors.New("store: invalid cursor")
