// sse.go implements P5-02: GET /api/v1/stream (the firehose) and GET
// /api/v1/sessions/{id}/stream (SPEC §5.1-§5.3). Both handlers share one
// core, serveStream, that follows the ticket's prescribed six-step sequence
// verbatim: bind params -> attach to the hub before writing any response
// byte (SPEC §5.2's attach-before-query ordering) -> write headers/retry ->
// replay a requested backlog -> run the live select loop -> tear down on
// disconnect. See sseWriter's doc comment for the single most important
// framing invariant this file enforces (id: only on `event: event` frames).

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// defaultStreamHeartbeat/defaultStreamReplayWindow/defaultStreamReplayMax
// mirror config.Config's own ARGUS_STREAM_* defaults (SPEC §3.7). They
// exist here, distinct from config's zero value, because a nil
// Deps.Config (Deps' own doc comment: "nil is treated as all-defaults") or
// an unset duration/limit would otherwise leave a heartbeat ticker
// constructed with a 0 period (time.NewTicker panics on that) or make every
// replay request look out-of-window — see streamHeartbeat/
// streamReplayWindow/streamReplayMax below.
const (
	defaultStreamHeartbeat    = 15 * time.Second
	defaultStreamReplayWindow = 5 * time.Minute
	defaultStreamReplayMax    = 2000

	// minWriteDeadline floors the per-write deadline sseWriter refreshes
	// before every frame (Trap 2, SPEC §5.3): a very short
	// ARGUS_STREAM_HEARTBEAT (this ticket's own shortened test config
	// included) must never shrink the deadline below the same 30s bound
	// http.Server.WriteTimeout used to enforce for every handler, or a
	// perfectly healthy connection could get reaped as if it were dead.
	minWriteDeadline = 30 * time.Second

	// sseRetryMS is SPEC §5.3's fixed reconnect backoff, sent once at open.
	sseRetryMS = 3000
)

func streamHeartbeat(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.StreamHeartbeat <= 0 {
		return defaultStreamHeartbeat
	}
	return cfg.StreamHeartbeat
}

func streamReplayWindow(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.StreamReplayWindow <= 0 {
		return defaultStreamReplayWindow
	}
	return cfg.StreamReplayWindow
}

func streamReplayMax(cfg *config.Config) int {
	if cfg == nil || cfg.StreamReplayMax <= 0 {
		return defaultStreamReplayMax
	}
	return cfg.StreamReplayMax
}

// isStreamPath reports whether p is one of the two SSE routes this file
// mounts. It lives here rather than in middleware.go (Trap 1's prescribed
// fix) because the knowledge of what a stream path looks like belongs with
// the handler that owns those routes, not with the generic timeout
// middleware that merely needs a yes/no answer per request.
func isStreamPath(p string) bool {
	if p == "/api/v1/stream" {
		return true
	}
	rest, ok := strings.CutPrefix(p, "/api/v1/sessions/")
	if !ok {
		return false
	}
	id, suffix, ok := strings.Cut(rest, "/")
	return ok && id != "" && suffix == "stream"
}

// mountStreamRoutes attaches the two SSE routes this ticket owns. Called
// only when d.Stream != nil (router.go's nil-safe convention, matching
// Reader/Analytics/Mounter): a nil Streamer means Phase 5 isn't wired in
// yet (P5-03 does that in internal/app/serve.go), so neither route exists
// rather than existing and 500ing on every request.
func mountStreamRoutes(r chi.Router, streamer Streamer, replay Replayer, cfg *config.Config, logger *slog.Logger) {
	r.Get("/stream", streamAllHandler(streamer, replay, cfg, logger))
	r.Get("/sessions/{id}/stream", streamSessionHandler(streamer, replay, cfg, logger))
}

// streamAllHandler implements GET /api/v1/stream (SPEC §5.1, §5.3): the
// firehose, filtered by kinds/project/vendor.
func streamAllHandler(streamer Streamer, replay Replayer, cfg *config.Config, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter := stream.Filter{
			Kinds: castKinds(repeatedParam(r, "kinds")),
			// project/vendor bind a single value here, not repeatedParam's
			// usual OR-set (contrast listEventsHandler's store.EventFilter,
			// whose Project/Vendor are []string): internal/stream/filter.go's
			// Filter.Project/Vendor are single strings by the hub's own
			// already-landed design (this ticket does not own internal/stream),
			// so the firehose can express only one project/one vendor per
			// subscription — q.Get takes the first value if a client repeats
			// the param, rather than silently dropping the request.
			Project: q.Get("project"),
			Vendor:  q.Get("vendor"),
		}
		serveStream(w, r, streamer, replay, cfg, logger, stream.AllTopic(), filter)
	}
}

