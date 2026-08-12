// Package config implements Argus's configuration: defaults, optional YAML
// file, and ARGUS_-prefixed environment variables, merged in that order
// (env wins). See docs/SPEC.md §3.7 for the normative key table this package
// implements — every field below must have a matching row there, and the
// table-driven test in config_test.go enforces that via reflection so the
// two cannot silently drift apart.
package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config holds the fully-resolved Argus configuration. Field order matches
// the SPEC §3.7 table and drives `config --markdown` output order.
//
// Struct tags are the single source of truth for a field's environment
// variable name, default value, documentation note, and whether it must be
// redacted by `config --print`. config_test.go reflects over these tags to
// keep this struct and the SPEC table honest with each other.
type Config struct {
	HTTPAddr    string `env:"ARGUS_HTTP_ADDR" default:":8080" doc:"single listener for ingest + API + UI"`
	DatabaseURL string `env:"ARGUS_DATABASE_URL" default:"" doc:"required" secret:"true" required:"true"`

	DBMaxConns int `env:"ARGUS_DB_MAX_CONNS" default:"10" doc:"pgxpool"`

	AutoMigrate   bool          `env:"ARGUS_AUTO_MIGRATE" default:"true" doc:"run migrations at serve start (advisory-locked)"`
	ShutdownGrace time.Duration `env:"ARGUS_SHUTDOWN_GRACE" default:"15s" doc:"graceful-shutdown budget (§3.8)"`

	IngestQueue                   int           `env:"ARGUS_INGEST_QUEUE" default:"1024" doc:"batches"`
	IngestWorkers                 int           `env:"ARGUS_INGEST_WORKERS" default:"4" doc:""`
	IngestBatchSize               int           `env:"ARGUS_INGEST_BATCH_SIZE" default:"500" doc:"events"`
	IngestFlush                   time.Duration `env:"ARGUS_INGEST_FLUSH" default:"250ms" doc:""`
	IngestMaxBodyBytes            int64         `env:"ARGUS_INGEST_MAX_BODY_BYTES" default:"8388608" doc:"decompressed"`
	IngestRetryConflict           int           `env:"ARGUS_INGEST_RETRY_CONFLICT" default:"8" doc:"deadlock/serialization attempts"`
	IngestRetryTransient          int           `env:"ARGUS_INGEST_RETRY_TRANSIENT" default:"3" doc:"connection-error attempts"`
	IngestHookAllowMessageDisplay bool          `env:"ARGUS_INGEST_HOOK_ALLOW_MESSAGE_DISPLAY" default:"false" doc:"§1.5.2"`
	IngestToken                   string        `env:"ARGUS_INGEST_TOKEN" default:"" doc:"ingest auth seam" secret:"true"`
	APIToken                      string        `env:"ARGUS_API_TOKEN" default:"" doc:"read-API auth seam" secret:"true"`

	RetentionRawDays     int `env:"ARGUS_RETENTION_RAW_DAYS" default:"90" doc:"DECISIONS.md; also the clock-clamp lower bound (§1.2)"`
	RetentionSessionDays int `env:"ARGUS_RETENTION_SESSION_DAYS" default:"0" doc:"0 = never"`
	RetentionHour        int `env:"ARGUS_RETENTION_HOUR" default:"4" doc:"local hour for the daily job"`
	AttrsRetentionDays   int `env:"ARGUS_ATTRS_RETENTION_DAYS" default:"0" doc:"0 = keep attrs for the full raw retention (OQ-5)"`

	DedupWindow time.Duration `env:"ARGUS_DEDUP_WINDOW" default:"7d" doc:"ingest_dedup retention = the exact-dedup guarantee"`

	RollupInterval         time.Duration `env:"ARGUS_ROLLUP_INTERVAL" default:"60s" doc:""`
	RollupMaxBuckets       int           `env:"ARGUS_ROLLUP_MAX_BUCKETS" default:"200" doc:"per run"`
	RollupSessionRemarkMax int           `env:"ARGUS_ROLLUP_SESSION_REMARK_MAX" default:"720" doc:"cap on buckets re-dirtied by a late project change"`

	SweepInterval      time.Duration `env:"ARGUS_SWEEP_INTERVAL" default:"60s" doc:"abandoned-session sweep"`
	SessionIdleTimeout time.Duration `env:"ARGUS_SESSION_IDLE_TIMEOUT" default:"15m" doc:"active→abandoned boundary"`

	StreamBuffer         int           `env:"ARGUS_STREAM_BUFFER" default:"256" doc:"per-subscriber channel"`
	StreamHeartbeat      time.Duration `env:"ARGUS_STREAM_HEARTBEAT" default:"15s" doc:""`
	StreamReplayWindow   time.Duration `env:"ARGUS_STREAM_REPLAY_WINDOW" default:"5m" doc:"SSE replay bound (also bounds the ts predicate)"`
	StreamReplayMax      int           `env:"ARGUS_STREAM_REPLAY_MAX" default:"2000" doc:"events per reconnect"`
	StreamMaxSubscribers int           `env:"ARGUS_STREAM_MAX_SUBSCRIBERS" default:"100" doc:"503 beyond it"`

	LogLevel  string `env:"ARGUS_LOG_LEVEL" default:"info" doc:"slog"`
	LogFormat string `env:"ARGUS_LOG_FORMAT" default:"json" doc:"tint handler for text"`

	CORSOrigins string `env:"ARGUS_CORS_ORIGINS" default:"" doc:"needed only for pnpm dev on :5173"`
	UIEnabled   bool   `env:"ARGUS_UI_ENABLED" default:"true" doc:"serve the embedded SPA"`
}

