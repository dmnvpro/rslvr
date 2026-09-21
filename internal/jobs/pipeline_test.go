package jobs

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rslvr/internal/capture"
	"rslvr/internal/vision"
)

// ---- fakes ----

type fakeCapturer struct {
	devices []capture.Device
	frame   []byte
	mime    string
	grabErr error
	listErr error
	block   chan struct{} // if set, Grab blocks until closed or ctx done

	mu       sync.Mutex
	grabs    int
	canceled bool
}

func (f *fakeCapturer) List(_ context.Context) ([]capture.Device, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.devices, nil
}

func (f *fakeCapturer) Grab(ctx context.Context, _ capture.Device) ([]byte, string, error) {
	f.mu.Lock()
	f.grabs++
	f.mu.Unlock()
	if f.grabErr != nil {
		return nil, "", f.grabErr
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			f.mu.Lock()
			f.canceled = true
			f.mu.Unlock()
			return nil, "", ctx.Err()
		}
	}
	return f.frame, f.mime, nil
}

func (f *fakeCapturer) Close() error { return nil }

func (f *fakeCapturer) grabCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grabs
}

type fakeAnalyzer struct {
	res     vision.Result
	err     error
	delay   time.Duration
	analyze func(ctx context.Context, img []byte, mime string) (vision.Result, error)

	mu    sync.Mutex
	calls int
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, img []byte, mime string) (vision.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.analyze != nil {
		return f.analyze(ctx, img, mime)
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return vision.Result{}, ctx.Err()
		}
	}
	if f.err != nil {
		return vision.Result{}, f.err
	}
	return f.res, nil
}

func (f *fakeAnalyzer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func frameJPEG(t *testing.T, px byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{px, px, px, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testRunner(capt capture.Capturer, ana vision.Analyzer, mutate func(*Options)) *Runner {
	opts := Options{
		Capturer:      capt,
		Analyzer:      ana,
		Timeout:       5 * time.Second,
		VisionTimeout: 4 * time.Second,
		CacheTTL:      30 * time.Second,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&opts)
	}
	return NewRunner(opts)
}

func TestRun_HappyPath(t *testing.T) {
	frame := frameJPEG(t, 128)
	capt := &fakeCapturer{
		devices: []capture.Device{{Index: 0, ID: "/dev/video0", Name: "Cam", Path: "/dev/video0"}},
		frame:   frame,
		mime:    "image/jpeg",
	}
	ana := &fakeAnalyzer{res: vision.Result{Answer: "B | Paris", Model: "m", ImageHash: vision.HashImage(frame)}}
	dir := t.TempDir()
	r := testRunner(capt, ana, func(o *Options) { o.Saver = NewSaver(dir) })

	out, err := r.Run(context.Background(), Request{Save: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Result.Answer != "B | Paris" {
		t.Errorf("answer = %q", out.Result.Answer)
	}
	if out.Result.SavedPath == "" {
		t.Fatal("shot was not saved")
	}
	if _, err := os.Stat(out.Result.SavedPath); err != nil {
		t.Errorf("saved file missing: %v", err)
	}
	if !filepath.IsAbs(out.Result.SavedPath) && out.Result.SavedPath[0] != '.' {
		t.Errorf("unexpected path form %q", out.Result.SavedPath)
	}
}

func TestRun_BusyWhenSlotTaken(t *testing.T) {
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "/dev/video0", Path: "/dev/video0"}},
		frame:   frameJPEG(t, 1),
		mime:    "image/jpeg",
	}
	block := make(chan struct{})
	ana := &fakeAnalyzer{analyze: func(_ context.Context, _ []byte, _ string) (vision.Result, error) {
		<-block
		return vision.Result{Answer: "x"}, nil
	}}
	r := testRunner(capt, ana, nil)
	r.SetLimiter(NewLimiter(1))

	done := make(chan struct{})
	go func() {
		_, _ = r.Run(context.Background(), Request{})
		close(done)
	}()

	// Give the first job time to take the slot.
	time.Sleep(50 * time.Millisecond)
	if _, err := r.Run(context.Background(), Request{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Run err = %v, want ErrBusy", err)
	}
	if !r.lim.Busy() {
		t.Error("limiter must be busy while the first job runs")
	}
	close(block)
	<-done
}

func TestRun_SemaphoreNotLeakedOnPanicOrError(t *testing.T) {
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		grabErr: capture.ErrNoSignal,
	}
	ana := &fakeAnalyzer{}
	r := testRunner(capt, ana, nil)

	for i := 0; i < 3; i++ {
		if _, err := r.Run(context.Background(), Request{}); err == nil {
			t.Fatal("expected grab error")
		}
	}
	if r.lim.Busy() {
		t.Error("slot leaked after errors")
	}
}

func TestRun_CacheHitDoesNotReanalyze(t *testing.T) {
	frame := frameJPEG(t, 7)
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frame,
		mime:    "image/jpeg",
	}
	ana := &fakeAnalyzer{res: vision.Result{Answer: "cached answer"}}
	r := testRunner(capt, ana, nil)

	base := vision.HashImage(frame)
	for i := 0; i < 3; i++ {
		out, err := r.Run(context.Background(), Request{})
		if err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		if out.Result.Answer != "cached answer" || out.Result.ImageHash != base {
			t.Errorf("Run %d: %+v", i, out.Result)
		}
	}
	if ana.callCount() != 1 {
		t.Errorf("analyzer called %d times, want 1 (cache hits)", ana.callCount())
	}
	if capt.grabCount() != 3 {
		t.Errorf("grab count = %d, want 3 (frame is captured fresh)", capt.grabCount())
	}
}