// streamSessionHandler implements GET /api/v1/sessions/{id}/stream (SPEC
// §5.1, §5.3). openapi.yaml declares no filter params on this operation
// (unlike streamAll), so the topic itself — scoped to one session — is the
// only filtering that applies; Filter{} matches everything within it.
func streamSessionHandler(streamer Streamer, replay Replayer, cfg *config.Config, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		serveStream(w, r, streamer, replay, cfg, logger, stream.SessionTopic(id), stream.Filter{})
	}
}

// serveStream is the six-step sequence the ticket prescribes, shared by
// both routes. Every early return before step 2's Subscribe call produces a
// normal problem+json response; every return after it goes through the
// deferred sub.Close() teardown instead.
func serveStream(
	w http.ResponseWriter, r *http.Request,
	streamer Streamer, replay Replayer, cfg *config.Config, logger *slog.Logger,
	topic stream.Topic, filter stream.Filter,
) {
	// Step 1: bind the replay position first. An undecodable ref is a plain
	// 400 — never a stream that opens and then fails inside itself (SPEC
	// §4.1's "opaque, validated, 400 on tamper" rule, same as GET
	// /api/v1/events/{ref}).
	afterRef, hasAfter, err := decodeReplayPosition(r)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid-event-ref", "event_ref is not valid base64url of ts:seq")
		return
	}

	// Step 2: attach to the hub BEFORE writing any response byte (SPEC
	// §5.2's attach-before-query ordering — see replayBacklog for the other
	// half of this guarantee). Subscribing first is also what lets a
	// Subscribe failure be an ordinary problem+json response rather than a
	// text/event-stream carrying an error inside it: nothing has been
	// written yet.
	sub, err := streamer.Subscribe(topic, filter)
	if err != nil {
		switch {
		case errors.Is(err, stream.ErrTooManySubscribers):
			writeProblem(w, r, http.StatusServiceUnavailable, "too-many-subscribers", "too many live SSE subscribers")
		case errors.Is(err, stream.ErrClosed):
			writeProblem(w, r, http.StatusServiceUnavailable, "not-ready", "server is shutting down")
		default:
			writeInternalError(w, r, logger, err)
		}
		return
	}
	defer sub.Close()

	heartbeat := streamHeartbeat(cfg)
	writeDeadline := 2 * heartbeat
	if writeDeadline < minWriteDeadline {
		writeDeadline = minWriteDeadline
	}
	sw := &sseWriter{w: w, rc: http.NewResponseController(w), deadline: writeDeadline, logger: logger}

	// Step 3: headers (all four, SPEC §5.3) + retry + flush.
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if err := sw.retry(sseRetryMS); err != nil {
		return
	}

	// Step 4: replay, if a position was requested. dedupe is bounded at
	// streamReplayMax(cfg) entries: replayBacklog adds at most one entry
	// per row EventsSince returns, which is itself capped at that same
	// limit, and the live loop below only ever removes entries from it.
	dedupe := make(map[string]struct{}, streamReplayMax(cfg))
	if hasAfter {
		if !replayBacklog(r.Context(), sw, replay, cfg, afterRef, dedupe, logger) {
			return
		}
	}

	// Step 5 (+ step 6, folded into sseWriter.write): the live select loop.
	// Its own doc comment covers the two-value receive contract and the
	// dedupe consult/shrink rule.
	runLiveLoop(r.Context(), sw, sub, heartbeat, dedupe, logger)
	// Step 6's teardown-on-disconnect is the deferred sub.Close() above,
	// reached whichever way runLiveLoop returns.
}

// decodeReplayPosition implements the prescribed `?after=` precedence: an
// explicit client request beats the browser's automatic `Last-Event-ID`
// header when both are present. Absent both, hasAfter is false and the
// connection just goes live with no backlog — not an error.
func decodeReplayPosition(r *http.Request) (ref model.EventRef, hasAfter bool, err error) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		return model.EventRef{}, false, nil
	}
	ref, err = model.DecodeEventRef(raw)
	if err != nil {
		return model.EventRef{}, false, err
	}
	return ref, true, nil
}

