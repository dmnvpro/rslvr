package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"rslvr/internal/capture"
	"rslvr/internal/vision"
)

// maxCacheEntries bounds the in-memory answer cache between sweeps.
const maxCacheEntries = 256

// Runner executes single grab+analyze cycles.
type Runner struct {
	cap  capture.Capturer
	ana  vision.Analyzer
	lim  *Limiter
	save *Saver

	timeout       time.Duration // overall cycle deadline
	visionTimeout time.Duration // per-analysis budget
	cacheTTL      time.Duration // 0 disables the cache
	log           *slog.Logger

	now func() time.Time // injectable clock (cache TTL tests)

	mu       sync.Mutex
	inflight string
	cache    map[string]cacheEntry
}

// Options configures the runner.
type Options struct {
	Capturer      capture.Capturer
	Analyzer      vision.Analyzer
	Saver         *Saver // nil disables saving
	Timeout       time.Duration
	VisionTimeout time.Duration
	CacheTTL      time.Duration
	Log           *slog.Logger
}

// NewRunner builds the pipeline.
func NewRunner(o Options) *Runner {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		cap:           o.Capturer,
		ana:           o.Analyzer,
		lim:           NewLimiter(1),
		save:          o.Saver,
		timeout:       o.Timeout,
		visionTimeout: o.VisionTimeout,
		cacheTTL:      o.CacheTTL,
		log:           log,
		now:           time.Now,
		cache:         map[string]cacheEntry{},
	}
}

// SetLimiter replaces the default 1-slot limiter.
func (r *Runner) SetLimiter(l *Limiter) { r.lim = l }

// Status reports the current job state.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inflight == "" {
		return Status{State: "idle"}
	}
	return Status{State: "busy", Busy: true, JobID: r.inflight}
}

// Outcome is the result of a completed job plus the frame itself, so callers
// (web UI) can show a preview without a second grab.
type Outcome struct {
	Result vision.Result
	Frame  []byte // raw JPEG/PNG bytes; nil on cached answers
	MIME   string
}

// Run executes one full cycle: Grab → (save Shot) → Analyze.
// ErrBusy is returned immediately when no slot is free.
func (r *Runner) Run(ctx context.Context, req Request) (Outcome, error) {
	release, ok := r.lim.TryAcquire()
	if !ok {
		return Outcome{}, ErrBusy
	}
	defer release()

	jobID := NewID()
	r.setInflight(jobID)
	defer r.clearInflight()

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	out, err := r.pipeline(ctx, jobID, req)
	if err != nil {
		r.log.Info("job failed", slog.String("job_id", jobID), slog.String("err", err.Error()))
	}
	return out, err
}

func (r *Runner) pipeline(ctx context.Context, jobID string, req Request) (Outcome, error) {
	log := r.log.With(slog.String("job_id", jobID))

	devs, err := r.cap.List(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("list devices: %w", err)
	}
	dev, err := capture.Resolve(req.Device, devs)
	if err != nil {
		return Outcome{}, err
	}
	log.Debug("grabbing frame", slog.String("device", dev.ID))

	frame, mime, err := r.cap.Grab(ctx, dev)
	if err != nil {
		return Outcome{}, fmt.Errorf("grab from %s: %w", dev.ID, err)
	}

	hash := vision.HashImage(frame)
	var savedPath string
	if req.Save && r.save != nil {
		if savedPath, err = r.save.Save(frame, mime, hash); err != nil {
			log.Warn("saving shot failed", slog.String("err", err.Error()))
		}
	}

	if res, ok := r.cached(hash, mime, req.Prompt); ok {
		log.Debug("cache hit", slog.String("image_sha256", hash))
		res.SavedPath = savedPath
		// Frame is fresh anyway; returning it keeps the UI preview working.
		return Outcome{Result: res, Frame: frame, MIME: mime}, nil
	}

	budget, err := r.visionBudget(ctx)
	if err != nil {
		return Outcome{}, err
	}
	actx, acancel := context.WithTimeout(ctx, budget)
	defer acancel()

	res, err := r.ana.Analyze(actx, frame, mime)
	if err != nil {
		return Outcome{}, err
	}
	res.ImageHash = hash // the pipeline owns frame identity, not the analyzer
	res.SavedPath = savedPath
	r.store(hash, mime, req.Prompt, res)
	return Outcome{Result: res, Frame: frame, MIME: mime}, nil
}

// visionBudget reserves min(vision.timeout, remaining overall deadline).
// A deadline already spent is an error, not a doomed request.
func (r *Runner) visionBudget(ctx context.Context) (time.Duration, error) {
	budget := r.visionTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, fmt.Errorf("%w: overall cycle deadline exceeded before the vision call", capture.ErrTimeout)
		}
		if remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return 0, fmt.Errorf("%w: vision timeout must be positive", capture.ErrTimeout)
	}
	return budget, nil
}

// cacheKey includes mime and prompt: the same frame under a different prompt
// is a different question and must not reuse the answer.
func (r *Runner) cacheKey(hash, mime, prompt string) string {
	return hash + "|" + mime + "|" + prompt
}

func (r *Runner) cached(hash, mime, prompt string) (vision.Result, bool) {
	if r.cacheTTL <= 0 {
		return vision.Result{}, false
	}
	key := r.cacheKey(hash, mime, prompt)
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[key]
	if !ok {
		return vision.Result{}, false
	}
	if !r.now().Before(entry.expires) {
		delete(r.cache, key)
		return vision.Result{}, false
	}
	return entry.res, true
}

func (r *Runner) store(hash, mime, prompt string, res vision.Result) {
	if r.cacheTTL <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) >= maxCacheEntries {
		r.sweepLocked()
		if len(r.cache) >= maxCacheEntries {
			r.cache = map[string]cacheEntry{} // extreme churn; start fresh
		}
	}
	r.cache[r.cacheKey(hash, mime, prompt)] = cacheEntry{res: res, expires: r.now().Add(r.cacheTTL)}
}

func (r *Runner) sweepLocked() {
	now := r.now()
	for k, e := range r.cache {
		if !now.Before(e.expires) {
			delete(r.cache, k)
		}
	}
}

func (r *Runner) setInflight(id string) {
	r.mu.Lock()
	r.inflight = id
	r.mu.Unlock()
}

func (r *Runner) clearInflight() {
	r.mu.Lock()
	r.inflight = ""
	r.mu.Unlock()
}