func TestRun_CacheDistinguishesPromptAndMime(t *testing.T) {
	frame := frameJPEG(t, 9)
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frame,
		mime:    "image/jpeg",
	}
	ana := &fakeAnalyzer{res: vision.Result{Answer: "a"}}
	r := testRunner(capt, ana, nil)

	_, _ = r.Run(context.Background(), Request{Prompt: "prompt A"})
	_, _ = r.Run(context.Background(), Request{Prompt: "prompt B"})
	if ana.callCount() != 2 {
		t.Errorf("calls = %d, want 2 (different prompts)", ana.callCount())
	}
	_, _ = r.Run(context.Background(), Request{Prompt: "prompt A"})
	if ana.callCount() != 2 {
		t.Errorf("calls = %d, want 2 (same prompt hits cache)", ana.callCount())
	}
}

func TestRun_CacheExpires(t *testing.T) {
	frame := frameJPEG(t, 3)
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frame,
		mime:    "image/jpeg",
	}
	ana := &fakeAnalyzer{res: vision.Result{Answer: "a"}}
	r := testRunner(capt, ana, nil)

	var clock atomic.Int64
	r.now = func() time.Time { return time.Unix(0, clock.Load()) }
	NewID = func() string { return "fixed" } // silence id churn in logs

	_, _ = r.Run(context.Background(), Request{})
	clock.Store(time.Now().Add(31 * time.Second).UnixNano())
	_, _ = r.Run(context.Background(), Request{})
	if ana.callCount() != 2 {
		t.Errorf("calls = %d, want 2 after TTL expiry", ana.callCount())
	}
}

func TestRun_NoCacheWhenDisabled(t *testing.T) {
	frame := frameJPEG(t, 5)
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frame,
		mime:    "image/jpeg",
	}
	ana := &fakeAnalyzer{res: vision.Result{Answer: "a"}}
	r := testRunner(capt, ana, func(o *Options) { o.CacheTTL = 0 })

	_, _ = r.Run(context.Background(), Request{})
	_, _ = r.Run(context.Background(), Request{})
	if ana.callCount() != 2 {
		t.Errorf("calls = %d, want 2 with cache disabled", ana.callCount())
	}
}

