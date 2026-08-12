package sim

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Distribution constants transcribed verbatim from SPEC §7.1. Each is used
// exactly once, at its call site below, with a comment repeating the exact
// SPEC clause it implements — kept as named constants rather than inline
// literals so a future SPEC amendment is a one-line diff.
const (
	turnsMean, turnsMin, turnsMax = 6.0, 1, 20 // "1-20 turns (geometric, mean 6)"

	apiRequestsPerTurnMin, apiRequestsPerTurnMax = 1, 8  // "1-8 api_request events"
	toolCallsPerTurnMin, toolCallsPerTurnMax     = 0, 12 // "0-12 tool calls"

	pAPIRequestHasPromptID = 5.0 / 6.0 // "5 in 6 carry prompt.id"

	inputTokensMu, inputTokensSigma = 7.31322, 0.8 // ln(1500)
	outputTokensMu, outputTokensSig = 6.39693, 1.0 // ln(600)
	cacheReadMu, cacheReadSigma     = 10.5966, 1.2 // ln(40000)
	cacheReadP                      = 0.75
	cacheCreationMu, cacheCreatSig  = 8.00637, 1.0 // ln(3000)
	cacheCreationP                  = 0.3
	durationMu, durationSigma       = 8.29405, 0.7 // ln(4000)

	pAPIError   = 0.03
	pAPIRefusal = 0.01

	pToolDecisionAccept = 0.88
	pToolResultSuccess  = 0.93

	pSubagentPermissionGap = 0.4     // occasional PermissionRequest before a user_* decision (SPEC gives no exact number; see hook_events.go's hookPermissionRequest doc)
	permissionGapMu        = 8.00637 // ln(3000)
	permissionGapSigma     = 1.1

	hookExecDurMu, hookExecDurSigma = 2.0794, 0.9 // ln(8)

	pPermissionModeChanged = 0.02
	pFileChangedBurst      = 0.05
	pCompactPair           = 0.02

	pSessionEnd = 0.85 // "15% of sessions are abandoned with no end event"

	maxSubagentDepth = 2

	metricExportPeriod = 60 * time.Second // "Every 60s of simulated time"
)

// sessionResult is one fully-generated session: every log record, hook
// payload, and metric point it produced, each still carrying its own
// simulated timestamp so batch.go can group them by flush interval without
// re-deriving timing. Pure data — no protobuf encoding happens here (that
// is encode.go's job) and no I/O happens here (transport.go's job).
type sessionResult struct {
	SessionID string
	Identity  sessionIdentity
	Logs      []logEmission
	Hooks     []hookEmission
	Metrics   []metricEmission
}

type logEmission struct {
	TS  time.Time
	Rec *logspb.LogRecord
}

type hookEmission struct {
	TS      time.Time
	Payload map[string]any
}

type metricEmission struct {
	TS time.Time
	M  *metricspb.Metric
}

// sessionBuilder accumulates one session's emissions while walking SPEC
// §7.1's per-turn recipe. cursor is simulated-seconds-since-origin, always
// non-decreasing, so every timestamp this builder stamps is reproducible
// from (seed, sessionOrdinal, startOffset) alone.
type sessionBuilder struct {
	cfg    Config
	clock  Clock
	r      *sessionRNG
	id     sessionIdentity
	result sessionResult
	cursor time.Duration
	logSeq int64
}

// newSessionBuilder starts a session at startOffset (SPEC §7.2's
// backfill-spread start time, computed by runner.go) using the RNG derived
// from (seed, sessionOrdinal) (SPEC §7.2's determinism rule, rng.go).
func newSessionBuilder(cfg Config, clock Clock, seed uint64, sessionOrdinal int, startOffset time.Duration) *sessionBuilder {
	r := newSessionRNG(seed, sessionOrdinal)
	id := newSessionIdentity(r)
	return &sessionBuilder{
		cfg:    cfg,
		clock:  clock,
		r:      r,
		id:     id,
		cursor: startOffset,
		result: sessionResult{SessionID: id.sessionID, Identity: id},
	}
}

func (b *sessionBuilder) now() time.Time { return b.clock.At(b.cursor) }

// advance moves the cursor forward by d and returns the new "now" — every
// inter-event gap in the generator goes through this one function so the
// cursor is the single source of simulated time (mirrors clock.go's
// Clock being the single wall-clock mapping).
func (b *sessionBuilder) advance(d time.Duration) time.Time {
	if d > 0 {
		b.cursor += d
	}
	return b.now()
}

func (b *sessionBuilder) emitLog(rec *logspb.LogRecord, ts time.Time) {
	b.result.Logs = append(b.result.Logs, logEmission{TS: ts, Rec: rec})
}

