package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rslvr/internal/capture"
	"rslvr/internal/config"
	"rslvr/internal/jobs"
	"rslvr/internal/vision"
)

// ---- fakes (duplicated from jobs tests on purpose: no shared test pkg) ----

type fakeCap struct {
	devices []capture.Device
	frame   []byte
	mime    string
	grabErr error

	mu    sync.Mutex
	grabs int
}

func (f *fakeCap) List(context.Context) ([]capture.Device, error) { return f.devices, nil }
func (f *fakeCap) Close() error                                   { return nil }
func (f *fakeCap) Grab(_ context.Context, _ capture.Device) ([]byte, string, error) {
	f.mu.Lock()
	f.grabs++
	f.mu.Unlock()
	if f.grabErr != nil {
		return nil, "", f.grabErr
	}
	return f.frame, f.mime, nil
}

type fakeAna struct {
	res   vision.Result
	err   error
	block chan struct{}

	mu    sync.Mutex
	calls int
}

func (f *fakeAna) Analyze(_ context.Context, _ []byte, _ string) (vision.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.block != nil {
		<-f.block
	}
	return f.res, f.err
}

func (f *fakeAna) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ---- harness ----

func frameJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.White)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fixture struct {
	srv  *httptest.Server
	cap  *fakeCap
	ana  *fakeAna
	pipe *jobs.Runner
}

