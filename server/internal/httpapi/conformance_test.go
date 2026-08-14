// conformance_test.go is P3-09's OpenAPI conformance harness (docs/SPEC.md
// §4.4): it loads server/api/openapi.yaml with kin-openapi, routes the ~50
// requests testdata/requests.yaml describes through the *real* router
// (httpapi.New) wired to a fake store (internal/store/testing.Fake), and
// validates every response body — not just its status code — against the
// schema for the operation that request actually hit. A meta-assertion
// requires every operationId in the spec to appear in the table, either as
// a round-tripped request or as an explicit, reasoned exemption (SSE and the
// ingest mount seams — see requests.yaml's own comment).
//
// This is deliberately the strictest test in the package: SPEC's own words
// are "a conformance test that passes because it validates too little is
// worse than no test at all" (ticket lead note), so every assertion here
// either fails the build on a genuine drift between a handler and the
// contract, or is annotated with why it cannot.
package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
	storetest "github.com/YohannHommet/argus/server/internal/store/testing"
)

// --- fixture identities shared by the Fake and requests.yaml ---------------

// conformSessionID/conformUnknownSessionID/conform*EventRef name the fixed
// entities requests.yaml's `path`s reference by literal id (sessions) or by
// the `{{known_event_ref}}`/`{{unknown_event_ref}}` placeholders
// resolveRequestPath substitutes (event_ref is an opaque base64url encoding
// of a timestamp+seq, SPEC §1.2 — not something a YAML file can spell out by
// hand without duplicating model.EventRef's own codec).
const (
	conformSessionID        = "s-conform"
	conformUnknownSessionID = "does-not-exist"
)

var (
	conformKnownEventRef   = model.EventRef{TS: time.Date(2026, 8, 11, 9, 12, 4, 221_000_000, time.UTC), Seq: 918233}
	conformUnknownEventRef = model.EventRef{TS: time.Date(2026, 8, 11, 9, 12, 5, 0, time.UTC), Seq: 1}

	// conformUnknownQuerySource is the ticket AC's "a response containing an
	// unknown query_source string validates (it must, since the schema is
	// string)" (SPEC §0): a value Argus has never seen, wired into the known
	// event's query_source field, so every request that returns it exercises
	// the AC — TestConformance_UnknownQuerySourceValidates asserts it
	// explicitly and by name.
	conformUnknownQuerySource = "a_future_query_source"
)

// --- request table -----------------------------------------------------

// requestCase is one row of testdata/requests.yaml (see that file's header
// comment for the field-by-field contract).
type requestCase struct {
	Name         string `yaml:"name"`
	OperationID  string `yaml:"operation_id"`
	Method       string `yaml:"method"`
	Path         string `yaml:"path"`
	ExpectStatus int    `yaml:"expect_status"`
}

// exemptCase is one row of requests.yaml's `exempt` list: an operationId
// this table deliberately never round-trips, with why.
type exemptCase struct {
	OperationID string `yaml:"operation_id"`
	Reason      string `yaml:"reason"`
}

type requestTable struct {
	Requests []requestCase `yaml:"requests"`
	Exempt   []exemptCase  `yaml:"exempt"`
}

func loadRequestTable(t *testing.T) requestTable {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "requests.yaml"))
	require.NoError(t, err)
	var table requestTable
	require.NoError(t, yaml.Unmarshal(raw, &table))
	require.NotEmpty(t, table.Requests, "testdata/requests.yaml must define at least one request")
	return table
}

// resolveRequestPath substitutes requests.yaml's event_ref placeholders (see
// its header comment) with the actual opaque refs this file's Fake fixtures
// use.
func resolveRequestPath(path string) string {
	path = strings.ReplaceAll(path, "{{known_event_ref}}", conformKnownEventRef.Encode())
	path = strings.ReplaceAll(path, "{{unknown_event_ref}}", conformUnknownEventRef.Encode())
	return path
}

// --- openapi.yaml loading ------------------------------------------------

// specFilePath resolves server/api/openapi.yaml from this source file's own
// location (runtime.Caller), matching internal/tools/specvalidate/main.go's
// own rationale: `go test` always runs with cwd set to this package's
// directory, but a relative path written for one invocation convention can
// silently break under another.
func specFilePath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "api", "openapi.yaml")
}

