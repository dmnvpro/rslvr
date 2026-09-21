// Package logx builds the process logger and provides secret masking.
package logx

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// New builds a slog.Logger writing to stderr in the given format ("text"|"json")
// at the given level ("debug"|"info"|"warn"|"error").
func New(level, format string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		h = slog.NewTextHandler(os.Stderr, opts)
	case "json":
		h = slog.NewJSONHandler(os.Stderr, opts)
	default:
		return nil, fmt.Errorf("logx: unknown format %q (want text|json)", format)
	}
	return slog.New(h), nil
}

// ParseLevel maps a level name to a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("logx: unknown level %q (want debug|info|warn|error)", s)
}

// MaskKey returns a safely loggable form of an API key, e.g. "sk-***abc".
func MaskKey(key string) string {
	switch {
	case key == "":
		return ""
	case len(key) <= 6:
		return "***"
	}
	return key[:3] + "***" + key[len(key)-3:]
}
