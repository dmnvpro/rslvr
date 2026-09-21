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

// Runner serializes frame grabs and Frame-set analyses on a shared slot pool.
type Runner struct {
	cap  capture.Capturer
	ana  vision.Analyzer
	lim  *Limiter
	save *Saver

	timeout       time.Duration // per-job deadline
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

// Grabbed is one captured frame plus its identity and optional Shot path.
type Grabbed struct {
	Frame     vision.Frame
	SHA256    string
	SavedPath string // empty when saving is off or the Shot failed
}

// Grab captures a single frame: list → resolve → grab → optional Shot.
// The device is an exclusive resource, so grabs share the job pool with
// analyses; ErrBusy is returned immediately when no slot is free.
func (r *Runner) Grab(ctx context.Context, device string, save bool) (Grabbed, error) {
	release, ok := r.lim.TryAcquire()
	if !ok {
		return Grabbed{}, ErrBusy
	}
	defer release()

	jobID := NewID()
	r.setInflight(jobID)
	defer r.clearInflight()

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	g, err := r.grab(ctx, device, save)
	if err != nil {
		r.log.Info("grab failed", slog.String("job_id", jobID), slog.String("err", err.Error()))
	}
	return g, err
}

func (r *Runner) grab(ctx context.Context, device string, save bool) (Grabbed, error) {
	log := r.log

	devs, err := r.cap.List(ctx)
	if err != nil {
		return Grabbed{}, fmt.Errorf("list devices: %w", err)
	}
	dev, err := capture.Resolve(device, devs)
	if err != nil {
		return Grabbed{}, err
	}
	log.Debug("grabbing frame", slog.String("device", dev.ID))

	data, mime, err := r.cap.Grab(ctx, dev)
	if err != nil {
		return Grabbed{}, fmt.Errorf("grab from %s: %w", dev.ID, err)
	}

	g := Grabbed{Frame: vision.Frame{Data: data, MIME: mime}, SHA256: vision.HashImage(data)}
	if save && r.save != nil {
		if g.SavedPath, err = r.save.Save(data, mime, g.SHA256); err != nil {
			log.Warn("saving shot failed", slog.String("err", err.Error()))
		}
	}
	return g, nil
}

// Resolve analyzes an ordered Frame set as one question. ErrBusy is returned
// immediately when no slot is free.
func (r *Runner) Resolve(ctx context.Context, frames []vision.Frame, prompt string) (vision.Result, error) {
	release, ok := r.lim.TryAcquire()
	if !ok {
		return vision.Result{}, ErrBusy
	}
	defer release()

	jobID := NewID()
	r.setInflight(jobID)
	defer r.clearInflight()

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	res, err := r.resolve(ctx, frames, prompt)
	if err != nil {
		r.log.Info("job failed", slog.String("job_id", jobID), slog.String("err", err.Error()))
	}
	return res, err
}

func (r *Runner) resolve(ctx context.Context, frames []vision.Frame, prompt string) (vision.Result, error) {
	setHash := vision.HashFrames(frames)

	if res, ok := r.cached(setHash, prompt); ok {
		r.log.Debug("cache hit", slog.String("frames_sha256", setHash))
		return res, nil
	}

	budget, err := r.visionBudget(ctx)
	if err != nil {
		return vision.Result{}, err
	}
	actx, acancel := context.WithTimeout(ctx, budget)
	defer acancel()

	res, err := r.ana.Analyze(actx, frames)
	if err != nil {
		return vision.Result{}, err
	}
	res.ImageHash = setHash // the pipeline owns frame-set identity, not the analyzer
	r.store(setHash, prompt, res)
	return res, nil
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

// cacheKey includes the prompt: the same Frame set under a different prompt is
// a different question and must not reuse the answer. The frame-set hash
// already folds in every frame's bytes and MIME.
func (r *Runner) cacheKey(setHash, prompt string) string {
	return setHash + "|" + prompt
}

func (r *Runner) cached(setHash, prompt string) (vision.Result, bool) {
	if r.cacheTTL <= 0 {
		return vision.Result{}, false
	}
	key := r.cacheKey(setHash, prompt)
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

func (r *Runner) store(setHash, prompt string, res vision.Result) {
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
	r.cache[r.cacheKey(setHash, prompt)] = cacheEntry{res: res, expires: r.now().Add(r.cacheTTL)}
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