// fieldSpec is the reflected view of one Config struct field, derived from
// its tags. It is the shared foundation for loading, validating, printing,
// and documenting the config.
type fieldSpec struct {
	goName   string
	env      string
	koanfKey string
	def      string
	doc      string
	secret   bool
	required bool
	index    int
	kind     reflect.Kind
	isDur    bool
}

// koanfKeyFromEnv derives the flat koanf key for an env var name, e.g.
// "ARGUS_HTTP_ADDR" -> "http_addr".
func koanfKeyFromEnv(env string) string {
	return strings.ToLower(strings.TrimPrefix(env, "ARGUS_"))
}

// schema reflects over the Config struct once and returns its field
// specs in declaration order. It is the single place that reads struct
// tags, so Load, Print, and Markdown all agree with each other and with
// the table-driven test.
func schema() []fieldSpec {
	t := reflect.TypeOf(Config{})
	specs := make([]fieldSpec, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		env := f.Tag.Get("env")
		if env == "" {
			panic(fmt.Sprintf("config: field %s has no env tag", f.Name))
		}
		isDur := f.Type == reflect.TypeOf(time.Duration(0))
		kind := f.Type.Kind()
		if isDur {
			kind = reflect.Int64 // duration is backed by int64, but treated specially below
		}
		specs = append(specs, fieldSpec{
			goName:   f.Name,
			env:      env,
			koanfKey: koanfKeyFromEnv(env),
			def:      f.Tag.Get("default"),
			doc:      f.Tag.Get("doc"),
			secret:   f.Tag.Get("secret") == "true",
			required: f.Tag.Get("required") == "true",
			index:    i,
			kind:     kind,
			isDur:    isDur,
		})
	}
	return specs
}

// parseDuration extends time.ParseDuration with a "d" (day) unit, since
// ARGUS_DEDUP_WINDOW's default (7d) is more naturally expressed in days than
// hours. A bare "<N>d" is the only day form accepted; anything else is
// delegated to time.ParseDuration unchanged.
func parseDuration(s string) (time.Duration, error) {
	if n := len(s); n > 1 && s[n-1] == 'd' {
		if days, err := strconv.ParseFloat(s[:n-1], 64); err == nil {
			return time.Duration(days * 24 * float64(time.Hour)), nil
		}
	}
	return time.ParseDuration(s)
}

// Load resolves the Config from defaults, then the optional YAML file at
// configPath (if non-empty), then ARGUS_-prefixed environment variables
// (highest priority). It returns the resolved config, a list of warnings
// (currently: unknown ARGUS_* env vars, so typos are caught instead of
// silently ignored), and an error if a value is malformed or a required key
// is missing.
func Load(configPath string) (*Config, []string, error) {
	return load(configPath, os.Environ)
}

