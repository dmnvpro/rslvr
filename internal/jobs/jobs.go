// Package jobs runs capture+analyze cycles: admission control (fixed slot
// pool, no queue), the frame pipeline, answer caching and shot saving.
package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"rslvr/internal/vision"
)

// ErrBusy means all job slots are taken; requests are rejected, never queued
// (settled decision — TechSpec's "queue" wording was dropped).
var ErrBusy = errors.New("another capture is already in progress")

// Limiter is a fixed-size non-blocking semaphore.
type Limiter struct {
	sem chan struct{}
}

// NewLimiter builds a limiter with n slots (n >= 1).
func NewLimiter(n int) *Limiter {
	if n < 1 {
		n = 1
	}
	return &Limiter{sem: make(chan struct{}, n)}
}

// TryAcquire takes a slot; ok=false means no slot is free right now.
// The returned func releases the slot (safe to call exactly once).
func (l *Limiter) TryAcquire() (release func(), ok bool) {
	select {
	case l.sem <- struct{}{}:
		released := false
		return func() {
			if !released {
				released = true
				<-l.sem
			}
		}, true
	default:
		return nil, false
	}
}

// Busy reports whether every slot is taken.
func (l *Limiter) Busy() bool {
	return len(l.sem) == cap(l.sem)
}

// Request is one job's parameters; empty fields fall back to config.
type Request struct {
	Device string // selector: "" | index | id | name substring
	Prompt string // resolved user prompt (config default or override)
	Save   bool
}

// Status is the current runner state (GET /api/status).
type Status struct {
	State string `json:"state"` // "idle" | "busy"
	Busy  bool   `json:"busy"`
	JobID string `json:"job_id,omitempty"`
}

// NewID produces a short random job id; overridable in tests.
var NewID = func() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

type cacheEntry struct {
	res     vision.Result
	expires time.Time
}
