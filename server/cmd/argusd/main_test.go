package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/config"
)

// --- m36: healthURL must correctly parse every ARGUS_HTTP_ADDR shape ------
// --- net.Listen accepts, including bracketed IPv6 forms, and route to ----
// --- whichever endpoint --endpoint selected (m37). ------------------------

func TestHealthURL(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		endpoint string
		want     string
	}{
		{"bind-all-shorthand", ":8080", "healthz", "http://localhost:8080/healthz"},
		{"explicit-bind-all", "0.0.0.0:8080", "healthz", "http://localhost:8080/healthz"},
		{
			name: "ipv6-unspecified-bracketed",
			// This is the m36 regression case: net.Listen accepts "[::]:8080"
			// (the IPv6 bind-all address) and the pre-fix strings.Cut(addr, ":")
			// split on the FIRST colon, yielding host "[" and port "]:8080" —
			// a URL that never connects, so the healthcheck always failed for
			// this valid address.
			addr:     "[::]:8080",
			endpoint: "healthz",
			want:     "http://localhost:8080/healthz",
		},
		{"loopback-hostname", "localhost:9090", "readyz", "http://localhost:9090/readyz"},
		{"explicit-ipv4", "127.0.0.1:8080", "readyz", "http://127.0.0.1:8080/readyz"},
		{
			name: "ipv6-explicit-bracketed",
			addr: "[2001:db8::1]:8080", endpoint: "healthz",
			want: "http://[2001:db8::1]:8080/healthz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, healthURL(tt.addr, tt.endpoint))
		})
	}
}

// --- m35: healthcheckHTTPAddr must resolve ARGUS_HTTP_ADDR without --------
// --- requiring ARGUS_DATABASE_URL, unlike the full config.Load merge it ---
// --- otherwise mirrors (defaults -> YAML file -> env, env wins). ----------

// withoutEnv unsets name for the duration of the test (restoring whatever
// was there before, if anything) — t.Setenv can only set a value, never
// remove one, and this test needs to prove behaviour in ARGUS_DATABASE_URL's
// ABSENCE, which is the m35 scenario ("a YAML-configured deployment with no
// DSN visible to this process").
func withoutEnv(t *testing.T, name string) {
	t.Helper()
	prev, had := os.LookupEnv(name)
	require.NoError(t, os.Unsetenv(name))
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(name, prev)
		}
	})
}

func TestHealthcheckHTTPAddr_DefaultsWithoutAnyConfig(t *testing.T) {
	withoutEnv(t, "ARGUS_DATABASE_URL")
	withoutEnv(t, "ARGUS_HTTP_ADDR")

	addr, err := healthcheckHTTPAddr("")
	require.NoError(t, err, "must not require ARGUS_DATABASE_URL (m35)")
	require.Equal(t, ":8080", addr)
}

func TestHealthcheckHTTPAddr_EnvOverridesDefault(t *testing.T) {
	withoutEnv(t, "ARGUS_DATABASE_URL")
	t.Setenv("ARGUS_HTTP_ADDR", "127.0.0.1:9091")

	addr, err := healthcheckHTTPAddr("")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9091", addr)
}

func TestHealthcheckHTTPAddr_YAMLFileOverridesDefaultAndEnvWinsOverYAML(t *testing.T) {
	withoutEnv(t, "ARGUS_DATABASE_URL")
	withoutEnv(t, "ARGUS_HTTP_ADDR")

	path := filepath.Join(t.TempDir(), "argus.yaml")
	require.NoError(t, os.WriteFile(path, []byte("http_addr: \"0.0.0.0:7070\"\n"), 0o600))

	addr, err := healthcheckHTTPAddr(path)
	require.NoError(t, err, "must not require ARGUS_DATABASE_URL even with a YAML file present (m35)")
	require.Equal(t, "0.0.0.0:7070", addr)

	// env still wins over the YAML file, matching config.Load's precedence.
	t.Setenv("ARGUS_HTTP_ADDR", "0.0.0.0:6060")
	addr, err = healthcheckHTTPAddr(path)
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0:6060", addr)
}

// --- m35 (regression guard): confirm the problem this ticket fixes is ----
// --- real — config.Load itself DOES require ARGUS_DATABASE_URL, which is --
// --- exactly why runHealthcheck can no longer go through it directly. ----

func TestConfigLoad_StillRequiresDatabaseURL(t *testing.T) {
	withoutEnv(t, "ARGUS_DATABASE_URL")
	_, _, err := config.Load("")
	require.Error(t, err, "config.Load must still require ARGUS_DATABASE_URL for every OTHER subcommand — only the healthcheck path may skip it")
}

// --- m37: --endpoint accepts exactly healthz/readyz, defaulting to -------
// --- healthz, and rejects anything else without touching the network. ----

func TestRunHealthcheck_EndpointFlag(t *testing.T) {
	withoutEnv(t, "ARGUS_DATABASE_URL")

	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantPath string
	}{
		{"default-is-healthz", nil, 0, "/healthz"},
		{"explicit-healthz", []string{"--endpoint=healthz"}, 0, "/healthz"},
		{"explicit-readyz", []string{"--endpoint=readyz"}, 0, "/readyz"},
		{"invalid-endpoint-rejected-before-any-network-call", []string{"--endpoint=bogus"}, 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath = ""
			t.Setenv("ARGUS_HTTP_ADDR", addr)
			code := runHealthcheck(tt.args)
			require.Equal(t, tt.wantCode, code)
			if tt.wantPath != "" {
				require.Equal(t, tt.wantPath, gotPath)
			}
		})
	}
}

// --- m35 (end-to-end): runHealthcheck succeeds with no ARGUS_DATABASE_URL -
// --- set at all, proving the healthcheck subcommand no longer goes -------
// --- through config.Load's required-key validation.

func TestRunHealthcheck_SucceedsWithoutDatabaseURL(t *testing.T) {
	withoutEnv(t, "ARGUS_DATABASE_URL")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("ARGUS_HTTP_ADDR", strings.TrimPrefix(ts.URL, "http://"))
	require.Equal(t, 0, runHealthcheck(nil))
}
