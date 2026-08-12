package store

import "errors"

// ErrNotImplemented is returned by postgres.Store methods that P1-04
// declares on the Store interface but does not yet implement. Later
// tickets replace these bodies without touching the interface (SPEC §3.3).
var ErrNotImplemented = errors.New("store: not implemented")