func (b *sessionBuilder) emitHook(payload map[string]any, ts time.Time) {
	b.result.Hooks = append(b.result.Hooks, hookEmission{TS: ts, Payload: payload})
}

func (b *sessionBuilder) emitMetric(m *metricspb.Metric, ts time.Time) {
	b.result.Metrics = append(b.result.Metrics, metricEmission{TS: ts, M: m})
}

func (b *sessionBuilder) nextSeq() int64 {
	seq := b.logSeq
	b.logSeq++
	return seq
}

// generateSession implements SPEC §7.1 end to end for one virtual session
// and returns its full emission set. project selects the fixed project
// SPEC §7.1 draws from (projects.go); legacy-app sessions skip every log
// event (SPEC: "legacy-app is metrics-only") but still emit their
// session.count / periodic metrics, so the "logs exporter appears off"
// demo case has metric-only sessions to show.
func generateSession(cfg Config, clock Clock, sessionOrdinal int, startOffset time.Duration, project string) sessionResult {
	b := newSessionBuilder(cfg, clock, cfg.Seed, sessionOrdinal, startOffset)
	logsOnly := project != legacyAppProject
	cwd := "/home/dev/" + project

	startType := pick(b.r.Rand, startTypes)
	sessionStartTS := b.now()

	if logsOnly {
		b.emitHook(hookSessionStart(b.id.sessionID, cwd, "default", startType, "/home/dev/.claude/transcripts/"+b.id.sessionID+".jsonl"), sessionStartTS)
		b.emitLog(buildHookRegistered(b.id, sessionStartTS, b.nextSeq(), "SessionStart", "*", "command", "userSettings"), sessionStartTS)
	}
	b.emitMetric(buildSessionCountMetric(b.id.sessionID, uint64(b.cursor.Seconds()), startType), sessionStartTS)

	// nextMetricTick is relative to this session's own start (b.cursor,
	// already seeded with startOffset by newSessionBuilder), not to the
	// simulation origin: a demo session backfilled 14 days into the past
	// must not replay ~20000 catch-up ticks just to reach its own start.
	nextMetricTick := b.cursor + metricExportPeriod

	turns := geometricClamped(b.r.Rand, turnsMean, turnsMin, turnsMax)
	for t := 0; t < turns; t++ {
		b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
		promptID := fmt.Sprintf("prompt-%s-%04d", b.id.sessionID, t)

		if logsOnly {
			b.runTurn(promptID, cwd, 0, "")
		}

		for b.cursor >= nextMetricTick {
			b.emitPeriodicMetrics()
			nextMetricTick += metricExportPeriod
		}
	}

	if bernoulli(b.r.Rand, pSessionEnd) {
		endTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
		if logsOnly {
			b.emitHook(hookSessionEnd(b.id.sessionID, "clean_exit"), endTS)
		}
	}

	return b.result
}

// runTurn implements one full SPEC §7.1 item-2 turn: the UserPromptSubmit
// hook + user_prompt log event pair, 1-8 api_request calls, 0-12 tool
// calls, occasional auxiliary events, and the closing Stop hook +
// assistant_response. agentID/parentAgentID are "" at the top level and
// non-empty inside a subagent's nested mini-turn (depth > 0).
func (b *sessionBuilder) runTurn(promptID, cwd string, depth int, agentID string) {
	ts := b.now()
	b.emitHook(hookUserPromptSubmit(b.id.sessionID, promptID), ts)

	messageUUID := b.r.uuid().String()
	promptLength := uniformRange(b.r.Rand, 8, 400)
	b.emitLog(buildUserPrompt(b.id, ts, b.nextSeq(), promptID, promptLength, messageUUID), ts)

	hooksInTurn := 1 // UserPromptSubmit counted toward this turn's hook batch (SPEC: "hook_execution_start/_complete pair around every hook batch")

	nAPIRequests := uniformRange(b.r.Rand, apiRequestsPerTurnMin, apiRequestsPerTurnMax)
	for i := 0; i < nAPIRequests; i++ {
		b.emitAPIRequest(&promptID)
	}
	if bernoulli(b.r.Rand, pAPIError) {
		b.emitAPIError(&promptID)
	}
	if bernoulli(b.r.Rand, pAPIRefusal) {
		b.emitAPIRefusal(&promptID)
	}

	nToolCalls := uniformRange(b.r.Rand, toolCallsPerTurnMin, toolCallsPerTurnMax)
	for i := 0; i < nToolCalls; i++ {
		hooksInTurn += b.emitToolCall(promptID, cwd, depth, agentID)
	}

	if bernoulli(b.r.Rand, pPermissionModeChanged) {
		modeTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
		b.emitLog(buildPermissionModeChanged(b.id, modeTS, b.nextSeq(), "default", "acceptEdits"), modeTS)
	}
	if bernoulli(b.r.Rand, pFileChangedBurst) {
		fcTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
		b.emitHook(hookFileChanged(b.id.sessionID, promptID, cwd+"/file.go"), fcTS)
		hooksInTurn++
	}
	if bernoulli(b.r.Rand, pCompactPair) {
		preTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
		b.emitHook(hookPreCompact(b.id.sessionID, promptID), preTS)
		postTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
		b.emitHook(hookPostCompact(b.id.sessionID, promptID), postTS)
		hooksInTurn += 2
	}

	stopTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
	b.emitHook(hookStop(b.id.sessionID, promptID), stopTS)
	b.emitLog(buildAssistantResponse(b.id, stopTS, b.nextSeq(), promptID, b.r.uuid().String()), stopTS)
	hooksInTurn++

	b.emitHookExecutionPair(&promptID, "PostToolUse", hooksInTurn, stopTS)
}

