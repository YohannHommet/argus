package otlp

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// Content-Type values SPEC §3.4 negotiates on. Anything else is a 415.
const (
	contentTypeProtobuf = "application/x-protobuf"
	contentTypeJSON     = "application/json"
)

// retryAfterSeconds is SPEC §3.4's fixed Retry-After value on a 503
// queue-full response: "the SDK retries with backoff — exactly the desired
// backpressure".
const retryAfterSeconds = "1"

// Canonical gRPC status codes (https://grpc.io/docs/guides/status-codes/),
// used as google.rpc.Status.code below. These are small, well-known
// integers with no dependency on any gRPC package — Argus has none in its
// module graph (go.mod has no genproto/grpc entry).
const (
	grpcCodeInvalidArgument   int32 = 3
	grpcCodeResourceExhausted int32 = 8
	grpcCodeUnavailable       int32 = 14
)

// wireFormat is the two encodings SPEC §3.4 negotiates: "application/
// x-protobuf -> proto.Unmarshal; application/json -> protojson.Unmarshal
// with DiscardUnknown: true". A handler always answers in the same format
// the request used.
type wireFormat int

const (
	wireProtobuf wireFormat = iota
	wireJSON
)

// negotiateFormat maps a Content-Type header onto the wire format to
// decode/encode with. Parameters (e.g. "; charset=utf-8") are ignored.
// Anything other than the two SPEC §3.4 names is unsupported (415).
func negotiateFormat(contentType string) (wireFormat, bool) {
	mediaType := contentType
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		mediaType = contentType[:i]
	}
	switch strings.TrimSpace(mediaType) {
	case contentTypeProtobuf:
		return wireProtobuf, true
	case contentTypeJSON:
		return wireJSON, true
	default:
		return 0, false
	}
}

// decodeErr is the OTLP/HTTP error contract for one failed request: an HTTP
// status, the canonical gRPC status code to embed in the google.rpc.Status
// body (see statusMessage), and a human-readable message.
type decodeErr struct {
	httpStatus int
	grpcCode   int32
	message    string
}

func (e *decodeErr) Error() string { return e.message }

// readBody applies SPEC §3.4's full request-body contract, up to but not
// including the OTLP-specific decode (that is decodeExportRequest's job,
// below — kept separate because the envelope decode differs per signal
// while the body-handling rules below do not):
//
//  1. Content-Type negotiation (415 on an unknown/missing type).
//  2. An outer http.MaxBytesReader cap on the wire bytes — covers the
//     uncompressed case directly, and bounds how much compressed data a
//     client may even send.
//  3. Content-Encoding: gzip support, with a *second*, independent cap: an
//     io.LimitReader wrapping the gzip.Reader, sized maxBodyBytes+1. This is
//     what makes the gzip-bomb AC's "bounded memory" half true — a payload
//     that would decompress to gigabytes is rejected after io.ReadAll has
//     materialized at most maxBodyBytes+1 bytes, never the claimed
//     uncompressed size (lead note 2). The outer MaxBytesReader alone would
//     not catch this: it only bounds the *compressed* bytes read off the
//     wire, which a bomb keeps deliberately small.
func readBody(w http.ResponseWriter, r *http.Request, maxBodyBytes int64) (wireFormat, []byte, *decodeErr) {
	format, ok := negotiateFormat(r.Header.Get("Content-Type"))
	if !ok {
		// The client's Content-Type is by definition not one we can answer
		// in (that's the whole reason for the 415), so the response falls
		// back to JSON — the one format guaranteed human-readable without a
		// protobuf decoder — rather than the zero wireFormat value
		// (wireProtobuf), which would hand a client that may not speak
		// protobuf at all a binary diagnostic it cannot parse (m17 minor).
		return wireJSON, nil, &decodeErr{
			httpStatus: http.StatusUnsupportedMediaType,
			grpcCode:   grpcCodeInvalidArgument,
			message:    fmt.Sprintf("unsupported content-type %q (want application/x-protobuf or application/json)", r.Header.Get("Content-Type")),
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var reader io.Reader = r.Body
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return format, nil, &decodeErr{
				httpStatus: http.StatusBadRequest,
				grpcCode:   grpcCodeInvalidArgument,
				message:    "invalid gzip stream: " + err.Error(),
			}
		}
		defer gz.Close() //nolint:errcheck // read-only decompressor on a request we're about to discard either way; a close error here carries no actionable information
		// +1 lets the size check below tell "exactly maxBodyBytes" apart
		// from "more than maxBodyBytes" without ever reading (and thus
		// allocating) more than maxBodyBytes+1 bytes of decompressed data,
		// no matter how large the compressed input claims to expand to.
		reader = io.LimitReader(gz, maxBodyBytes+1)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return format, nil, &decodeErr{
				httpStatus: http.StatusRequestEntityTooLarge,
				grpcCode:   grpcCodeResourceExhausted,
				message:    "request body exceeds the configured limit",
			}
		}
		return format, nil, &decodeErr{
			httpStatus: http.StatusBadRequest,
			grpcCode:   grpcCodeInvalidArgument,
			message:    "reading request body: " + err.Error(),
		}
	}
	if int64(len(body)) > maxBodyBytes {
		return format, nil, &decodeErr{
			httpStatus: http.StatusRequestEntityTooLarge,
			grpcCode:   grpcCodeResourceExhausted,
			message:    "decompressed request body exceeds the configured limit",
		}
	}

	return format, body, nil
}

