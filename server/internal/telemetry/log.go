package telemetry

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/lmittmann/tint"
)

// NewLogger builds the process-wide slog.Logger.
//
// format is "json" (slog's built-in JSON handler, for production/compose logs)
// or "text" (a tint handler, for human-readable dev output). level is one of
// "debug", "info", "warn", "error" (case-insensitive).
func NewLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}

	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	case "text":
		handler = tint.NewTextHandler(w, &tint.Options{Level: lvl})
	default:
		return nil, fmt.Errorf("telemetry: unknown log format %q (want json or text)", format)
	}

	return slog.New(handler), nil
}

// ParseLevel parses a slog level name. It is exported so internal/config can
// validate ARGUS_LOG_LEVEL at startup without duplicating the level table.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("telemetry: unknown log level %q (want debug, info, warn, or error)", level)
	}
}
