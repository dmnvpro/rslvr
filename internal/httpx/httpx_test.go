package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// bodyString drains and closes the response, returning its body.
func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp == nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestRetry_On429Then200(t *testing.T) {
	calls := 0
	cfg := RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond, Rand: func() float64 { return 0 }}
	resp, err := cfg.Do(context.Background(), func(_ context.Context) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("slow down"))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if got := bodyString(t, resp); got != "ok" {
		t.Errorf("body = %q", got)
	}
}

func TestRetry_On500Then200(t *testing.T) {
	calls := 0
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond}
	resp, err := cfg.Do(context.Background(), func(_ context.Context) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("boom"))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if got := bodyString(t, resp); got != "ok" {
		t.Errorf("body = %q", got)
	}
}

func TestNoRetry_OnClientErrors(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 422} {
		calls := 0
		cfg := RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}
		resp, _ := cfg.Do(context.Background(), func(_ context.Context) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		if calls != 1 {
			t.Errorf("status %d: calls = %d, want 1", code, calls)
		}
		if resp == nil || resp.StatusCode != code {
			t.Errorf("status %d: response lost", code)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
}

func TestRetry_NetworkErrorThenSuccess(t *testing.T) {
	calls := 0
	netErr := errors.New("connection reset")
	cfg := RetryConfig{MaxRetries: 1, BaseDelay: time.Millisecond}
	resp, err := cfg.Do(context.Background(), func(_ context.Context) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, netErr
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	bodyString(t, resp)
}

func TestRetry_ExhaustionReturnsLastError(t *testing.T) {
	netErr := errors.New("refused")
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond}
	calls := 0
	_, err := cfg.Do(context.Background(), func(_ context.Context) (*http.Response, error) {
		calls++
		return nil, netErr
	})
	if !errors.Is(err, netErr) || calls != 3 {
		t.Fatalf("calls=%d err=%v, want 3 attempts and netErr", calls, err)
	}
}

func TestRetry_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := RetryConfig{MaxRetries: 5, BaseDelay: time.Hour}
	_, err := cfg.Do(ctx, func(_ context.Context) (*http.Response, error) {
		return nil, errors.New("reset")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRetryAfter_Seconds(t *testing.T) {
	var got time.Duration
	cfg := RetryConfig{MaxRetries: 1, BaseDelay: time.Millisecond}
	calls := 0
	start := time.Now()
	resp, err := cfg.Do(context.Background(), func(_ context.Context) (*http.Response, error) {
		calls++
		if calls == 1 {
			h := http.Header{}
			h.Set("Retry-After", "1")
			return &http.Response{StatusCode: 429, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	got = time.Since(start)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	bodyString(t, resp)
	if got < 900*time.Millisecond {
		t.Errorf("Retry-After was not honored, elapsed %s", got)
	}
}

func TestDelayFor_ExponentialWithCap(t *testing.T) {
	rnd := func() float64 { return 0 } // jitter [d/2, d) collapses to d/2
	cfg := RetryConfig{BaseDelay: 100 * time.Millisecond, MaxDelay: 400 * time.Millisecond}
	for n, want := range []time.Duration{
		50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
		200 * time.Millisecond, 200 * time.Millisecond,
	} {
		got := cfg.delayFor(n, nil, rnd)
		if got != want {
			t.Errorf("delayFor(%d) = %s, want %s", n, got, want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty = %s", got)
	}
	if got := parseRetryAfter("-5"); got != 0 {
		t.Errorf("negative = %s", got)
	}
	if got := parseRetryAfter("2"); got != 2*time.Second {
		t.Errorf("seconds = %s", got)
	}
	if got := parseRetryAfter("not a date"); got != 0 {
		t.Errorf("garbage = %s", got)
	}
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 {
		t.Errorf("http-date = %s, want positive", got)
	}
}
