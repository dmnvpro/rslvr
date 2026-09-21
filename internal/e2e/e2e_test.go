//go:build e2e

// Package e2e runs the real binary against a real (v4l2loopback) camera.
// Prepare the camera first: `make e2e-prepare` (needs sudo). Every test
// skips cleanly when /dev/video0 is absent.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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

// startServer launches the binary in serve mode and returns the base URL once
// the "serving" log line reports the real (possibly :0-assigned) address.
func startServer(t *testing.T, bin, cfgPath string) string {
	t.Helper()
	cmd := exec.Command(bin, "--config", cfgPath, "--timeout", "30s")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		sc := bufio.NewScanner(strings.NewReader(out.String()))
		for sc.Scan() {
			if _, rest, ok := strings.Cut(sc.Text(), "msg=serving"); ok {
				if _, addr, ok := strings.Cut(rest, " addr="); ok {
					if f := strings.Fields(addr); len(f) > 0 {
						return strings.TrimPrefix(f[0], "http://")
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server never reported its address; output:\n%s", out.String())
	return ""
}

func postJSON(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return resp.StatusCode, m
}

func writeConfig(t *testing.T, mockURL string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := "listen: 127.0.0.1:0\n" +
		"capture:\n  device: \"" + device + "\"\n  save_dir: \"" + dir + "\"\n  warmup_ms: 500ms\n" +
		"vision:\n  base_url: \"" + mockURL + "\"\n  api_key: \"e2e-key\"\n  model: mock/model\n"
	cfgPath := dir + "/config.yaml"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// TestRealResolveWithMockAPI drives the full UI flow over the real binary:
// /api/frame captures a frame with the real ffmpeg from the loopback camera,
// /api/screenshot "analyzes" it via a local fake OpenAI endpoint — no key or
// network needed.
func TestRealResolveWithMockAPI(t *testing.T) {
	requireCamera(t)
	bin := binary(t)

	srv := mockAPI(t, "A | test answer")
	base := startServer(t, bin, writeConfig(t, srv.URL))

	code, frame := postJSON(t, "http://"+base+"/api/frame", `{"device":"`+device+`","save":true}`)
	if code != http.StatusOK {
		t.Fatalf("/api/frame status = %d: %v", code, frame)
	}
	img, _ := frame["image"].(string)
	if img == "" {
		t.Fatal("/api/frame returned no image")
	}
	savedPath, _ := frame["saved_path"].(string)

	code, res := postJSON(t, "http://"+base+"/api/screenshot",
		`{"frames":[{"data":"`+img+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("/api/screenshot status = %d: %v", code, res)
	}
	if res["answer"] != "A | test answer" {
		t.Errorf("answer = %v", res["answer"])
	}
	if savedPath == "" {
		t.Fatal("frame was not saved into save_dir")
	}
	if _, err := os.Stat(savedPath); err != nil {
		t.Errorf("saved frame missing: %v", err)
	}
}

// TestRealGrabFrameIsNotBlack proves warmup keeps the captured frame usable:
// the testsrc pattern is bright, so a near-black frame means the grab fired
// before the device produced real output.
func TestRealGrabFrameIsNotBlack(t *testing.T) {
	requireCamera(t)
	bin := binary(t)

	base := startServer(t, bin, writeConfig(t, mockAPI(t, "A | x").URL))

	code, frame := postJSON(t, "http://"+base+"/api/frame", `{"device":"`+device+`","save":true}`)
	if code != http.StatusOK {
		t.Fatalf("/api/frame status = %d: %v", code, frame)
	}
	path, _ := frame["saved_path"].(string)
	if path == "" {
		t.Fatal("no saved frame path")
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
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
	t.Logf("%s: average brightness %.1f/255", path, avg)
	if avg < 20 {
		t.Errorf("frame looks black (avg %.1f); increase capture.warmup_ms", avg)
	}
}

// mockAPI fakes the OpenAI-compatible /chat/completions endpoint.
func mockAPI(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"id":"e2e-1","model":"mock","choices":[{"message":{"content":%q}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			answer)
	}))
	t.Cleanup(srv.Close)
	return srv
}