// emitAPIRequest implements SPEC §7.1 item 2's api_request recipe exactly:
// the query_source mixed distribution (querysource.go), the "5 in 6 carry
// prompt.id … generate_session_title ones deliberately omit it" rule, and
// every named lognormal token/duration distribution.
func (b *sessionBuilder) emitAPIRequest(promptID *string) {
	ts := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
	qs := pick(b.r.Rand, querySources)

	effectivePromptID := promptID
	if qs == generateSessionTitleQuerySource {
		effectivePromptID = nil // SPEC §7.1: "deliberately omit it, exercising the out-of-turn path"
	} else if !bernoulli(b.r.Rand, pAPIRequestHasPromptID) {
		effectivePromptID = nil
	}

	model := pick(b.r.Rand, models)
	inputTok := int64(lognormal(b.r.Rand, inputTokensMu, inputTokensSigma))
	outputTok := int64(lognormal(b.r.Rand, outputTokensMu, outputTokensSig))
	var cacheRead, cacheCreation int64
	if bernoulli(b.r.Rand, cacheReadP) {
		cacheRead = int64(lognormal(b.r.Rand, cacheReadMu, cacheReadSigma))
	}
	if bernoulli(b.r.Rand, cacheCreationP) {
		cacheCreation = int64(lognormal(b.r.Rand, cacheCreationMu, cacheCreatSig))
	}
	durationMS := int64(lognormal(b.r.Rand, durationMu, durationSigma))
	micros := costMicros(model, inputTok, outputTok, cacheRead, cacheCreation)

	f := apiRequestFields{
		model:           model,
		inputTokens:     inputTok,
		outputTokens:    outputTok,
		cacheReadTokens: cacheRead,
		cacheCreation:   cacheCreation,
		durationMS:      durationMS,
		querySource:     qs,
		includeCost:     b.cfg.CostMode != CostModeOmit,
		costMicros:      micros,
		requestID:       "req_" + b.r.uuid().String(),
	}
	b.emitLog(buildAPIRequest(b.id, ts, b.nextSeq(), effectivePromptID, f), ts)
}

func (b *sessionBuilder) emitAPIError(promptID *string) {
	ts := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
	model := pick(b.r.Rand, models)
	qs := pick(b.r.Rand, querySources)
	durationMS := int64(lognormal(b.r.Rand, durationMu, durationSigma))
	b.emitLog(buildAPIError(b.id, ts, b.nextSeq(), promptID, model, durationMS, 529, "req_"+b.r.uuid().String(), qs), ts)
}

func (b *sessionBuilder) emitAPIRefusal(promptID *string) {
	ts := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
	model := pick(b.r.Rand, models)
	b.emitLog(buildAPIRefusal(b.id, ts, b.nextSeq(), promptID, model, "policy"), ts)
}

// toolFileTools is the subset of toolNames whose tool_result carries a
// file_path under --tool-details=1 (SPEC §1.5.1: "file_path =
// attrs.tool_parameters.file_path"), matching the "known file tools" the
// PreToolUse hook mapping row also calls out.
var toolFileTools = map[string]bool{"Read": true, "Edit": true, "Write": true, "Glob": true, "Grep": true}

