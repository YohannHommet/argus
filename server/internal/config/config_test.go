package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// expectedDefault is what SPEC.md §3.7 documents for one key. It is the
// hand-transcribed source of truth from the spec table (lines 1181-1216);
// everything else in this test file is derived from the Config struct via
// reflection, so the two can be compared without either one "faking" the
// other.
type expectedDefault struct {
	def      string
	required bool
}

// wantDefaults mirrors SPEC.md §3.7 verbatim. If a field is added to Config
// without a row here (or vice versa), TestSchemaMatchesSpecTable fails.
var wantDefaults = map[string]expectedDefault{
	"ARGUS_HTTP_ADDR":                         {def: ":8080"},
	"ARGUS_DATABASE_URL":                      {def: "", required: true},
	"ARGUS_DB_MAX_CONNS":                      {def: "10"},
	"ARGUS_AUTO_MIGRATE":                      {def: "true"},
	"ARGUS_SHUTDOWN_GRACE":                    {def: "15s"},
	"ARGUS_INGEST_QUEUE":                      {def: "1024"},
	"ARGUS_INGEST_WORKERS":                    {def: "4"},
	"ARGUS_INGEST_BATCH_SIZE":                 {def: "500"},
	"ARGUS_INGEST_FLUSH":                      {def: "250ms"},
	"ARGUS_INGEST_MAX_BODY_BYTES":             {def: "8388608"},
	"ARGUS_INGEST_RETRY_CONFLICT":             {def: "8"},
	"ARGUS_INGEST_RETRY_TRANSIENT":            {def: "3"},
	"ARGUS_INGEST_HOOK_ALLOW_MESSAGE_DISPLAY": {def: "false"},
	"ARGUS_INGEST_TOKEN":                      {def: ""},
	"ARGUS_API_TOKEN":                         {def: ""},
	"ARGUS_RETENTION_RAW_DAYS":                {def: "90"},
	"ARGUS_RETENTION_SESSION_DAYS":            {def: "0"},
	"ARGUS_RETENTION_HOUR":                    {def: "4"},
	"ARGUS_ATTRS_RETENTION_DAYS":              {def: "0"},
	"ARGUS_DEDUP_WINDOW":                      {def: "7d"},
	"ARGUS_ROLLUP_INTERVAL":                   {def: "60s"},
	"ARGUS_ROLLUP_MAX_BUCKETS":                {def: "200"},
	"ARGUS_ROLLUP_SESSION_REMARK_MAX":         {def: "720"},
	"ARGUS_SWEEP_INTERVAL":                    {def: "60s"},
	"ARGUS_SESSION_IDLE_TIMEOUT":              {def: "15m"},
	"ARGUS_STREAM_BUFFER":                     {def: "256"},
	"ARGUS_STREAM_HEARTBEAT":                  {def: "15s"},
	"ARGUS_STREAM_REPLAY_WINDOW":              {def: "5m"},
	"ARGUS_STREAM_REPLAY_MAX":                 {def: "2000"},
	"ARGUS_STREAM_MAX_SUBSCRIBERS":            {def: "100"},
	"ARGUS_LOG_LEVEL":                         {def: "info"},
	"ARGUS_LOG_FORMAT":                        {def: "json"},
	"ARGUS_CORS_ORIGINS":                      {def: ""},
	"ARGUS_UI_ENABLED":                        {def: "true"},
}

// TestSchemaMatchesSpecTable enumerates the Config struct via reflection and
// cross-checks it against wantDefaults: every struct field must have a row
// here with the documented default, and every row here must correspond to a
// struct field. Adding/removing/renaming a field without updating this map
// (or the map without the struct) fails the test either way.
func TestSchemaMatchesSpecTable(t *testing.T) {
	specs := schema()

	got := make(map[string]expectedDefault, len(specs))
	for _, s := range specs {
		got[s.env] = expectedDefault{def: s.def, required: s.required}
	}

	gotKeys := keys(got)
	wantKeys := keys(wantDefaults)
	require.Equal(t, wantKeys, gotKeys, "struct fields and SPEC §3.7 table must name exactly the same keys")

	for k, want := range wantDefaults {
		g, ok := got[k]
		require.Truef(t, ok, "missing struct field for %s", k)
		require.Equalf(t, want.def, g.def, "%s: default mismatch", k)
		require.Equalf(t, want.required, g.required, "%s: required mismatch", k)
	}
}

