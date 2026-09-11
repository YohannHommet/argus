package stream

import "sync/atomic"

// Subscription is the live handle Hub.Subscribe returns (SPEC §5.3). Its
// zero value is not meaningful — only Hub.Subscribe constructs one, wiring
// the back-reference Close needs — so every field is unexported; callers
// interact through C/Topic/TakeDropped/Dropped/Close only.
type Subscription struct {
	topic  Topic
	filter Filter
	ch     chan Message

	// dropped counts messages this subscriber's own buffer discarded
	// because it was full when the hub tried to send (SPEC §5.3's
	// drop-oldest rule). It is atomic because Hub.send increments it from
	// inside a shared RLock section — many concurrent ingest workers can
	// be publishing to different subscribers, or even the same one, at
	// once — while TakeDropped/Dropped read it from a completely
	// different goroutine (the SSE handler) that never touches the hub's
	// mutex at all.
	dropped atomic.Uint64

	// closed is guarded by the owning Hub's mutex, not its own atomic —
	// deliberately, so that "is this subscriber still a valid send
	// target" and "remove it from the topic maps" happen as ONE atomic
	// step (see hub.go's unsubscribe/Shutdown). Two independently
	// synchronized flags — this one, and map membership — could disagree
	// in the window between them and let a Publish call land a send on a
	// channel Close is concurrently closing, which is exactly the panic
	// the ticket's leak-free requirement (rule 6) exists to rule out.
	closed bool

	hub *Hub // back-reference so Close needs no argument and Subscription stays a plain value type to its caller
}

// C returns the channel a subscriber's writer goroutine selects on (SPEC
// §5.3: "select on subscription chan / heartbeat ticker / ctx.Done()"). It
// is closed exactly once, by Close or Hub.Shutdown, after which a receive
// yields the zero Message with ok == false — callers must treat that as
// "stop reading", not as a delivered message; MessageShutdown is the
// best-effort courtesy frame that (when delivery succeeds) arrives just
// before the close, never a substitute for checking ok.
func (s *Subscription) C() <-chan Message { return s.ch }

// Topic reports the scope this subscription was created with.
func (s *Subscription) Topic() Topic { return s.topic }

// TakeDropped atomically reads and zeroes the drop counter. The SSE
// handler calls this once per frame-send loop iteration (SPEC §5.1): a
// non-zero result means schedule an `event: lag` frame reporting exactly
// that many drops, and zeroing on read means the next call reports only
// NEW drops since the last lag frame was scheduled, never a running total
// that would make every subsequent lag frame overcount.
func (s *Subscription) TakeDropped() uint64 { return s.dropped.Swap(0) }

// Dropped reads the drop counter without zeroing it — for tests and any
// metrics/debug surface that wants the running total rather than the
// lag-frame delta TakeDropped consumes.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Close unsubscribes s and releases its slot against the Hub's
// WithMaxSubscribers cap. Idempotent — a second Close, or one racing a
// concurrent Hub.Shutdown, is a no-op rather than a double-close panic —
// and safe to call after Hub.Shutdown has already run, because Shutdown
// marks every subscriber it tears down the same way this method does (see
// hub.go's unsubscribe/Shutdown for the shared bookkeeping).
func (s *Subscription) Close() { s.hub.unsubscribe(s) }
