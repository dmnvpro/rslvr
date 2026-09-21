package capture

import (
	"log/slog"
	"time"
)

// Options configures the ffmpeg capture backend.
type Options struct {
	// FFmpegPath is the ffmpeg binary; must be resolvable (checked at startup).
	FFmpegPath string
	// Warmup delays the frame read after the device opens so auto-exposure
	// settles (black-frame fix for capture cards).
	Warmup time.Duration
	// Log receives debug diagnostics; may be nil.
	Log *slog.Logger
}
