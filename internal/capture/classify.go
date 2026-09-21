package capture

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// failurePatterns maps ffmpeg stderr fragments to sentinel errors, ordered by
// specificity. Pinned by tests; extend when new device families show up.
var failurePatterns = []struct {
	patterns []string // lower-cased fragments, any-of match
	err      error
}{
	{
		patterns: []string{
			"device or resource busy", "ebusy", "permission denied",
			"access denied", "being used by another application",
		},
		err: ErrDeviceBusy,
	},
	{
		patterns: []string{
			"no such device", "invalid argument", "input/output error",
			"select timeout", "vidioc_dqbuf", "no signal",
			"could not read video", "could not open video capture device",
		},
		err: ErrNoSignal,
	},
}

// ClassifyGrabFailure converts an ffmpeg exit failure plus stderr into the
// package sentinel errors. gotBytes tells whether any frame data made it out.
func ClassifyGrabFailure(exitErr error, stderr string, gotBytes int) error {
	tail := stderrTail(stderr)
	low := strings.ToLower(stderr)
	for _, p := range failurePatterns {
		if containsAny(low, p.patterns) {
			return fmt.Errorf("%w: %s", p.err, tail)
		}
	}
	if errors.Is(exitErr, context.DeadlineExceeded) || errors.Is(exitErr, context.Canceled) {
		return fmt.Errorf("%w: %s", ErrTimeout, tail)
	}
	if gotBytes == 0 {
		if exitErr != nil {
			return fmt.Errorf("ffmpeg produced no data: %s", tail)
		}
		return fmt.Errorf("%w: ffmpeg exited cleanly without a frame", ErrNoSignal)
	}
	return fmt.Errorf("ffmpeg failed: %s", tail)
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// stderrTail trims noisy ffmpeg diagnostics to a human-readable chunk.
func stderrTail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	tail := strings.TrimSpace(strings.Join(lines, "; "))
	const maxTail = 400
	if len(tail) > maxTail {
		tail = tail[len(tail)-maxTail:]
	}
	return tail
}