func load(configPath string, environ func() []string) (*Config, []string, error) {
	specs := schema()

	k := koanf.New(".")

	defaults := make(map[string]any, len(specs))
	for _, s := range specs {
		defaults[s.koanfKey] = s.def
	}
	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
		return nil, nil, fmt.Errorf("config: loading defaults: %w", err)
	}

	if configPath != "" {
		if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
			return nil, nil, fmt.Errorf("config: loading %s: %w", configPath, err)
		}
	}

	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: "ARGUS_",
		TransformFunc: func(key, value string) (string, any) {
			return koanfKeyFromEnv(key), value
		},
		EnvironFunc: environ,
	}), nil); err != nil {
		return nil, nil, fmt.Errorf("config: loading environment: %w", err)
	}

	cfg := &Config{}
	v := reflect.ValueOf(cfg).Elem()

	for _, s := range specs {
		raw := k.String(s.koanfKey)
		field := v.Field(s.index)

		switch {
		case s.isDur:
			d, err := parseDuration(raw)
			if err != nil {
				return nil, nil, fmt.Errorf("config: %s: invalid duration %q: %w", s.env, raw, err)
			}
			field.SetInt(int64(d))
		case s.kind == reflect.String:
			field.SetString(raw)
		case s.kind == reflect.Bool:
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, nil, fmt.Errorf("config: %s: invalid bool %q: %w", s.env, raw, err)
			}
			field.SetBool(b)
		case s.kind == reflect.Int, s.kind == reflect.Int64:
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("config: %s: invalid integer %q: %w", s.env, raw, err)
			}
			field.SetInt(n)
		default:
			return nil, nil, fmt.Errorf("config: %s: unsupported field kind %s", s.env, s.kind)
		}
	}

	if err := cfg.validate(specs); err != nil {
		return nil, nil, err
	}

	return cfg, unknownEnvWarnings(specs, environ), nil
}

// validate checks required fields and value constraints that a plain
// type-parse cannot express.
func (c *Config) validate(specs []fieldSpec) error {
	for _, s := range specs {
		if s.required && s.goName == "DatabaseURL" && c.DatabaseURL == "" {
			return fmt.Errorf("config: %s is required", s.env)
		}
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		return fmt.Errorf("config: ARGUS_LOG_FORMAT: invalid value %q (want json or text)", c.LogFormat)
	}
	if _, err := parseLevelName(c.LogLevel); err != nil {
		return fmt.Errorf("config: ARGUS_LOG_LEVEL: %w", err)
	}
	return nil
}

// parseLevelName validates a log level name without importing
// internal/telemetry (config must not depend on telemetry, and telemetry
// must not depend on config, to keep the two seams independent).
func parseLevelName(level string) (string, error) {
	switch strings.ToLower(level) {
	case "debug", "info", "warn", "warning", "error":
		return strings.ToLower(level), nil
	default:
		return "", fmt.Errorf("invalid value %q (want debug, info, warn, or error)", level)
	}
}

// unknownEnvWarnings scans the process environment for ARGUS_-prefixed
// variables that do not match any known config key, so a typo (e.g.
// ARGUS_HTTTP_ADDR) is surfaced instead of silently ignored.
func unknownEnvWarnings(specs []fieldSpec, environ func() []string) []string {
	known := make(map[string]bool, len(specs))
	for _, s := range specs {
		known[s.env] = true
	}

	var warnings []string
	for _, kv := range environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "ARGUS_") {
			continue
		}
		if !known[name] {
			warnings = append(warnings, fmt.Sprintf("unknown environment variable %s (not a recognized ARGUS_* config key)", name))
		}
	}
	sort.Strings(warnings)
	return warnings
}

// Print renders the effective config as sorted "KEY=value" lines, with
// secret fields (ARGUS_DATABASE_URL, ARGUS_INGEST_TOKEN, ARGUS_API_TOKEN)
// redacted so `config --print` output is safe to paste into a ticket or chat.
func (c *Config) Print() string {
	specs := schema()
	v := reflect.ValueOf(c).Elem()

	lines := make([]string, 0, len(specs))
	for _, s := range specs {
		value := formatValue(v.Field(s.index), s)
		if s.secret && value != "" {
			value = "REDACTED"
		}
		lines = append(lines, fmt.Sprintf("%s=%s", s.env, value))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func formatValue(v reflect.Value, s fieldSpec) string {
	switch {
	case s.isDur:
		return time.Duration(v.Int()).String()
	case s.kind == reflect.String:
		return v.String()
	case s.kind == reflect.Bool:
		return strconv.FormatBool(v.Bool())
	default:
		return strconv.FormatInt(v.Int(), 10)
	}
}

// Markdown renders the SPEC §3.7 reference table from the struct schema
// itself (not from a resolved Config instance), so `docs/OPERATIONS.md` can
// embed it and CI can check it never drifts from the code.
func Markdown() string {
	specs := schema()

	var b strings.Builder
	b.WriteString("| Key / env | Default | Notes |\n")
	b.WriteString("|---|---|---|\n")
	for _, s := range specs {
		def := "`" + s.def + "`"
		switch {
		case s.required:
			def = "—"
		case s.def == "":
			def = "empty"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", s.env, def, s.doc)
	}
	return b.String()
}
