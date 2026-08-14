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