// emitToolCall implements SPEC §7.1 item 2's per-tool-call recipe:
// PreToolUse hook -> tool_decision -> (if accepted) tool_result +
// PostToolUse hook, with the Task-tool subagent branch (depth-bounded) and
// the occasional PermissionRequest-before-user-decision gap. Returns the
// number of hook payloads it emitted, for the caller's
// hook_execution_start/_complete accounting.
func (b *sessionBuilder) emitToolCall(promptID, cwd string, depth int, agentID string) int {
	toolName := pick(b.r.Rand, toolNames)
	toolUseID := "toolu_" + b.r.uuid().String()
	hooksEmitted := 0

	decisionSource := pick(b.r.Rand, decisionSources)
	if strings.HasPrefix(decisionSource, "user_") && bernoulli(b.r.Rand, pSubagentPermissionGap) {
		prTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
		b.emitHook(hookPermissionRequest(b.id.sessionID, promptID, toolName), prTS)
		hooksEmitted++
		b.advance(lognormalDuration(b.r.Rand, permissionGapMu, permissionGapSigma)) // human-latency gap, SPEC §7.1
	}

	preTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
	b.emitHook(hookPreToolUse(b.id.sessionID, promptID, toolName, toolUseID, b.cfg.ToolUseIDInHooks, filePathFor(cwd, toolName), agentID), preTS)
	hooksEmitted++

	decisionTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
	decision := "accept"
	if !bernoulli(b.r.Rand, pToolDecisionAccept) {
		decision = "reject"
	}
	toolSource := pick(b.r.Rand, toolSources)
	b.emitLog(buildToolDecision(b.id, decisionTS, b.nextSeq(), &promptID, toolName, toolUseID, decision, decisionSource, toolSource, b.cfg.ToolUseIDInDecision), decisionTS)

	if decision != "accept" {
		return hooksEmitted
	}

	if toolName == "Task" && depth < maxSubagentDepth {
		// The spawned subagent's parent is the agent that is *running* this
		// tool call — agentID, which is "" at the top level so a directly
		// spawned subagent correctly reports no parent (SPEC §2.3: a NULL
		// parent_agent_id means the root/main agent). Passing the spawning
		// tool_use_id here instead fabricated a parent_agent_id that was not
		// an agent id at all, which both lied about the tree's root and made
		// a depth-2 tree impossible, because no child's parent_agent_id could
		// ever match another subagent's agent_id (Phase-2 exit criterion 7).
		b.runSubagent(promptID, cwd, depth+1, agentID)
	}

	success := bernoulli(b.r.Rand, pToolResultSuccess)
	resultTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
	f := toolResultFields{
		toolName:        toolName,
		toolUseID:       toolUseID,
		success:         success,
		durationMS:      int64(lognormal(b.r.Rand, durationMu, durationSigma)),
		inputSizeBytes:  int64(uniformRange(b.r.Rand, 16, 4096)),
		resultSizeBytes: int64(uniformRange(b.r.Rand, 16, 65536)),
		decisionSource:  decisionSource,
	}
	if !success {
		f.errType = "tool_execution_error"
	}
	if b.cfg.ToolDetails {
		if toolFileTools[toolName] {
			f.filePath = filePathFor(cwd, toolName)
		}
		if toolName == "Task" {
			f.subagentType = "explore"
		}
	}
	b.emitLog(buildToolResult(b.id, resultTS, b.nextSeq(), &promptID, f), resultTS)

	if success {
		b.emitHook(hookPostToolUse(b.id.sessionID, promptID, toolName, toolUseID, b.cfg.ToolUseIDInHooks, agentID), resultTS)
	} else {
		b.emitHook(hookPostToolUseFailure(b.id.sessionID, promptID, toolName, toolUseID, f.errType, b.cfg.ToolUseIDInHooks), resultTS)
	}
	hooksEmitted++

	return hooksEmitted
}

// filePathFor synthesizes a plausible file_path for --tool-details=1
// output, scoped under the session's cwd so the "file-touch view" demo
// data looks coherent (SPEC §1.5.1: "needs OTEL_LOG_TOOL_DETAILS=1 …
// which the quickstart sets").
func filePathFor(cwd, toolName string) string {
	return cwd + "/internal/" + toolName + ".go"
}