// loadSpec loads and structurally validates server/api/openapi.yaml (the
// same load+Validate specvalidate performs) and builds the legacy chi-style
// router kin-openapi uses to resolve a request to its operation.
func loadSpec(t *testing.T) (*openapi3.T, routers.Router) {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specFilePath())
	require.NoError(t, err)
	require.NoError(t, doc.Validate(t.Context()))

	router, err := legacyrouter.NewRouter(doc)
	require.NoError(t, err)
	return doc, router
}

// specOperationIDs returns every operationId declared in doc, in the
// document's own path/method iteration order.
func specOperationIDs(doc *openapi3.T) []string {
	var ids []string
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			ids = append(ids, op.OperationID)
		}
	}
	return ids
}

// --- the fake store fixtures ------------------------------------------------

// conformFixtures bundles every canned value the conformance Fake serves, so
// the individual Func closures below and
// TestConformance_UnknownQuerySourceValidates can all name the exact same
// objects.
type conformFixtures struct {
	session      *model.SessionDetail
	turns        []model.Turn
	knownEvent   model.Event
	otherEvent   model.Event
	toolCalls    []model.ToolCall
	subagentTree model.SubagentTree
	summary      model.Summary
	modelSummary model.Summary
	series       model.Series
	breakdown    model.Breakdown
	decisions    model.DecisionMatrix
	facets       model.Facets
	dataQuality  model.DataQuality
	unknownKinds []model.UnknownKindGroup
	hookLatency  model.HookLatency
}

