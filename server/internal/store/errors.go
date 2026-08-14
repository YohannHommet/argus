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