func newFixture(t *testing.T, mutate func(*config.Config)) *fixture {
	t.Helper()
	frame := frameJPEG(t)
	f := &fixture{
		cap: &fakeCap{
			devices: []capture.Device{{Index: 0, ID: "/dev/video0", Name: "USB Cam", Path: "/dev/video0"}},
			frame:   frame,
			mime:    "image/jpeg",
		},
		ana: &fakeAna{res: vision.Result{Answer: "B | Paris", Model: "test/model", LatencyMS: 42}},
	}
	cfg := config.Defaults()
	cfg.Capture.SaveDir = "" // do not write files in tests
	if mutate != nil {
		mutate(&cfg)
	}
	runner := jobs.NewRunner(jobs.Options{
		Capturer:      f.cap,
		Analyzer:      f.ana,
		Timeout:       cfg.Timeout,
		VisionTimeout: cfg.Vision.Timeout,
		CacheTTL:      30 * time.Second,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	f.pipe = runner
	h := New(Deps{Cfg: cfg, Runner: runner, Capturer: f.cap,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	f.srv = httptest.NewServer(h)
	t.Cleanup(f.srv.Close)
	return f
}

func post(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v (status %s)", err, resp.Status)
	}
	return m
}

// ---- tests ----

func TestScreenshot_200Shape(t *testing.T) {
	f := newFixture(t, nil)
	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	m := decode(t, resp)
	if m["answer"] != "B | Paris" {
		t.Errorf("answer = %v", m["answer"])
	}
	if m["image_sha256"] == "" {
		t.Error("image_sha256 missing")
	}
	preview, _ := m["preview"].(string)
	if !strings.HasPrefix(preview, "data:image/jpeg;base64,") {
		t.Errorf("preview = %q", preview)
	}
}

func TestScreenshot_400BadJSON(t *testing.T) {
	f := newFixture(t, nil)
	resp, err := http.Post(f.srv.URL+"/api/screenshot", "application/json", strings.NewReader("{nope"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	m := decode(t, resp)
	if m["error"].(map[string]any)["code"] != "bad_request" {
		t.Errorf("error code = %v", m["error"])
	}
}

func TestScreenshot_409Busy(t *testing.T) {
	f := newFixture(t, nil)
	f.ana.block = make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = post(t, f.srv.URL+"/api/screenshot", map[string]any{})
		close(done)
	}()
	// Wait until the runner actually holds the slot (deterministic), then the
	// next request must get 409 immediately.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.pipe.Status().Busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !f.pipe.Status().Busy {
		t.Fatal("first job never started")
	}
	second := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", second.StatusCode)
	}
	second.Body.Close()

	close(f.ana.block)
	<-done
}

func TestScreenshot_422Unreadable(t *testing.T) {
	f := newFixture(t, nil)
	f.ana.err = vision.ErrUnreadableImage
	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if code := decode(t, resp)["error"].(map[string]any)["code"]; code != "unreadable" {
		t.Errorf("code = %v", code)
	}
}

func TestScreenshot_422NoSignal(t *testing.T) {
	f := newFixture(t, nil)
	f.cap.grabErr = capture.ErrNoSignal
	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestScreenshot_409DeviceBusy(t *testing.T) {
	f := newFixture(t, nil)
	f.cap.grabErr = capture.ErrDeviceBusy
	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestScreenshot_502Upstream(t *testing.T) {
	f := newFixture(t, nil)
	f.ana.err = fmt.Errorf("dial refused: %w", vision.ErrUpstream)
	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestScreenshot_502Auth(t *testing.T) {
	f := newFixture(t, nil)
	f.ana.err = vision.ErrAuth
	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestScreenshot_504Timeout(t *testing.T) {
	f := newFixture(t, nil)
	f.cap.grabErr = capture.ErrTimeout
	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", resp.StatusCode)
	}
}

func TestDevices(t *testing.T) {
	f := newFixture(t, nil)
	resp, err := http.Get(f.srv.URL + "/api/devices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	m := decode(t, resp)
	devs := m["devices"].([]any)
	if len(devs) != 1 {
		t.Fatalf("devices = %v", devs)
	}
	if m["selected"] != "/dev/video0" {
		t.Errorf("selected = %v", m["selected"])
	}
	if devs[0].(map[string]any)["name"] != "USB Cam" {
		t.Errorf("devices[0] = %v", devs[0])
	}
}

func TestStatus_Health_Index(t *testing.T) {
	f := newFixture(t, nil)

	resp, _ := http.Get(f.srv.URL + "/api/status")
	m := decode(t, resp)
	resp.Body.Close()
	if m["state"] != "idle" || m["busy"] != false {
		t.Errorf("status = %v", m)
	}

	resp, _ = http.Get(f.srv.URL + "/api/health")
	m = decode(t, resp)
	resp.Body.Close()
	if m["ok"] != true {
		t.Errorf("health = %v", m)
	}

	resp, _ = http.Get(f.srv.URL + "/")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	for _, want := range []string{`id="btn-shot"`, `id="dev"`, `Take screenshot`} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

func TestCIDR_Whitelist(t *testing.T) {
	f := newFixture(t, func(c *config.Config) {
		c.Server.AllowedIPs = []string{"127.0.0.0/8"} // httptest client is 127.0.0.1
	})
	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed IP rejected: %d", resp.StatusCode)
	}
}

func TestCIDR_Denied(t *testing.T) {
	// Remote address spoofing check happens at the middleware level: run the
	// handler stack against a recorder with a foreign RemoteAddr.
	cfg := config.Defaults()
	cfg.Server.AllowedIPs = []string{"10.9.9.0/24"}
	runner := jobs.NewRunner(jobs.Options{Capturer: &fakeCap{}, Analyzer: &fakeAna{}})
	h := New(Deps{Cfg: cfg, Runner: runner, Capturer: &fakeCap{}}).Handler()

	req := httptest.NewRequest("POST", "/api/screenshot", strings.NewReader("{}"))
	req.RemoteAddr = "203.0.113.7:12345"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestToken_Required(t *testing.T) {
	f := newFixture(t, func(c *config.Config) { c.Server.AuthToken = "s3cret" })

	// No token → 401.
	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Health stays open for liveness probes.
	hresp, _ := http.Get(f.srv.URL + "/api/health")
	m := decode(t, hresp)
	hresp.Body.Close()
	if m["ok"] != true {
		t.Errorf("health must not require the token")
	}

	// Bearer token works.
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/screenshot", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer s3cret")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("bearer: %d, want 200", resp2.StatusCode)
	}

	// X-Auth-Token works.
	req, _ = http.NewRequest("POST", f.srv.URL+"/api/screenshot", strings.NewReader("{}"))
	req.Header.Set("X-Auth-Token", "s3cret")
	resp3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("x-token: %d, want 200", resp3.StatusCode)
	}
}

func TestScreenshot_PromptOverridePlumbsThrough(t *testing.T) {
	// The override changes the job prompt, which is part of the cache key:
	// two identical frames with different overrides must analyze twice, and
	// the same override twice must hit the cache once.
	f := newFixture(t, nil)

	for _, override := range []string{"custom prompt", "custom prompt"} {
		resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{"prompt_override": override})
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}
	if got := f.ana.callCount(); got != 1 {
		t.Fatalf("calls = %d, want 1 (same override must hit cache)", got)
	}

	resp := post(t, f.srv.URL+"/api/screenshot", map[string]any{"prompt_override": "other prompt"})
	resp.Body.Close()
	if got := f.ana.callCount(); got != 2 {
		t.Fatalf("calls = %d, want 2 (different override is a different question)", got)
	}
}

func TestParseCIDRs(t *testing.T) {
	nets := parseCIDRs([]string{"127.0.0.1", "10.0.0.0/8", "garbage"})
	if len(nets) != 2 {
		t.Fatalf("nets = %d, want 2 (garbage dropped)", len(nets))
	}
	if !nets[0].Contains(net.ParseIP("127.0.0.1")) {
		t.Error("single IP must map to /32")
	}
	if !nets[1].Contains(net.ParseIP("10.1.2.3")) {
		t.Error("CIDR must contain its range")
	}
	if got := parseCIDRs(nil); got != nil {
		t.Errorf("nil input = %v", got)
	}
}