// replayBacklog implements SPEC §5.2's reconnect replay. The hub is already
// attached (serveStream's step 2 ran first), so any live event landing
// while EventsSince runs is buffered on sub.C() rather than lost — that is
// the whole race fix, and it is why this function only ever ADDS to dedupe,
// never has to worry about missing something published concurrently.
// Returns false if a frame write failed (the peer is gone) and the caller
// must stop entirely.
func replayBacklog(
	ctx context.Context, sw *sseWriter, replay Replayer, cfg *config.Config,
	after model.EventRef, dedupe map[string]struct{}, logger *slog.Logger,
) bool {
	floor := time.Now().Add(-streamReplayWindow(cfg))

	// A nil Replayer (Deps.Replay unset while Deps.Stream is wired — see
	// Deps.Replay's own doc comment) cannot run the query at all. The
	// honest degradation is the same response an out-of-window ref gets:
	// openapi.yaml's StreamResetFrame has exactly one `reason` value
	// regardless of which condition produced it.
	if replay == nil || after.TS.Before(floor) {
		return emitReset(sw, floor) == nil
	}

	// windowStart is provably after.TS here: SPEC §5.2 defines it as
	// max(ts, now-window), and the branch above already rejected ts <
	// floor, so ts >= floor makes ts the max.
	events, err := replay.EventsSince(ctx, after, after.TS, streamReplayMax(cfg))
	if err != nil {
		if logger != nil {
			logger.LogAttrs(ctx, slog.LevelError, "httpapi: sse: replay query failed, continuing live without backlog",
				slog.String("error", err.Error()))
		}
		// Never kill the connection over a failed replay, and never
		// silently pretend there was no backlog: tell the client to
		// refetch over REST instead.
		return emitReset(sw, floor) == nil
	}

	for _, e := range events {
		te := newTimelineEvent(e)
		if err := sw.frame("event", te.EventRef, te); err != nil {
			return false
		}
		dedupe[te.EventRef] = struct{}{}
	}
	return true
}

func emitReset(sw *sseWriter, from time.Time) error {
	return sw.frame("reset", "", resetFramePayload{Reason: "replay_window_exceeded", From: from.UTC().Format(time.RFC3339)})
}

// runLiveLoop is prescribed design step 5: select on the subscription
// channel, the heartbeat ticker, and ctx.Done(). It must use the two-value
// receive on sub.C() (msg, ok := <-sub.C()): stream.Hub.Shutdown sends one
// MessageShutdown and then CLOSES the channel, so a one-value receive would
// yield a zero Message forever after that and spin this loop at 100% CPU —
// see stream.Subscription.C's own doc comment for the same contract stated
// from the sender's side.
func runLiveLoop(ctx context.Context, sw *sseWriter, sub *stream.Subscription, heartbeat time.Duration, dedupe map[string]struct{}, logger *slog.Logger) {
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Teardown: serveStream's deferred sub.Close() unsubscribes.
			return

		case <-ticker.C:
			if err := sw.heartbeatFrame(); err != nil {
				return
			}

		case msg, ok := <-sub.C():
			if !ok {
				return
			}
			if n := sub.TakeDropped(); n > 0 {
				if err := sw.frame("lag", "", lagFramePayload{Dropped: n}); err != nil {
					return
				}
			}

			switch msg.Type {
			case stream.MessageEvent:
				te := newTimelineEvent(msg.Env.Event)
				if _, dup := dedupe[te.EventRef]; dup {
					// Already delivered by replayBacklog. A ref can only
					// collide once (refs are unique, SPEC §1.2), so this
					// entry is removed rather than left behind — see
					// serveStream's dedupe-size comment. Deliberately an
					// explicit ref-set membership check, NOT a (ts,seq) <=
					// lastReplayed comparison: the hub only guarantees
					// ordering within one flush, not across flushes
					// (internal/ingest/pipeline.go's Publisher contract), so
					// a comparison could wrongly drop a legitimately-newer
					// out-of-order event.
					delete(dedupe, te.EventRef)
					continue
				}
				if err := sw.frame("event", te.EventRef, te); err != nil {
					return
				}
			case stream.MessageSession:
				if err := sw.frame("session", "", msg.Session); err != nil {
					return
				}
			case stream.MessageStats:
				if err := sw.frame("stats", "", msg.Stats); err != nil {
					return
				}
			case stream.MessageShutdown:
				// Best effort: the connection is ending either way, so a
				// write failure here changes nothing about the outcome.
				_ = sw.frame("shutdown", "", struct{}{})
				return
			default:
				// exhaustive (golangci-lint) is satisfied by the four cases
				// above listing every current stream.MessageType member;
				// this default exists only so a future member this ticket
				// doesn't know about logs instead of silently doing nothing
				// or panicking.
				if logger != nil {
					logger.Warn("httpapi: sse: unknown stream.MessageType, ignoring", "type", msg.Type)
				}
			}
		}
	}
}

