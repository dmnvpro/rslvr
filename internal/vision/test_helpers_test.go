package vision

import (
	"io"
	"log/slog"
)

// newTestLogger returns a debug-level logger writing to w (default stderr).
func newTestLogger(w io.Writer) *slog.Logger {
	if w == nil {
		return slog.Default()
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