// decodeExportRequest decodes one OTLP/HTTP Export*ServiceRequest body into
// its resource-level elements (ResourceLogs, ResourceMetrics, or
// ResourceSpans) — without ever importing
// go.opentelemetry.io/proto/otlp/collector/.../v1.
//
// That subpackage's protoc-gen-grpc-gateway output additionally imports
// github.com/grpc-ecosystem/grpc-gateway/v2 and google.golang.org/grpc,
// neither of which is anywhere in this module's go.sum (verified with
// `grep -rn genproto\|grpc go.sum` before writing this, not assumed) —
// go.mod pins go.opentelemetry.io/proto/otlp itself, but that pin does not
// make its collector subpackage compilable without also vendoring an
// unrelated gRPC stack this project otherwise has zero dependency on, and
// this ticket may not run `go get`/`go mod tidy` to add one. Every OTLP/HTTP
// Export*ServiceRequest message has the identical shape regardless: a
// single repeated field 1 (resource_logs / resource_metrics /
// resource_spans). Decoding that one field by hand, in both wire formats,
// and handing the results to normalize.FromOTLPLogs/FromOTLPMetrics exactly
// like a generated ExportLogsServiceRequest.GetResourceLogs() would,
// reproduces the same bytes-on-the-wire semantics using only the
// data-model subpackages (logs/v1, metrics/v1, trace/v1) that
// internal/ingest/normalize already imports successfully with no such
// dependency.
//
// jsonKey is the top-level field's camelCase JSON name (e.g.
// "resourceLogs"); newElem constructs one empty element to unmarshal into.
func decodeExportRequest[T proto.Message](format wireFormat, body []byte, jsonKey string, newElem func() T) ([]T, error) {
	if format == wireJSON {
		return decodeExportRequestJSON(body, jsonKey, newElem)
	}
	return decodeExportRequestProto(body, newElem)
}

// decodeExportRequestProto walks the top-level fields of an
// Export*ServiceRequest message by hand: field 1 (LEN-encoded) is one
// resource-level element, unmarshalled with the real generated type's
// proto.Unmarshal (which, as usual for protobuf, silently tolerates any
// unknown field *inside* that element — the "future OTLP version" AC).
// Any other top-level field number is skipped the same way proto.Unmarshal
// would skip it, rather than failing the request: this envelope only ever
// carries field 1 today, but that mirrors real forward-compatible protobuf
// decoding instead of assuming it always will.
func decodeExportRequestProto[T proto.Message](body []byte, newElem func() T) ([]T, error) {
	var out []T
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		body = body[n:]

		if num != 1 || typ != protowire.BytesType {
			skip := protowire.ConsumeFieldValue(num, typ, body)
			if skip < 0 {
				return nil, protowire.ParseError(skip)
			}
			body = body[skip:]
			continue
		}

		elemBytes, n := protowire.ConsumeBytes(body)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		body = body[n:]

		elem := newElem()
		if err := proto.Unmarshal(elemBytes, elem); err != nil {
			return nil, err
		}
		out = append(out, elem)
	}
	return out, nil
}

// decodeExportRequestJSON is decodeExportRequestProto's JSON counterpart:
// parse the envelope object ourselves (accepting both the canonical
// camelCase key and the proto field's original snake_case spelling, the
// same leniency protojson itself applies), then protojson.Unmarshal
// (DiscardUnknown: true, SPEC §3.4) each element individually — DiscardUnknown
// is exactly what lets an element carrying a field this pinned protobuf
// version does not know about still decode instead of erroring.
func decodeExportRequestJSON[T proto.Message](body []byte, jsonKey string, newElem func() T) ([]T, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	raw, ok := envelope[jsonKey]
	if !ok {
		raw, ok = envelope[snakeCase(jsonKey)]
	}
	if !ok || len(raw) == 0 {
		return nil, nil
	}

	var rawElems []json.RawMessage
	if err := json.Unmarshal(raw, &rawElems); err != nil {
		return nil, err
	}

	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	out := make([]T, 0, len(rawElems))
	for _, re := range rawElems {
		elem := newElem()
		if err := opts.Unmarshal(re, elem); err != nil {
			return nil, err
		}
		out = append(out, elem)
	}
	return out, nil
}