// runSubagent implements SPEC §7.1 item 2's "Task/Agent calls … emit
// SubagentStart, a nested mini-session whose api_requests carry
// query_source (no agent_id) and whose hook payloads carry
// agent_id/parent_agent_id, then SubagentStop." parentAgentID is the
// caller's own agent_id ("" at the top level, i.e. depth-1 subagents have
// no parent).
func (b *sessionBuilder) runSubagent(parentPromptID, cwd string, depth int, parentAgentID string) {
	agentID := "agent-" + b.r.uuid().String()
	startTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
	b.emitHook(hookSubagentStart(b.id.sessionID, parentPromptID, agentID, "explore", parentAgentID), startTS)

	subPromptID := fmt.Sprintf("%s-sub-%s", parentPromptID, agentID)
	nAPIRequests := uniformRange(b.r.Rand, 1, 3)
	for i := 0; i < nAPIRequests; i++ {
		b.emitAPIRequest(&subPromptID) // OTel side: no agent_id ever (fidelity rule, SPEC §1.9)
	}

	nToolCalls := uniformRange(b.r.Rand, 0, 3)
	for i := 0; i < nToolCalls; i++ {
		b.emitToolCall(subPromptID, cwd, depth, agentID)
	}

	success := bernoulli(b.r.Rand, pToolResultSuccess)
	stopTS := b.advance(lognormalDuration(b.r.Rand, durationMu, durationSigma))
	b.emitHook(hookSubagentStop(b.id.sessionID, parentPromptID, agentID, success), stopTS)
}

// emitHookExecutionPair implements SPEC §7.1 item 2's "hook_execution_start
// /_complete pair around every hook batch, with total_duration_ms drawn
// lognormal μ=ln(8) σ=0.9". numHooks is the count of hook payloads this
// turn produced (the "batch" the pair wraps).
func (b *sessionBuilder) emitHookExecutionPair(promptID *string, hookEvent string, numHooks int, ts time.Time) {
	startTS := ts
	b.emitLog(buildHookExecutionStart(b.id, startTS, b.nextSeq(), promptID, hookEvent, hookEvent, "userSettings", numHooks), startTS)

	totalDur := lognormal(b.r.Rand, hookExecDurMu, hookExecDurSigma)
	completeTS := b.advance(time.Duration(totalDur) * time.Millisecond)
	b.emitLog(buildHookExecutionComplete(b.id, completeTS, b.nextSeq(), promptID, hookEvent, hookEvent, "userSettings", numHooks, totalDur, numHooks), completeTS)
}

// emitPeriodicMetrics implements SPEC §7.1 item 4's 60s export: cost.usage,
// token.usage, lines_of_code.count, active_time.total,
// code_edit_tool.decision, and occasionally commit.count/pull_request.count.
func (b *sessionBuilder) emitPeriodicMetrics() {
	ts := b.now()
	seconds := uint64(b.cursor.Seconds())

	model := pick(b.r.Rand, models)
	qs := pick(b.r.Rand, querySources)
	usd := lognormal(b.r.Rand, -4, 1.0)
	b.emitMetric(buildCostUsageMetric(b.id.sessionID, seconds, model, qs, usd), ts)

	for _, tt := range []string{"input", "output", "cacheRead", "cacheCreation"} {
		b.emitMetric(buildTokenUsageMetric(b.id.sessionID, seconds, model, tt, int64(lognormal(b.r.Rand, inputTokensMu, inputTokensSigma))), ts)
	}

	b.emitMetric(buildLinesOfCodeMetric(b.id.sessionID, seconds, model, "added", int64(uniformRange(b.r.Rand, 0, 50))), ts)
	b.emitMetric(buildLinesOfCodeMetric(b.id.sessionID, seconds, model, "removed", int64(uniformRange(b.r.Rand, 0, 20))), ts)
	b.emitMetric(buildActiveTimeMetric(b.id.sessionID, seconds, "cli", metricExportPeriod.Seconds()), ts)

	decision := "accept"
	if !bernoulli(b.r.Rand, pToolDecisionAccept) {
		decision = "reject"
	}
	b.emitMetric(buildCodeEditToolDecisionMetric(b.id.sessionID, seconds, "Edit", decision, pick(b.r.Rand, decisionSources), "go"), ts)

	if bernoulli(b.r.Rand, 0.1) {
		b.emitMetric(buildCommitCountMetric(b.id.sessionID, seconds), ts)
	}
	if bernoulli(b.r.Rand, 0.03) {
		b.emitMetric(buildPullRequestCountMetric(b.id.sessionID, seconds), ts)
	}
}

// lognormalDuration draws a lognormal(mu, sigma) value in milliseconds and
// returns it as a time.Duration, used for every inter-event gap in the
// generator (SPEC §7.1's duration_ms lognormal shape doubles as the
// generator's own event-spacing model — there is no separately-specified
// "time between events" distribution in the SPEC, so reusing the
// documented duration_ms shape is the least-invented choice available).
func lognormalDuration(r *rand.Rand, mu, sigma float64) time.Duration {
	return time.Duration(lognormal(r, mu, sigma)) * time.Millisecond
}