func keys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// environOf builds an EnvironFunc-compatible slice ("KEY=value") from a map,
// isolated from the real process environment so tests never depend on (or
// pollute) os.Environ.
func environOf(vars map[string]string) func() []string {
	return func() []string {
		out := make([]string, 0, len(vars))
		for k, v := range vars {
			out = append(out, k+"="+v)
		}
		return out
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, warnings, err := load("", environOf(map[string]string{
		"ARGUS_DATABASE_URL": "postgres://localhost/argus",
	}))
	require.NoError(t, err)
	require.Empty(t, warnings)

	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, 10, cfg.DBMaxConns)
	require.True(t, cfg.AutoMigrate)
	require.Equal(t, 15*time.Second, cfg.ShutdownGrace)
	require.Equal(t, 7*24*time.Hour, cfg.DedupWindow)
	require.Equal(t, "json", cfg.LogFormat)
	require.True(t, cfg.UIEnabled)
}

func TestLoadMissingDatabaseURL(t *testing.T) {
	_, _, err := load("", environOf(map[string]string{}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ARGUS_DATABASE_URL")
}

func TestLoadInvalidDuration(t *testing.T) {
	_, _, err := load("", environOf(map[string]string{
		"ARGUS_DATABASE_URL":   "postgres://localhost/argus",
		"ARGUS_SHUTDOWN_GRACE": "not-a-duration",
	}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ARGUS_SHUTDOWN_GRACE")
	require.Contains(t, err.Error(), "invalid duration")
}

func TestLoadEnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join([]string{
		"http_addr: \":9090\"",
		"db_max_conns: 25",
		"database_url: \"postgres://from-yaml/argus\"",
	}, "\n")), 0o600))

	cfg, _, err := load(path, environOf(map[string]string{
		"ARGUS_DB_MAX_CONNS": "42",
	}))
	require.NoError(t, err)

	require.Equal(t, ":9090", cfg.HTTPAddr, "YAML overrides the default")
	require.Equal(t, 42, cfg.DBMaxConns, "env overrides YAML")
	require.Equal(t, "postgres://from-yaml/argus", cfg.DatabaseURL, "YAML satisfies the required key")
}

func TestLoadUnknownEnvWarning(t *testing.T) {
	_, warnings, err := load("", environOf(map[string]string{
		"ARGUS_DATABASE_URL": "postgres://localhost/argus",
		"ARGUS_HTTTP_ADDR":   ":9999", // typo'd key
	}))
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "ARGUS_HTTTP_ADDR")
}