// snakeCase converts "resourceLogs" -> "resource_logs" — the only
// transformation decodeExportRequestJSON needs, mirroring protojson's own
// leniency about accepting either a message's JSON name or its original
// proto field name.
func snakeCase(camel string) string {
	var b strings.Builder
	for _, r := range camel {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// writeExportResult writes SPEC §3.4's OTLP/HTTP export response contract
// in the request's wire format: an empty Export*ServiceResponse (zero bytes
// of protobuf is a valid, fully-default message; "{}" is protojson's
// equivalent) when rejectedCount is 0, or a partial_success otherwise.
//
// Every OTLP/HTTP Export*PartialSuccess message (ExportLogsPartialSuccess,
// ExportMetricsPartialSuccess, ExportTracePartialSuccess) has the identical
// wire shape — an int64 rejected-count at field 1, a string error_message
// at field 2 — differing only in the JSON name of that count field
// ("rejectedLogRecords" / "rejectedDataPoints" / "rejectedSpans"), so one
// encoder serves all three call sites instead of tripling this logic (the
// same collector-dependency reasoning as decodeExportRequest applies to why
// there is no generated Go type to marshal here either).
//
// Wire-compatibility of this hand-encoding was verified empirically against
// the real generated types on 2026-08-12, in a scratch module where grpc was
// available, with four checks: the bytes this function emits for
// (rejected=1, "no session.id") are BYTE-IDENTICAL to
// proto.Marshal(&ExportLogsServiceResponse{PartialSuccess: ...}); they decode
// under the real ExportLogsServiceResponse to the same field values; the
// zero-byte success body decodes to a response whose PartialSuccess is nil;
// and a real ExportLogsServiceRequest round-trips into logspb.LogsData with
// its ResourceLogs intact (both declare resource_logs at field 1, repeated).
// Re-run that check if this encoder is ever edited.
func writeExportResult(w http.ResponseWriter, format wireFormat, countJSONKey string, rejectedCount int64, message string) {
	w.Header().Set("Content-Type", contentTypeForFormat(format))
	w.WriteHeader(http.StatusOK)

	if format == wireJSON {
		resp := map[string]any{}
		if rejectedCount > 0 {
			resp["partialSuccess"] = map[string]any{
				countJSONKey:   strconv.FormatInt(rejectedCount, 10), // protobuf JSON mapping: int64 as a string
				"errorMessage": message,
			}
		}
		_ = json.NewEncoder(w).Encode(resp) // response write errors are unactionable
		return
	}

	var body []byte
	if rejectedCount > 0 {
		var ps []byte
		ps = protowire.AppendTag(ps, 1, protowire.VarintType)
		ps = protowire.AppendVarint(ps, uint64(rejectedCount))
		if message != "" {
			ps = protowire.AppendTag(ps, 2, protowire.BytesType)
			ps = protowire.AppendString(ps, message)
		}
		body = protowire.AppendTag(body, 1, protowire.BytesType)
		body = protowire.AppendBytes(body, ps)
	}
	_, _ = w.Write(body) // response write errors are unactionable; nil body is a valid empty message
}

func contentTypeForFormat(format wireFormat) string {
	if format == wireJSON {
		return contentTypeJSON
	}
	return contentTypeProtobuf
}

// statusJSON is the JSON mapping of google.rpc.Status
// (https://cloud.google.com/apis/design/errors#error_model): {"code":
// <int32>, "message": <string>}. Used when the request arrived as
// application/json, so the response format matches the request format.
type statusJSON struct {
	Code    int32  `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// statusMessage hand-encodes google.rpc.Status's wire shape (int32 code = 1;
// string message = 2; repeated google.protobuf.Any details = 3, omitted
// here) directly with google.golang.org/protobuf/encoding/protowire — the
// same low-level encoder protoc-generated code itself calls, for the same
// reason decodeExportRequest avoids the collector subpackage: this module
// has no google.golang.org/genproto or gRPC dependency to import the real
// google.rpc.Status Go type from, and this ticket must not add one. A Go
// struct is not what makes google.rpc.Status "real" on the wire, though:
// protobuf's binary format only requires agreement on field numbers and
// wire types, which this function reproduces exactly, so the bytes it
// produces are indistinguishable on the wire from any real
// google.rpc.Status encoder's output.
func statusMessage(code int32, message string) []byte {
	var b []byte
	if code != 0 {
		b = protowire.AppendTag(b, 1, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(code)) //nolint:gosec // code is always one of this file's small, non-negative grpcCode* constants
	}
	if message != "" {
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendString(b, message)
	}
	return b
}

// writeStatus writes SPEC §3.4's error contract for a rejected request
// (415/400/413/503): a google.rpc.Status-shaped body (statusMessage/
// statusJSON) in the same wire format the request used, under httpStatus.
func writeStatus(w http.ResponseWriter, httpStatus int, format wireFormat, code int32, message string) {
	w.Header().Set("Content-Type", contentTypeForFormat(format))
	w.WriteHeader(httpStatus)
	if format == wireJSON {
		_ = json.NewEncoder(w).Encode(statusJSON{Code: code, Message: message}) // response write errors are unactionable
		return
	}
	_, _ = w.Write(statusMessage(code, message)) // response write errors are unactionable
}