func newConformFixtures() conformFixtures {
	startedAt := time.Date(2026, 8, 11, 9, 2, 11, 412_000_000, time.UTC)
	lastEventAt := time.Date(2026, 8, 11, 9, 31, 44, 900_000_000, time.UTC)
	durationMS := int64(1773488)
	p50 := 120

	session := &model.SessionDetail{
		SessionSummary: model.SessionSummary{
			ID:              conformSessionID,
			Vendor:          "claude_code",
			Project:         "argus",
			CWD:             "/home/y/Labs/argus",
			Status:          model.SessionStatusActive,
			StartType:       "fresh",
			StartedAt:       &startedAt,
			EndedAt:         nil,
			LastEventAt:     lastEventAt,
			DurationMS:      &durationMS,
			TurnCount:       12,
			EventCount:      480,
			ToolCallCount:   96,
			ToolRejectCount: 3,
			SubagentCount:   2,
			ErrorCount:      1,
			Tokens:          model.TokenUsage{Input: 41233, Output: 18944, CacheRead: 1204331, CacheCreation: 88210},
			Cost: model.SessionCost{
				USD: 4.2711, ReportedUSD: 4.2711, EstimatedUSD: 0, EstimatedShare: 0,
				ByQuerySource:       map[string]float64{"": 3.9011, "sdk": 0.35, conformUnknownQuerySource: 0.02},
				DominantQuerySource: "",
				OtherQuerySourceUSD: 0.37,
			},
			Models:       []string{"claude-opus-5", "claude-sonnet-4-5"},
			Partial:      false,
			AppVersion:   "2.1.220",
			Entrypoint:   "cli",
			TerminalType: "wsl-Ubuntu",
		},
		PermissionModeHistory: []model.PermissionModeChange{
			{TS: startedAt.Add(3 * time.Minute), From: "default", To: "acceptEdits", Trigger: "user"},
		},
		TopTools: []model.ToolUsageSummary{
			{ToolName: "Edit", Calls: 40, Rejects: 2, P50MS: &p50},
			{ToolName: "Bash", Calls: 10, Rejects: 0, P50MS: nil},
		},
		DecisionSummary: model.SessionDecisionSummary{
			Accept: 90, Reject: 6,
			BySource:   map[string]int{"config": 60, "hook": 10, "user_permanent": 10, "user_temporary": 5, "user_reject": 4, "user_abort": 1},
			ExactShare: 1.0,
		},
		SourcesSeen:      []model.Source{model.SourceOTelLog, model.SourceHook},
		RawEventsExpired: false,
		HookLatency:      &model.SessionHookLatency{P50MS: 9, P95MS: 41, ByHookEvent: map[string]int64{"PostToolUse": 9}},
		FirstSeenAt:      startedAt.Add(-time.Second),
		User:             "yohann",
		OrganizationID:   "org_1",
	}

	turns := []model.Turn{
		{
			SessionID: conformSessionID, PromptID: "p_88f1", TurnIndex: intPtr(3),
			StartedAt: timePtr(startedAt.Add(9 * time.Minute)), EndedAt: timePtr(startedAt.Add(90 * time.Second)),
			FirstSeenAt: startedAt.Add(9 * time.Minute), LastEventAt: startedAt.Add(91 * time.Minute),
			DurationMS: intPtr(90000), Status: model.TurnStatusComplete,
			APIRequestCount: 2, ToolCallCount: 5, ToolRejectCount: 1, ErrorCount: 0,
			InputTokens: 4123, OutputTokens: 1894, CacheReadTokens: 12000, CacheCreateTokens: 800,
			CostUSD: 0.42, CostEstimatedUSD: 0, Models: []string{"claude-opus-5"},
		},
	}

	toolUseID := "toolu_01A"
	knownEvent := model.Event{
		Seq: conformKnownEventRef.Seq, ID: "0192abcd-0000-0000-0000-000000000001",
		TS: conformKnownEventRef.TS, SessionID: conformSessionID, PromptID: strPtr("p_88f1"),
		Vendor: "claude_code", Source: model.SourceOTelLog, Kind: model.KindToolDecision,
		EventName: "tool_decision",
		ToolName:  strPtr("Edit"), ToolUseID: &toolUseID,
		Decision: strPtr("reject"), DecisionSource: strPtr("user_reject"), ToolSource: strPtr("builtin"),
		QuerySource:    &conformUnknownQuerySource,
		PermissionMode: strPtr("default"),
		FilePath:       strPtr("server/internal/store/postgres/store.go"),
		Attrs:          map[string]any{"tool_decision.tool_use_id": toolUseID, "tool_decision.decision": "reject"},
	}

	model5 := "claude-opus-5"
	otherEvent := model.Event{
		Seq: conformKnownEventRef.Seq + 1, ID: "0192abcd-0000-0000-0000-000000000002",
		TS: conformKnownEventRef.TS.Add(time.Second), SessionID: conformSessionID, PromptID: strPtr("p_88f1"),
		Vendor: "claude_code", Source: model.SourceOTelLog, Kind: model.KindLLMRequest,
		EventName: "api_request", Model: &model5,
		InputTokens: int64Ptr(4123), OutputTokens: int64Ptr(1894), CacheReadTokens: int64Ptr(12000), CacheCreationTokens: int64Ptr(800),
		CostUSD: float64Ptr(0.42), DurationMS: intPtr(900), Success: boolPtr(true),
		Attrs: map[string]any{},
	}

	inputBytes := 412
	toolCalls := []model.ToolCall{
		{
			ID: "5b1f7a2e-0000-0000-0000-000000000001", SessionID: conformSessionID, PromptID: strPtr("p_88f1"),
			ToolUseID: &toolUseID, ToolName: "Edit", ToolSource: strPtr("builtin"),
			Decision: strPtr("reject"), DecisionSource: strPtr("user_reject"), PermissionMode: strPtr("default"),
			StartedAt: conformKnownEventRef.TS, DecidedAt: timePtr(conformKnownEventRef.TS.Add(200 * time.Millisecond)), EndedAt: timePtr(conformKnownEventRef.TS.Add(200 * time.Millisecond)),
			DurationMS: intPtr(200), WaitMS: intPtr(200), Success: boolPtr(false),
			FilePath: strPtr("server/internal/store/postgres/store.go"), InputSizeBytes: &inputBytes,
			Correlation: model.CorrelationExact, EventCount: 2,
		},
	}

	subagentTree := model.SubagentTree{
		Nodes: []model.SubagentNode{
			{
				AgentID: "root", ParentAgentID: nil, AgentType: "main", Depth: 0, Status: model.SubagentStatusRunning,
				StartedAt: &startedAt, EndedAt: nil, ToolCallCount: intPtr(40), CostUSD: nil,
				Children: []model.SubagentNode{
					{
						AgentID: "ag_7", ParentAgentID: strPtr("root"), AgentType: "Explore", Depth: 1, Status: model.SubagentStatusComplete,
						StartedAt: timePtr(startedAt.Add(3 * time.Minute)), EndedAt: timePtr(startedAt.Add(6 * time.Minute)),
						SpawnToolUseID: strPtr("toolu_9f"), ToolCallCount: intPtr(12), CostUSD: nil,
						Children: []model.SubagentNode{},
					},
				},
			},
		},
		CostAttribution: model.SubagentCostAttribution{
			ByQuerySource:       map[string]float64{"": 3.9011, "sdk": 0.35},
			DominantQuerySource: "",
			OtherQuerySourceUSD: 0.35,
			PerNodeAvailable:    false,
			Note:                "Claude Code does not emit per-agent cost; api_request carries query_source only.",
		},
	}

	windowFrom := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	windowTo := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	summary := model.Summary{
		Window:   model.Window{From: windowFrom, To: windowTo, Bucket: "hour"},
		Sessions: int64Ptr(34), Turns: int64Ptr(291), APIRequests: 1044, APIErrors: 7,
		ToolCalls: int64Ptr(2210), ToolRejects: int64Ptr(91), RejectRate: float64Ptr(0.0412),
		Tokens: model.TokenUsage{Input: 1200000, Output: 340000, CacheRead: 81000000, CacheCreation: 990000},
		Cost:   model.Cost{USD: 71.44, ReportedUSD: 70.02, EstimatedUSD: 1.42, EstimatedShare: 0.0199},
		LOC:    &model.LOC{Added: 8123, Removed: 2044}, ActiveSeconds: int64Ptr(41220),
		Source: model.Source("event"), MetricsOnlyProjects: []string{"legacy-app"}, NotAttributable: []string{},
	}
	modelSummary := model.Summary{
		Window:   model.Window{From: windowFrom, To: windowTo, Bucket: "hour"},
		Sessions: nil, Turns: nil, APIRequests: 512, APIErrors: 3,
		ToolCalls: nil, ToolRejects: nil, RejectRate: nil,
		Tokens: model.TokenUsage{Input: 600000, Output: 170000, CacheRead: 40000000, CacheCreation: 500000},
		Cost:   model.Cost{USD: 35.2, ReportedUSD: 35.2, EstimatedUSD: 0, EstimatedShare: 0},
		LOC:    nil, ActiveSeconds: nil,
		Source: model.Source("event"), MetricsOnlyProjects: []string{},
		NotAttributable: []string{"sessions", "turns", "tool_calls", "tool_rejects", "reject_rate", "loc", "active_seconds"},
	}

	series := model.Series{
		Bucket:  "hour",
		Buckets: []time.Time{windowTo.Add(-time.Hour), windowTo},
		Series:  []model.SeriesPoint{{Key: "argus", Values: []float64{1.2, 0}}},
		Other:   &model.SeriesOther{Values: []float64{0.4, 0.1}},
	}

	breakdown := model.Breakdown{Dimension: "tool", Rows: []model.BreakdownRow{{Key: "Edit", Value: 812, Share: 0.37}}}

	decisions := model.DecisionMatrix{Rows: []model.DecisionMatrixRow{
		{
			ToolName: "Edit", Accept: 300, Reject: 41,
			BySource:   map[string]int64{"config": 210, "hook": 12, "user_permanent": 40, "user_temporary": 38, "user_reject": 37, "user_abort": 4},
			ExactShare: 1.0, P50WaitMS: int64Ptr(1900), P95WaitMS: int64Ptr(22400),
		},
	}}

	facets := model.Facets{
		Projects: []string{"argus", "platform"}, Models: []string{"claude-opus-5", "claude-sonnet-4-5"},
		Vendors: []string{"claude_code", "codex"}, Tools: []string{"Edit", "Read", "Bash"},
		DecisionSources: []string{"config", "hook", "user_permanent", "user_temporary", "user_reject", "user_abort"},
		QuerySources:    []string{"", "sdk", conformUnknownQuerySource},
	}

	dataQuality := model.DataQuality{LogsExporterSeen: true, MetricsExporterSeen: true, HooksSeen: true, ToolDetailsSeen: false}

	unknownKinds := []model.UnknownKindGroup{
		{
			EventName: "some_new_event", Source: model.SourceOTelLog, Count: 41,
			FirstSeen: windowFrom, LastSeen: windowTo, Sample: map[string]any{"raw.attr": "value"},
		},
	}

	hookLatency := model.HookLatency{Rows: []model.HookLatencyRow{
		{HookEvent: "PostToolUse", Executions: 412, P50MS: 9, P95MS: 41, P99MS: 120, Errors: 0, Cancelled: 0},
	}}

	return conformFixtures{
		session: session, turns: turns, knownEvent: knownEvent, otherEvent: otherEvent,
		toolCalls: toolCalls, subagentTree: subagentTree, summary: summary, modelSummary: modelSummary,
		series: series, breakdown: breakdown, decisions: decisions, facets: facets,
		dataQuality: dataQuality, unknownKinds: unknownKinds, hookLatency: hookLatency,
	}
}

