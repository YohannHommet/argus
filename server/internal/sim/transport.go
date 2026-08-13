package sim

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// contentTypeProtobuf/contentTypeJSON are the two OTLP/HTTP content types
// SPEC §7.2's --otlp-protocol flag selects between.
const (
	contentTypeProtobuf = "application/x-protobuf"
	contentTypeJSON     = "application/json"
)

// SendResult is what one Transport call reports back to runner.go for the
// exit report (SPEC §7.2: "HTTP status histogram … and non-2xx bodies").
// FileTransport always reports StatusCode 0 (there is no HTTP exchange);
// runner.go only folds StatusCode into the histogram when Err == nil and
// StatusCode != 0, i.e. only for HTTPTransport.
type SendResult struct {
	StatusCode int
	Body       []byte
	Err        error
}

// Transport is the seam between pure generation and delivery (doc.go's
// generator/transport split). Exactly two implementations exist:
// HTTPTransport (POST to a live Argus) and FileTransport (--out, fixture
// generation). runner.go is the only caller.
type Transport interface {
	SendLogs(ctx context.Context, body []byte, contentType string) SendResult
	SendMetrics(ctx context.Context, body []byte, contentType string) SendResult
	SendHooks(ctx context.Context, body []byte) SendResult
}

// HTTPTransport POSTs to a live Argus server: {target}/v1/logs,
// {target}/v1/metrics (SPEC §3.4), {target}/ingest/hook (SPEC §3.5).
type HTTPTransport struct {
	Client *http.Client
	Target string
}

func (t *HTTPTransport) post(ctx context.Context, path, contentType string, body []byte) SendResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Target+path, bytes.NewReader(body))
	if err != nil {
		return SendResult{Err: fmt.Errorf("sim: build request for %s: %w", path, err)}
	}
	req.Header.Set("Content-Type", contentType)

	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return SendResult{Err: fmt.Errorf("sim: POST %s: %w", path, err)}
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if readErr != nil {
		return SendResult{StatusCode: resp.StatusCode, Err: fmt.Errorf("sim: read response body from %s: %w", path, readErr)}
	}
	return SendResult{StatusCode: resp.StatusCode, Body: respBody}
}

// SendLogs POSTs body to {target}/v1/logs (SPEC §3.4).
func (t *HTTPTransport) SendLogs(ctx context.Context, body []byte, contentType string) SendResult {
	return t.post(ctx, "/v1/logs", contentType, body)
}

// SendMetrics POSTs body to {target}/v1/metrics (SPEC §3.4).
func (t *HTTPTransport) SendMetrics(ctx context.Context, body []byte, contentType string) SendResult {
	return t.post(ctx, "/v1/metrics", contentType, body)
}

// SendHooks POSTs body to {target}/ingest/hook (SPEC §3.5).
func (t *HTTPTransport) SendHooks(ctx context.Context, body []byte) SendResult {
	return t.post(ctx, "/ingest/hook", contentTypeJSON, body)
}

// FileTransport implements SPEC §7.2's "--out=dir/ writes the same
// payloads to files instead of POSTing (fixture generation)". Each session
// gets its own subdirectory (session-NNNN/) so a run's whole output tree is
// a deterministic function of --seed and --sessions alone: file names never
// depend on wall-clock time, goroutine scheduling, or map iteration order
// (SPEC §7.2's byte-identical-output AC, lead note 2). runner.go always
// generates and writes sequentially when a FileTransport is in play
// (concurrency only governs HTTPTransport load-mode pacing), so the
// per-session counters below never race.
type FileTransport struct {
	Dir string

	sessionDir string
	logsSeq    int
	metricsSeq int
	hooksSeq   int
}

// BeginSession switches FileTransport to writing into a fresh
// session-NNNN/ subdirectory and resets its per-kind sequence counters.
// runner.go calls this once per session before emitting that session's
// batches.
func (t *FileTransport) BeginSession(sessionOrdinal int) error {
	t.sessionDir = filepath.Join(t.Dir, fmt.Sprintf("session-%04d", sessionOrdinal))
	t.logsSeq, t.metricsSeq, t.hooksSeq = 0, 0, 0
	return os.MkdirAll(t.sessionDir, 0o755) //nolint:gosec // fixture output directory, not a security boundary
}

func (t *FileTransport) writeFile(kind string, seq int, ext string, body []byte) SendResult {
	name := filepath.Join(t.sessionDir, fmt.Sprintf("%s-%04d.%s", kind, seq, ext))
	if err := os.WriteFile(name, body, 0o644); err != nil { //nolint:gosec // fixture output file, not a security boundary
		return SendResult{Err: fmt.Errorf("sim: write %s: %w", name, err)}
	}
	return SendResult{}
}

// SendLogs writes body to session-NNNN/logs-NNNN.{pb,json} (SPEC §7.2's
// --out).
func (t *FileTransport) SendLogs(_ context.Context, body []byte, contentType string) SendResult {
	ext := "pb"
	if contentType == contentTypeJSON {
		ext = "json"
	}
	res := t.writeFile("logs", t.logsSeq, ext, body)
	t.logsSeq++
	return res
}

// SendMetrics writes body to session-NNNN/metrics-NNNN.{pb,json}.
func (t *FileTransport) SendMetrics(_ context.Context, body []byte, contentType string) SendResult {
	ext := "pb"
	if contentType == contentTypeJSON {
		ext = "json"
	}
	res := t.writeFile("metrics", t.metricsSeq, ext, body)
	t.metricsSeq++
	return res
}

// SendHooks writes body to session-NNNN/hooks-NNNN.json.
func (t *FileTransport) SendHooks(_ context.Context, body []byte) SendResult {
	res := t.writeFile("hooks", t.hooksSeq, "json", body)
	t.hooksSeq++
	return res
}