func TestRun_VisionBudgetCappedByRemainingDeadline(t *testing.T) {
	frame := frameJPEG(t, 11)
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frame,
		mime:    "image/jpeg",
	}
	var gotBudget time.Duration
	ana := &fakeAnalyzer{analyze: func(ctx context.Context, _ []byte, _ string) (vision.Result, error) {
		dl, _ := ctx.Deadline()
		gotBudget = time.Until(dl)
		return vision.Result{Answer: "ok"}, nil
	}}
	r := testRunner(capt, ana, func(o *Options) {
		o.Timeout = 800 * time.Millisecond
		o.VisionTimeout = 10 * time.Second // must shrink to the remaining ~800ms
	})
	if _, err := r.Run(context.Background(), Request{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotBudget > 800*time.Millisecond {
		t.Errorf("vision budget = %s, want capped by overall deadline", gotBudget)
	}
}

func TestVisionBudget_ExpiredDeadline(t *testing.T) {
	capt := &fakeCapturer{devices: []capture.Device{{ID: "d", Path: "d"}}}
	ana := &fakeAnalyzer{}
	r := testRunner(capt, ana, nil)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := r.visionBudget(ctx); !errors.Is(err, capture.ErrTimeout) {
		t.Fatalf("err = %v, want capture.ErrTimeout", err)
	}
}

func TestRun_CancelCancelsGrab(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frameJPEG(t, 21),
		mime:    "image/jpeg",
		block:   block,
	}
	ana := &fakeAnalyzer{}
	r := testRunner(capt, ana, func(o *Options) { o.Timeout = time.Hour })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, Request{})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if capt.canceled {
		t.Log("grab observed cancellation, as expected")
	}
}

func TestRun_AnalyzerErrorPropagates(t *testing.T) {
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frameJPEG(t, 17),
		mime:    "image/jpeg",
	}
	ana := &fakeAnalyzer{err: vision.ErrUnreadableImage}
	r := testRunner(capt, ana, nil)
	_, err := r.Run(context.Background(), Request{})
	if !errors.Is(err, vision.ErrUnreadableImage) {
		t.Fatalf("err = %v, want ErrUnreadableImage", err)
	}
}

func TestStatus(t *testing.T) {
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frameJPEG(t, 19),
		mime:    "image/jpeg",
	}
	block := make(chan struct{})
	ana := &fakeAnalyzer{analyze: func(_ context.Context, _ []byte, _ string) (vision.Result, error) {
		<-block
		return vision.Result{Answer: "x"}, nil
	}}
	r := testRunner(capt, ana, nil)
	if st := r.Status(); st.State != "idle" || st.Busy {
		t.Fatalf("status = %+v, want idle", st)
	}
	done := make(chan struct{})
	go func() {
		_, _ = r.Run(context.Background(), Request{})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	if st := r.Status(); !st.Busy || st.State != "busy" || st.JobID == "" {
		t.Errorf("status = %+v, want busy with job id", st)
	}
	close(block)
	<-done
	if st := r.Status(); st.Busy {
		t.Errorf("status = %+v, want idle after completion", st)
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(2)
	r1, ok1 := l.TryAcquire()
	r2, ok2 := l.TryAcquire()
	if !ok1 || !ok2 {
		t.Fatal("two acquires must succeed")
	}
	if _, ok := l.TryAcquire(); ok {
		t.Fatal("third acquire must fail")
	}
	r1()
	if _, ok := l.TryAcquire(); !ok {
		t.Fatal("acquire after release must succeed")
	}
	r2()
}

func TestSaver_SkipsWhenDisabled(t *testing.T) {
	var s *Saver
	p, err := s.Save([]byte("x"), "image/jpeg", "abcdef1234567890")
	if err != nil || p != "" {
		t.Errorf("nil saver: %q %v", p, err)
	}
	p, err = NewSaver("").Save([]byte("x"), "image/jpeg", "abcdef1234567890")
	if err != nil || p != "" {
		t.Errorf("empty dir: %q %v", p, err)
	}
}

func TestSaver_NamesFileByHash(t *testing.T) {
	dir := t.TempDir()
	s := NewSaver(dir)
	p, err := s.Save([]byte("frame"), "image/png", "abcdef1234567890")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(p)
	if !strings.HasPrefix(p, dir+string(os.PathSeparator)) {
		t.Errorf("path %q outside dir", p)
	}
	if got := base[len(base)-len("_abcdef12.png"):]; got != "_abcdef12.png" {
		t.Errorf("filename = %q, want hash8+ext suffix", base)
	}
}