// TestLoadReservedTestEnvPrefixIgnored pins the CI regression that broke the
// end-to-end test: ci.yml exports ARGUS_TEST_DATABASE_URL for the integration
// harness, and strict unknown-ARGUS_*-variable validation flagged it as a
// typo. The whole ARGUS_TEST_ namespace is reserved for the harness, so any
// future harness variable is ignored too — while everything outside it stays
// strict, because that strictness is what catches a real typo.
func TestLoadReservedTestEnvPrefixIgnored(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantWarnings []string
	}{
		{
			name: "the exact variable CI exports is ignored",
			env: map[string]string{
				"ARGUS_DATABASE_URL":      "postgres://localhost/argus",
				"ARGUS_TEST_DATABASE_URL": "postgres://localhost:55433/argus",
			},
			wantWarnings: nil,
		},
		{
			name: "any other reserved-prefix variable is ignored too",
			env: map[string]string{
				"ARGUS_DATABASE_URL":          "postgres://localhost/argus",
				"ARGUS_TEST_SOMETHING_FUTURE": "1",
				"ARGUS_TEST_DATABASE_URL":     "postgres://localhost:55433/argus",
			},
			wantWarnings: nil,
		},
		{
			name: "a genuinely unknown variable is still reported",
			env: map[string]string{
				"ARGUS_DATABASE_URL": "postgres://localhost/argus",
				"ARGUS_BOGUS":        "nope",
			},
			wantWarnings: []string{"ARGUS_BOGUS"},
		},
		{
			name: "reserved prefix does not mask a real typo alongside it",
			env: map[string]string{
				"ARGUS_DATABASE_URL":      "postgres://localhost/argus",
				"ARGUS_TEST_DATABASE_URL": "postgres://localhost:55433/argus",
				"ARGUS_HTTTP_ADDR":        ":9999",
			},
			wantWarnings: []string{"ARGUS_HTTTP_ADDR"},
		},
		{
			name: "a bare ARGUS_TEST_ prefix with no suffix is still reserved",
			env: map[string]string{
				"ARGUS_DATABASE_URL": "postgres://localhost/argus",
				"ARGUS_TEST_":        "1",
			},
			wantWarnings: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, warnings, err := load("", environOf(tc.env))
			require.NoError(t, err)
			require.Len(t, warnings, len(tc.wantWarnings))
			for i, want := range tc.wantWarnings {
				require.Contains(t, warnings[i], want)
			}
		})
	}
}

// TestNoConfigKeyUsesReservedPrefix enforces the other half of the contract:
// the reserved namespace is only safe to skip if no real config key can ever
// live inside it. Reflection over the schema means a future field added under
// ARGUS_TEST_ fails here rather than being silently unreadable from the
// environment.
func TestNoConfigKeyUsesReservedPrefix(t *testing.T) {
	for _, s := range schema() {
		require.False(t, strings.HasPrefix(s.env, ReservedEnvPrefix),
			"config key %s must not use the harness-reserved %s prefix — unknownEnvWarnings skips that namespace, so such a key would be silently unvalidated", s.env, ReservedEnvPrefix)
	}
}

func TestPrintRedactsSecrets(t *testing.T) {
	cfg, _, err := load("", environOf(map[string]string{ //nolint:gosec // test fixture credentials, not real secrets
		"ARGUS_DATABASE_URL": "postgres://user:pass@localhost/argus",
		"ARGUS_INGEST_TOKEN": "ingest-secret",
		"ARGUS_API_TOKEN":    "api-secret",
	}))
	require.NoError(t, err)

	out := cfg.Print()
	require.NotContains(t, out, "postgres://user:pass@localhost/argus")
	require.NotContains(t, out, "ingest-secret")
	require.NotContains(t, out, "api-secret")
	require.Contains(t, out, "ARGUS_DATABASE_URL=REDACTED")
	require.Contains(t, out, "ARGUS_INGEST_TOKEN=REDACTED")
	require.Contains(t, out, "ARGUS_API_TOKEN=REDACTED")
}

func TestMarkdownRoundTripsKeySet(t *testing.T) {
	md := Markdown()

	specs := schema()
	want := make([]string, 0, len(specs))
	for _, s := range specs {
		want = append(want, s.env)
	}
	sort.Strings(want)

	var got []string
	for _, line := range strings.Split(md, "\n") {
		if !strings.HasPrefix(line, "| `ARGUS_") {
			continue
		}
		fields := strings.SplitN(line, "|", 4)
		require.GreaterOrEqual(t, len(fields), 3)
		got = append(got, strings.Trim(strings.TrimSpace(fields[1]), "`"))
	}
	sort.Strings(got)

	require.Equal(t, want, got)
}

func TestParseDurationDays(t *testing.T) {
	d, err := parseDuration("7d")
	require.NoError(t, err)
	require.Equal(t, 7*24*time.Hour, d)

	d, err = parseDuration("250ms")
	require.NoError(t, err)
	require.Equal(t, 250*time.Millisecond, d)

	_, err = parseDuration("nope")
	require.Error(t, err)
}
