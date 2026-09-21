// Package httpx wraps HTTP calls with the retry policy of TechSpec §8:
// retry only 429 / 5xx / network errors, exponential backoff with jitter,
// honoring Retry-After.
package httpx

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// RetryConfig configures Do. MaxRetries counts additional attempts after the
// first one, so MaxRetries=3 means up to 4 requests.
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	// Rand is the jitter source; nil means math/rand/v2.
	Rand func() float64
}

// retryableStatus reports whether an HTTP status is worth another attempt.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// Do runs attempt, retrying per the policy. It returns the last response
// (which the caller must close) and error. Responses of failed attempts are
// closed here; the returned response belongs to the caller.
func (c RetryConfig) Do(ctx context.Context, attempt func(context.Context) (*http.Response, error)) (*http.Response, error) {
	rng := c.Rand
	if rng == nil {
		rng = rand.Float64
	}
	var lastResp *http.Response
	for n := 0; ; n++ {
		resp, err := attempt(ctx)

		if err == nil && !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		// Cancellation must never wait in a backoff sleep.
		if ctx.Err() != nil {
			closeBody(resp)
			return nil, ctx.Err()
		}
		if n >= c.MaxRetries {
			return resp, err
		}

		if resp != nil && lastResp != nil && lastResp != resp {
			closeBody(lastResp)
		}
		lastResp = resp

		if wait := c.delayFor(n, resp, rng); wait > 0 {
			if !sleepCtx(ctx, wait) {
				closeBody(resp)
				closeBody(lastResp)
				return nil, ctx.Err()
			}
		}
	}
}

// delayFor computes the wait before attempt n+1 (0-based n of the failed one).
func (c RetryConfig) delayFor(n int, resp *http.Response, rng func() float64) time.Duration {
	if resp != nil {
		if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
			return ra
		}
	}
	base := c.BaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	capDelay := c.MaxDelay
	if capDelay <= 0 {
		capDelay = 10 * time.Second
	}
	d := base << n // exponential: base * 2^n
	if d > capDelay || d <= 0 {
		d = capDelay
	}
	// Full jitter in [d/2, d).
	half := d / 2
	return half + time.Duration(rng()*float64(half))
}

// parseRetryAfter understands seconds and HTTP-date forms; negative/invalid → 0.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// sleepCtx waits for d; false means ctx was canceled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
}