// lagFramePayload/resetFramePayload are the SSE `lag`/`reset` frame bodies
// (SPEC §5.1), matching openapi.yaml's StreamLagFrame/StreamResetFrame.
// `event`/`session`/`stats`/`shutdown` frames reuse existing wire types
// directly (timelineEvent, model.SessionSummary, stream.Stats, struct{}{})
// rather than a parallel adapter — see sseWriter.frame's doc comment.
type lagFramePayload struct {
	Dropped uint64 `json:"dropped"`
}

type resetFramePayload struct {
	Reason string `json:"reason"`
	From   string `json:"from"`
}

// sseWriter frames one SSE connection's writes (SPEC §5.1, §5.3). Its
// single most important invariant — an `id:` line is written if and only
// if the frame name is "event" — is enforced in frame's one branch, and
// stated here, specifically because SPEC §5.1/review finding m4 says an
// id: on any other frame (session/stats/lag/reset/shutdown) corrupts the
// browser's Last-Event-ID on reconnect. Every frame this file emits funnels
// through frame/retry/heartbeatFrame, so that decision is made exactly
// once — never re-derived, and potentially gotten wrong, at an individual
// call site.
type sseWriter struct {
	w        io.Writer
	rc       *http.ResponseController
	deadline time.Duration
	logger   *slog.Logger

	// dlUnsupported is set once SetWriteDeadline returns
	// http.ErrNotSupported (Trap 2, SPEC §5.3): http.ResponseController
	// requires the underlying writer to opt in via Unwrap(), and while
	// chi's AccessLog wrapper does, a future middleware might not — so this
	// is logged once and then the write path continues unbounded rather
	// than failing an otherwise-healthy connection.
	dlUnsupported bool
}

// write sends one raw SSE frame's bytes end to end: refresh the per-write
// deadline, write, then flush (prescribed design step 6). A non-nil return
// means the peer is gone (or the connection cannot make progress); every
// caller must stop looping immediately rather than retry — matching
// http.Server.WriteTimeout's old global 30s bound at the per-connection
// level instead of the whole-response level, which is what makes an SSE
// connection that must legitimately outlive 30s compatible with still
// reaping a genuinely dead TCP peer (Trap 2).
func (sw *sseWriter) write(raw string) error {
	if !sw.dlUnsupported {
		if err := sw.rc.SetWriteDeadline(time.Now().Add(sw.deadline)); err != nil {
			if !errors.Is(err, http.ErrNotSupported) {
				return fmt.Errorf("httpapi: sse: set write deadline: %w", err)
			}
			sw.dlUnsupported = true
			if sw.logger != nil {
				sw.logger.Warn("httpapi: sse: response writer does not support SetWriteDeadline; continuing unbounded")
			}
		}
	}
	if _, err := io.WriteString(sw.w, raw); err != nil {
		return fmt.Errorf("httpapi: sse: write: %w", err)
	}
	if err := sw.rc.Flush(); err != nil {
		return fmt.Errorf("httpapi: sse: flush: %w", err)
	}
	return nil
}

// retry writes SPEC §5.3's one-time `retry: <ms>` line, sent right after
// headers so a browser's automatic reconnect backs off by this much rather
// than hammering a restarting server.
func (sw *sseWriter) retry(ms int) error {
	return sw.write(fmt.Sprintf("retry: %d\n\n", ms))
}

// heartbeatFrame writes SPEC §5.1's `: heartbeat` keep-alive comment line —
// deliberately not routed through frame, since a comment has no event name
// or data field at all.
func (sw *sseWriter) heartbeatFrame() error {
	return sw.write(": heartbeat\n\n")
}

// frame writes one named SSE frame (SPEC §5.1). id is emitted as an `id:`
// line only when name == "event" — see sseWriter's own doc comment for why
// that check lives here and nowhere else. payload is marshaled to JSON
// directly; every call site in this file already passes the exact wire
// shape openapi.yaml declares (timelineEvent, *model.SessionSummary,
// *stream.Stats, lagFramePayload, resetFramePayload, struct{}{}), so no
// further adaptation happens here.
func (sw *sseWriter) frame(name, id string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("httpapi: sse: marshal %s frame: %w", name, err)
	}
	var b strings.Builder
	if name == "event" && id != "" {
		b.WriteString("id: ")
		b.WriteString(id)
		b.WriteByte('\n')
	}
	b.WriteString("event: ")
	b.WriteString(name)
	b.WriteString("\ndata: ")
	b.Write(body)
	b.WriteString("\n\n")
	return sw.write(b.String())
}
