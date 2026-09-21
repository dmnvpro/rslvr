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
	analyze func(ctx context.Context, frames []vision.Frame) (vision.Result, error)

	mu    sync.Mutex
	calls int
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, frames []vision.Frame) (vision.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.analyze != nil {
		return f.analyze(ctx, frames)
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

func frameSet(t *testing.T, pxs ...byte) []vision.Frame {
	t.Helper()
	frames := make([]vision.Frame, 0, len(pxs))
	for _, px := range pxs {
		frames = append(frames, vision.Frame{Data: frameJPEG(t, px), MIME: "image/jpeg"})
	}
	return frames
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

// ---- Grab ----

func TestGrab_HappyPathSavesShot(t *testing.T) {
	frame := frameJPEG(t, 128)
	capt := &fakeCapturer{
		devices: []capture.Device{{Index: 0, ID: "/dev/video0", Name: "Cam", Path: "/dev/video0"}},
		frame:   frame,
		mime:    "image/jpeg",
	}
	dir := t.TempDir()
	r := testRunner(capt, &fakeAnalyzer{}, func(o *Options) { o.Saver = NewSaver(dir) })

	g, err := r.Grab(context.Background(), "", true)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if !bytes.Equal(g.Frame.Data, frame) || g.Frame.MIME != "image/jpeg" {
		t.Errorf("grabbed frame mismatch: %d bytes, mime %q", len(g.Frame.Data), g.Frame.MIME)
	}
	if g.SHA256 != vision.HashImage(frame) {
		t.Errorf("sha256 = %q, want frame hash", g.SHA256)
	}
	if g.SavedPath == "" {
		t.Fatal("shot was not saved")
	}
	if _, err := os.Stat(g.SavedPath); err != nil {
		t.Errorf("saved file missing: %v", err)
	}
}

func TestGrab_NoSaveWhenDisabled(t *testing.T) {
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frameJPEG(t, 1),
		mime:    "image/jpeg",
	}
	r := testRunner(capt, &fakeAnalyzer{}, nil)

	g, err := r.Grab(context.Background(), "", false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if g.SavedPath != "" {
		t.Errorf("saved_path = %q, want empty", g.SavedPath)
	}
}

func TestGrab_SemaphoreNotLeakedOnError(t *testing.T) {
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		grabErr: capture.ErrNoSignal,
	}
	r := testRunner(capt, &fakeAnalyzer{}, nil)

	for i := 0; i < 3; i++ {
		if _, err := r.Grab(context.Background(), "", false); err == nil {
			t.Fatal("expected grab error")
		}
	}
	if r.lim.Busy() {
		t.Error("slot leaked after errors")
	}
}

func TestGrab_CancelCancelsGrab(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	capt := &fakeCapturer{
		devices: []capture.Device{{ID: "d", Path: "d"}},
		frame:   frameJPEG(t, 21),
		mime:    "image/jpeg",
		block:   block,
	}
	r := testRunner(capt, &fakeAnalyzer{}, func(o *Options) { o.Timeout = time.Hour })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.Grab(ctx, "", false)
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
		t.Fatal("Grab did not return after cancel")
	}
}

// ---- Resolve ----

func TestResolve_HappyPath(t *testing.T) {
	frames := frameSet(t, 128)
	ana := &fakeAnalyzer{res: vision.Result{Answer: "B | Paris", Model: "m"}}
	r := testRunner(&fakeCapturer{}, ana, nil)

	res, err := r.Resolve(context.Background(), frames, "p")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Answer != "B | Paris" {
		t.Errorf("answer = %q", res.Answer)
	}
	if res.ImageHash != vision.HashFrames(frames) {
		t.Errorf("image_sha256 = %q, want frame-set hash", res.ImageHash)
	}
	if ana.callCount() != 1 {
		t.Errorf("analyzer called %d times", ana.callCount())
	}
}

func TestResolve_BusyWhenSlotTaken(t *testing.T) {
	block := make(chan struct{})
	ana := &fakeAnalyzer{analyze: func(_ context.Context, _ []vision.Frame) (vision.Result, error) {
		<-block
		return vision.Result{Answer: "x"}, nil
	}}
	r := testRunner(&fakeCapturer{}, ana, nil)
	r.SetLimiter(NewLimiter(1))

	done := make(chan struct{})
	go func() {
		_, _ = r.Resolve(context.Background(), frameSet(t, 1), "p")
		close(done)
	}()

	// Give the first job time to take the slot.
	time.Sleep(50 * time.Millisecond)
	if _, err := r.Resolve(context.Background(), frameSet(t, 2), "p"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Resolve err = %v, want ErrBusy", err)
	}
	// Grabs share the same pool: the device cannot serve two masters.
	if _, err := r.Grab(context.Background(), "", false); !errors.Is(err, ErrBusy) {
		t.Fatalf("Grab while resolving err = %v, want ErrBusy", err)
	}
	if !r.lim.Busy() {
		t.Error("limiter must be busy while the first job runs")
	}
	close(block)
	<-done
}

func TestResolve_CacheHitDoesNotReanalyze(t *testing.T) {
	frames := frameSet(t, 7)
	ana := &fakeAnalyzer{res: vision.Result{Answer: "cached answer"}}
	r := testRunner(&fakeCapturer{}, ana, nil)

	for i := 0; i < 3; i++ {
		res, err := r.Resolve(context.Background(), frames, "p")
		if err != nil {
			t.Fatalf("Resolve %d: %v", i, err)
		}
		if res.Answer != "cached answer" {
			t.Errorf("Resolve %d: %+v", i, res)
		}
	}
	if ana.callCount() != 1 {
		t.Errorf("analyzer called %d times, want 1 (cache hits)", ana.callCount())
	}
}

func TestResolve_CacheDistinguishesPromptAndSet(t *testing.T) {
	ana := &fakeAnalyzer{res: vision.Result{Answer: "a"}}
	r := testRunner(&fakeCapturer{}, ana, nil)

	_, _ = r.Resolve(context.Background(), frameSet(t, 9), "prompt A")
	_, _ = r.Resolve(context.Background(), frameSet(t, 9), "prompt B")
	if ana.callCount() != 2 {
		t.Errorf("calls = %d, want 2 (different prompts)", ana.callCount())
	}
	_, _ = r.Resolve(context.Background(), frameSet(t, 9), "prompt A")
	if ana.callCount() != 2 {
		t.Errorf("calls = %d, want 2 (same prompt hits cache)", ana.callCount())
	}
	// A different frame set is a different question even under one prompt.
	_, _ = r.Resolve(context.Background(), frameSet(t, 9, 10), "prompt A")
	if ana.callCount() != 3 {
		t.Errorf("calls = %d, want 3 (different frame set is a different question)", ana.callCount())
	}
	// Order matters: the model reads frames in order, so [a,b] != [b,a].
	a := frameJPEG(t, 11)
	b := frameJPEG(t, 12)
	forward := []vision.Frame{{Data: a, MIME: "image/jpeg"}, {Data: b, MIME: "image/jpeg"}}
	reverse := []vision.Frame{{Data: b, MIME: "image/jpeg"}, {Data: a, MIME: "image/jpeg"}}
	if vision.HashFrames(forward) == vision.HashFrames(reverse) {
		t.Error("frame order must change the frame-set hash")
	}
}

func TestResolve_CacheExpires(t *testing.T) {
	ana := &fakeAnalyzer{res: vision.Result{Answer: "a"}}
	r := testRunner(&fakeCapturer{}, ana, nil)

	var clock atomic.Int64
	r.now = func() time.Time { return time.Unix(0, clock.Load()) }
	NewID = func() string { return "fixed" } // silence id churn in logs

	frames := frameSet(t, 3)
	_, _ = r.Resolve(context.Background(), frames, "p")
	clock.Store(time.Now().Add(31 * time.Second).UnixNano())
	_, _ = r.Resolve(context.Background(), frames, "p")
	if ana.callCount() != 2 {
		t.Errorf("calls = %d, want 2 after TTL expiry", ana.callCount())
	}
}

func TestResolve_NoCacheWhenDisabled(t *testing.T) {
	ana := &fakeAnalyzer{res: vision.Result{Answer: "a"}}
	r := testRunner(&fakeCapturer{}, ana, func(o *Options) { o.CacheTTL = 0 })

	frames := frameSet(t, 5)
	_, _ = r.Resolve(context.Background(), frames, "p")
	_, _ = r.Resolve(context.Background(), frames, "p")
	if ana.callCount() != 2 {
		t.Errorf("calls = %d, want 2 with cache disabled", ana.callCount())
	}
}

func TestResolve_VisionBudgetCappedByRemainingDeadline(t *testing.T) {
	var gotBudget time.Duration
	ana := &fakeAnalyzer{analyze: func(ctx context.Context, _ []vision.Frame) (vision.Result, error) {
		dl, _ := ctx.Deadline()
		gotBudget = time.Until(dl)
		return vision.Result{Answer: "ok"}, nil
	}}
	r := testRunner(&fakeCapturer{}, ana, func(o *Options) {
		o.Timeout = 800 * time.Millisecond
		o.VisionTimeout = 10 * time.Second // must shrink to the remaining ~800ms
	})
	if _, err := r.Resolve(context.Background(), frameSet(t, 11), "p"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotBudget > 800*time.Millisecond {
		t.Errorf("vision budget = %s, want capped by overall deadline", gotBudget)
	}
}

func TestVisionBudget_ExpiredDeadline(t *testing.T) {
	r := testRunner(&fakeCapturer{}, &fakeAnalyzer{}, nil)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := r.visionBudget(ctx); !errors.Is(err, capture.ErrTimeout) {
		t.Fatalf("err = %v, want capture.ErrTimeout", err)
	}
}

func TestResolve_AnalyzerErrorPropagates(t *testing.T) {
	ana := &fakeAnalyzer{err: vision.ErrUnreadableImage}
	r := testRunner(&fakeCapturer{}, ana, nil)
	_, err := r.Resolve(context.Background(), frameSet(t, 17), "p")
	if !errors.Is(err, vision.ErrUnreadableImage) {
		t.Fatalf("err = %v, want ErrUnreadableImage", err)
	}
}

func TestStatus(t *testing.T) {
	block := make(chan struct{})
	ana := &fakeAnalyzer{analyze: func(_ context.Context, _ []vision.Frame) (vision.Result, error) {
		<-block
		return vision.Result{Answer: "x"}, nil
	}}
	r := testRunner(&fakeCapturer{}, ana, nil)
	if st := r.Status(); st.State != "idle" || st.Busy {
		t.Fatalf("status = %+v, want idle", st)
	}
	done := make(chan struct{})
	go func() {
		_, _ = r.Resolve(context.Background(), frameSet(t, 19), "p")
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