// newConformanceFake wires every store.Reader method the conformance table
// (or the dedicated schema tests below) can reach to fx's fixed, in-memory
// data — deterministic across runs (no map-iteration-dependent output, no
// time.Now() in any returned value), per the ticket note that a conformance
// table must never flake.
func newConformanceFake(fx conformFixtures) *storetest.Fake {
	return &storetest.Fake{
		ListSessionsFunc: func(_ context.Context, _ store.SessionFilter, _ store.Page) ([]model.SessionSummary, store.Cursor, error) {
			return []model.SessionSummary{fx.session.SessionSummary}, "", nil
		},
		GetSessionFunc: func(_ context.Context, id string) (*model.SessionDetail, error) {
			if id == conformSessionID {
				return fx.session, nil
			}
			return nil, store.ErrSessionNotFound
		},
		ListTurnsFunc: func(_ context.Context, _ string) ([]model.Turn, error) {
			return fx.turns, nil
		},
		ListEventsFunc: func(_ context.Context, _ store.EventFilter, _ store.Page) ([]model.Event, store.Cursor, error) {
			return []model.Event{fx.knownEvent, fx.otherEvent}, "", nil
		},
		GetEventFunc: func(_ context.Context, ref model.EventRef) (*model.Event, error) {
			if ref.TS.Equal(conformKnownEventRef.TS) && ref.Seq == conformKnownEventRef.Seq {
				return &fx.knownEvent, nil
			}
			return nil, store.ErrEventNotFound
		},
		ListToolCallsFunc: func(_ context.Context, _ store.ToolCallFilter, _ store.Page) ([]model.ToolCall, store.Cursor, error) {
			return fx.toolCalls, "", nil
		},
		SubagentTreeFunc: func(_ context.Context, _ string) (model.SubagentTree, error) {
			return fx.subagentTree, nil
		},
		AnalyticsSummaryFunc: func(_ context.Context, f store.AnalyticsFilter) (model.Summary, error) {
			if len(f.Model) > 0 {
				return fx.modelSummary, nil
			}
			return fx.summary, nil
		},
		AnalyticsSeriesFunc: func(_ context.Context, f store.AnalyticsFilter, g store.Grouping) (model.Series, error) {
			if g.Metric == store.MetricSessions && len(f.Model) > 0 {
				return model.Series{}, store.ErrNotAttributable
			}
			return fx.series, nil
		},
		AnalyticsBreakdownFunc: func(_ context.Context, _ store.AnalyticsFilter, d store.Dimension) (model.Breakdown, error) {
			b := fx.breakdown
			b.Dimension = string(d.Name)
			if d.Name == store.DimensionQuerySource {
				b.Rows = []model.BreakdownRow{{Key: conformUnknownQuerySource, Value: 0.35, Share: 1.0}}
			}
			return b, nil
		},
		AnalyticsDecisionsFunc: func(_ context.Context, _ store.AnalyticsFilter) (model.DecisionMatrix, error) {
			return fx.decisions, nil
		},
		FacetsFunc: func(_ context.Context) (model.Facets, error) {
			return fx.facets, nil
		},
		DataQualityFunc: func(_ context.Context) (model.DataQuality, error) {
			return fx.dataQuality, nil
		},
		UnknownKindsFunc: func(_ context.Context, _ time.Time, _ int) ([]model.UnknownKindGroup, error) {
			return fx.unknownKinds, nil
		},
		HookLatencyFunc: func(_ context.Context, _ store.AnalyticsFilter) (model.HookLatency, error) {
			return fx.hookLatency, nil
		},
	}
}

