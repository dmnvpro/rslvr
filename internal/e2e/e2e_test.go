//go:build e2e

// Package e2e runs the real binary against a real (v4l2loopback) camera.
// Prepare the camera first: `make e2e-prepare` (needs sudo). Every test
// skips cleanly when /dev/video0 is absent.
package e2e

import (
	"image"
	_ "image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const device = "/dev/video0"

func requireCamera(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(device); err != nil {
		t.Skipf("no %s; run `make e2e-prepare` (v4l2loopback + testsrc) first", device)
	}
}

func binary(t *testing.T) string {
	t.Helper()
	const p = "../../bin/rslvr"
	if _, err := os.Stat(p); err != nil {
		t.Skip("bin/rslvr not built; run `make build` first")
	}
	return p
}

func TestRealListDevices(t *testing.T) {
	requireCamera(t)
	bin := binary(t)

	cmd := exec.Command(bin, "--list-devices")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--list-devices failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), device) {
		t.Errorf("output missing %s: %s", device, out)
	}
}

// TestRealOnceWithMockAPI runs the full Grab→Analyze pipeline: the frame is
// captured by the real ffmpeg from the loopback camera, then "analyzed" by a
// local fake OpenAI endpoint — no key or network needed.
func TestRealOnceWithMockAPI(t *testing.T) {
	requireCamera(t)
	bin := binary(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"e2e-1","model":"mock","choices":[{"message":{"content":"A | test answer"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := "listen: 127.0.0.1:0\ncapture:\n  device: \"" + device + "\"\n  save_dir: \"" + dir + "\"\n  warmup_ms: 500ms\nvision:\n  base_url: \"" + srv.URL + "\"\n  api_key: \"e2e-key\"\n  model: mock/model\n"
	cfgPath := dir + "/config.yaml"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "--once", "--config", cfgPath, "--timeout", "30s")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--once failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "A | test answer") {
		t.Errorf("stdout missing answer: %s", out)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jpg") {
			saved++
		}
	}
	if saved == 0 {
		t.Error("no frame copy saved into save_dir")
	}
}

// TestRealGrabFrameIsNotBlack proves warmup keeps the captured frame usable:
// the testsrc pattern is bright, so a near-black frame means the grab fired
// before the device produced real output.
func TestRealGrabFrameIsNotBlack(t *testing.T) {
	requireCamera(t)
	bin := binary(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"A | x"}}]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := "capture:\n  device: \"" + device + "\"\n  save_dir: \"" + dir + "\"\n  warmup_ms: 700ms\nvision:\n  base_url: \"" + srv.URL + "\"\n  api_key: \"k\"\n"
	cfgPath := dir + "/config.yaml"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "--once", "--config", cfgPath, "--timeout", "30s")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--once failed: %v\n%s", err, out)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jpg") {
			continue
		}
		f, err := os.Open(dir + "/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", e.Name(), err)
		}
		b := img.Bounds()
		var sum int64
		var n int64
		for y := b.Min.Y; y < b.Max.Y; y += 4 {
			for x := b.Min.X; x < b.Max.X; x += 4 {
				r, g, bl, _ := img.At(x, y).RGBA()
				sum += int64(r/257) + int64(g/257) + int64(bl/257)
				n += 3
			}
		}
		avg := float64(sum) / float64(n)
		t.Logf("%s: average brightness %.1f/255", e.Name(), avg)
		if avg < 20 {
			t.Errorf("frame looks black (avg %.1f); increase capture.warmup_ms", avg)
		}
		return
	}
	t.Fatal("no saved frame found")
}
