package sim

import (
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/google/uuid"
)

// kvString/kvInt/kvDouble/kvBool build one OTLP KeyValue of the given
// scalar type. These are the only functions in this package that construct
// *commonpb.KeyValue directly, mirroring otlpattrs.go's decoding side being
// the only place the normalizer touches these protobuf types (doc.go).
func kvString(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func kvInt(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func kvDouble(k string, v float64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v}}}
}

func kvBool(k string, v bool) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v}}}
}

// sessionIdentity holds the per-session identity fields the live capture
// shows repeated on every log record's own attribute set (research doc §2:
// "user.id, session.id, organization.id, user.email, user.account_uuid,
// user.account_id, terminal.type" observed on every tool_decision record;
// SPEC §1.5.1's mapping table lists session.id/prompt.id as the two
// promoted common attributes and "everything else → attrs" for the rest).
// Values here are synthetic (uuid-derived from the session RNG, never a
// real identity), matching testdata/README.md-style "no real identity
// values" hygiene the SPEC requires of committed goldens.
type sessionIdentity struct {
	userID          string
	sessionID       string
	orgID           string
	userEmail       string
	userAccountUUID string
	userAccountID   string
	terminalType    string
	appVersion      string
	osType          string
	osVersion       string
	hostArch        string
}

// newSessionIdentity builds one session's identity block, drawing
// terminal.type from the SPEC §7.1 set (including undocumented values) and
// deriving every other field deterministically from r so two runs with the
// same --seed produce byte-identical identity attributes.
func newSessionIdentity(r *sessionRNG) sessionIdentity {
	return sessionIdentity{
		userID:          "user_" + r.uuid().String(),
		sessionID:       r.uuid().String(),
		orgID:           r.uuid().String(),
		userEmail:       "user@example.invalid", // live capture §2/§3: synthetic placeholder shape ("user@example.invalid"), never a real address
		userAccountUUID: r.uuid().String(),
		userAccountID:   "user_" + r.uuid().String(),
		terminalType:    pick(r.Rand, terminalTypesWeighted),
		appVersion:      "2.1.228", // live capture resource attrs: service.version observed value
		osType:          "linux",   // live capture resource attrs: os.type observed value
		osVersion:       "6.18.33.2-microsoft-standard-WSL2",
		hostArch:        "amd64",
	}
}

// terminalTypesWeighted turns the plain terminalTypes set (projects.go)
// into an even weighted table so pick() can draw from it; SPEC §7.1 does
// not give terminal.type a probability table, only "a set that includes
// undocumented values", so each entry is equally likely.
var terminalTypesWeighted = func() []weighted[string] {
	out := make([]weighted[string], len(terminalTypes))
	p := 1.0 / float64(len(terminalTypes))
	for i, t := range terminalTypes {
		out[i] = weighted[string]{prob: p, val: t}
	}
	return out
}()

// resource builds the ResourceLogs/ResourceMetrics resource block for one
// session: service.name=claude-code, service.version, os.type, os.version,
// host.arch (live capture §4.5's "Resource attributes present" list), plus
// wsl.version when the drawn terminal.type is the WSL value observed
// alongside it in the same capture.
func (id sessionIdentity) resource() *resourcepb.Resource {
	attrs := []*commonpb.KeyValue{
		kvString("host.arch", id.hostArch),
		kvString("os.type", id.osType),
		kvString("os.version", id.osVersion),
		kvString("service.name", "claude-code"),
		kvString("service.version", id.appVersion),
	}
	if id.terminalType == "wsl-Ubuntu" {
		attrs = append(attrs, kvString("wsl.version", "2"))
	}
	return &resourcepb.Resource{Attributes: attrs}
}

// commonRecordAttrs returns the per-record identity attributes the live
// capture shows on every log record regardless of event.name (research doc
// §2's observed key list), in the same order the fixtures use so
// byte-for-byte comparisons against them stay easy to eyeball.
func (id sessionIdentity) commonRecordAttrs() []*commonpb.KeyValue {
	return []*commonpb.KeyValue{
		kvString("user.id", id.userID),
		kvString("session.id", id.sessionID),
		kvString("organization.id", id.orgID),
		kvString("user.email", id.userEmail),
		kvString("user.account_uuid", id.userAccountUUID),
		kvString("user.account_id", id.userAccountID),
		kvString("terminal.type", id.terminalType),
	}
}

// uuid mints a deterministic-looking v4-shaped UUID from r's own stream.
// google/uuid.NewRandomFromReader reads from a supplied io.Reader;
// sessionRNGReader (rng.go) adapts *rand.Rand to that shape so the whole
// identifier stays inside the seeded PCG stream (SPEC §7.2's determinism
// guarantee would otherwise break the moment an identifier is minted from
// crypto/rand or the global math/rand/v2 source).
func (r *sessionRNG) uuid() uuid.UUID {
	id, err := uuid.NewRandomFromReader(sessionRNGReader{r.Rand})
	if err != nil {
		// NewRandomFromReader only fails if the reader errors; sessionRNGReader
		// never does, so this is unreachable in practice. Falling back to the
		// nil UUID keeps the function panic-free rather than asserting an
		// invariant that cannot actually break.
		return uuid.UUID{}
	}
	return id
}