func newConformanceRouter(fake *storetest.Fake) http.Handler {
	return httpapi.New(httpapi.Deps{Reader: fake, Analytics: fake})
}

func intPtr(v int) *int              { return &v }
func int64Ptr(v int64) *int64        { return &v }
func float64Ptr(v float64) *float64  { return &v }
func boolPtr(v bool) *bool           { return &v }
func strPtr(v string) *string        { return &v }
func timePtr(v time.Time) *time.Time { return &v }

// --- the conformance test itself --------------------------------------

// TestConformance is P3-09's AC: every operationId's response, for every row
// of testdata/requests.yaml, must validate against server/api/openapi.yaml's
// schema — not merely match its status code.
func TestConformance(t *testing.T) {
	doc, router := loadSpec(t)
	table := loadRequestTable(t)

	fake := newConformanceFake(newConformFixtures())
	handler := newConformanceRouter(fake)

	for _, tc := range table.Requests {
		t.Run(tc.Name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tc.Method, resolveRequestPath(tc.Path), nil)
			rec := httptest.NewRecorder()

			route, pathParams, err := router.FindRoute(req)
			require.NoError(t, err, "no route in openapi.yaml matches %s %s", tc.Method, tc.Path)
			require.Equal(t, tc.OperationID, route.Operation.OperationID,
				"the router (server/api/openapi.yaml) resolves %s %s to a different operationId than the table declares", tc.Method, tc.Path)

			handler.ServeHTTP(rec, req)
			require.Equal(t, tc.ExpectStatus, rec.Code, "unexpected status; body: %s", rec.Body.String())

			respInput := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{
					Request:    req,
					PathParams: pathParams,
					Route:      route,
				},
				Status: rec.Code,
				Header: rec.Header(),
			}
			respInput.SetBodyBytes(rec.Body.Bytes())

			err = openapi3filter.ValidateResponse(t.Context(), respInput)
			require.NoError(t, err, "response for operation %s does not match its openapi.yaml schema; body: %s", tc.OperationID, rec.Body.String())
		})
	}

	t.Run("meta: every operationId is covered", func(t *testing.T) {
		requireOperationIDCoverage(t, doc, table)
	})
}

// requireOperationIDCoverage is the ticket's meta-assertion: every
// operationId openapi.yaml defines must appear in requests.yaml, either as a
// round-tripped request or as a reasoned exemption — an operation added to
// the spec without a matching table entry fails the build immediately,
// rather than silently shipping with 0% conformance coverage. It also runs
// in the opposite direction: a table entry naming an operationId the spec no
// longer has would otherwise quietly stop testing anything.
func requireOperationIDCoverage(t *testing.T, doc *openapi3.T, table requestTable) {
	t.Helper()

	covered := map[string]bool{}
	for _, r := range table.Requests {
		covered[r.OperationID] = true
	}
	// The exemption list is pinned here, in code, not merely required to carry
	// a reason. Requiring a reason alone leaves the 100%-coverage gate with an
	// open escape hatch: a future operation could be exempted with a one-line
	// justification and ship at 0% conformance coverage without anything going
	// red. Pinning the set means adding an exemption fails this test until
	// someone deliberately edits it here — which is the point at which the
	// trade-off gets reviewed rather than assumed.
	allowedExemptions := map[string]string{
		"streamSession": "SSE; no hub before Phase 5 — covered instead by direct StreamEvent schema validation",
		"streamAll":     "SSE; no hub before Phase 5 — covered instead by direct StreamEvent schema validation",
		"ingestLogs":    "mounted via Deps.OTLPMounter, not the Reader ports this Fake backs",
		"ingestMetrics": "mounted via Deps.OTLPMounter, not the Reader ports this Fake backs",
		"ingestTraces":  "mounted via Deps.OTLPMounter, not the Reader ports this Fake backs",
		"ingestHook":    "mounted via Deps.HookMounter, not the Reader ports this Fake backs",
	}
	seenExempt := map[string]bool{}
	for _, e := range table.Exempt {
		require.NotEmpty(t, e.Reason, "exempt operationId %q must document why it is exempt", e.OperationID)
		require.Contains(t, allowedExemptions, e.OperationID,
			"operationId %q is not an approved conformance exemption — round-trip it, or add it to allowedExemptions in this file so the trade-off gets reviewed", e.OperationID)
		seenExempt[e.OperationID] = true
		covered[e.OperationID] = true
	}
	for id := range allowedExemptions {
		require.True(t, seenExempt[id],
			"approved exemption %q is no longer listed in requests.yaml: if it can now be round-tripped, delete it from allowedExemptions too so this list cannot rot", id)
	}

	specIDs := specOperationIDs(doc)
	specSet := make(map[string]bool, len(specIDs))
	var missing []string
	for _, id := range specIDs {
		specSet[id] = true
		if !covered[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing,
		"every operationId in server/api/openapi.yaml must have a testdata/requests.yaml entry (request or exempt): missing %v", missing)

	var stale []string
	for id := range covered {
		if !specSet[id] {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	require.Empty(t, stale,
		"testdata/requests.yaml references operationId(s) no longer defined in server/api/openapi.yaml: %v", stale)
}

// TestConformance_UnknownQuerySourceValidates is the ticket AC, asserted by
// name and value: a `query_source` Argus has never seen must still validate,
// because openapi.yaml types it `string`, never an `enum` (SPEC §0, §4.4) —
// a generated union would otherwise make an unseen value a type error.
func TestConformance_UnknownQuerySourceValidates(t *testing.T) {
	_, router := loadSpec(t)
	fake := newConformanceFake(newConformFixtures())
	handler := newConformanceRouter(fake)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/"+conformKnownEventRef.Encode(), nil)
	rec := httptest.NewRecorder()

	route, pathParams, err := router.FindRoute(req)
	require.NoError(t, err)

	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"query_source":"`+conformUnknownQuerySource+`"`,
		"the fixture event must actually carry the unknown query_source this test is named for")

	respInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: pathParams, Route: route},
		Status:                 rec.Code,
		Header:                 rec.Header(),
	}
	respInput.SetBodyBytes(rec.Body.Bytes())
	require.NoError(t, openapi3filter.ValidateResponse(t.Context(), respInput),
		"a response carrying an unseen query_source value must still validate — the schema types it string, not enum")
}

// TestConformance_StreamEventSchemas is streamSession/streamAll's exemption
// treatment (requests.yaml's `exempt` entries, SPEC §5.1): the hub does not
// exist before Phase 5, so there is no live SSE connection to round-trip,
// but every frame *shape* SPEC §5.1 documents is validated directly against
// components.schemas.StreamEvent — the same schema-conformance guarantee the
// request-table rows get, just without an HTTP round trip.
func TestConformance_StreamEventSchemas(t *testing.T) {
	doc, _ := loadSpec(t)
	streamEvent := doc.Components.Schemas["StreamEvent"].Value
	require.NotNil(t, streamEvent, "openapi.yaml must define components.schemas.StreamEvent")

	frames := map[string]map[string]any{
		"event": {
			"event_ref": conformKnownEventRef.Encode(), "seq": float64(918234), "id": "0192abcd-0000-0000-0000-000000000002",
			"ts": "2026-08-11T09:12:05.221Z", "session_id": conformSessionID, "prompt_id": "p_88f1",
			"kind": "tool.result", "event_name": "tool_result", "source": "otel_log", "vendor": "claude_code",
			"tool_name": "Edit", "tool_use_id": "toolu_01A", "decision": nil, "decision_source": nil,
			"tool_source": "builtin", "query_source": conformUnknownQuerySource, "model": nil, "tokens": nil, "cost": nil,
			"duration_ms": 180, "success": true, "error_type": nil, "agent_id": nil, "agent_type": nil,
			"permission_mode": "default", "file_path": "server/internal/store/postgres/store.go", "clock_skewed": false,
		},
		"session": {
			"id": conformSessionID, "status": "active", "turn_count": 12,
			"cost": map[string]any{
				"usd": 4.27, "reported_usd": 4.27, "estimated_usd": 0.0, "estimated_share": 0.0,
				"by_query_source": map[string]any{}, "dominant_query_source": "", "other_query_source_usd": 0.0,
			},
		},
		"stats":    {"events_per_sec": 42.1, "active_sessions": 3, "queue_depth": 0, "ingest_lag_ms": 180, "dropped_total": 0},
		"lag":      {"dropped": 3},
		"reset":    {"reason": "replay_window_exceeded", "from": "2026-08-11T09:00:00Z"},
		"shutdown": {},
	}

	for name, payload := range frames {
		t.Run("event: "+name, func(t *testing.T) {
			require.NoError(t, streamEvent.VisitJSON(payload), "the %q frame payload must validate against components.schemas.StreamEvent", name)
		})
	}
}
