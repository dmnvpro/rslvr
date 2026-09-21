package capture

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"os/exec"
	"testing"
	"time"
)

// TestFFmpegRealGrab_Lavfi exercises the real exec machinery (process group,
// output limits, frame validation) against a synthetic ffmpeg source — no
// camera or v4l2loopback required. Skipped when ffmpeg is not installed.
func TestFFmpegRealGrab_Lavfi(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}

	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-frames:v", "1", "-c:v", "mjpeg", "-q:v", "7",
		"-pix_fmt", "yuvj420p", "-f", "image2pipe", "pipe:1",
	}
	stdout, stderr, err := defaultExecer{}.Run(context.Background(), "ffmpeg", args)
	if err != nil {
		t.Fatalf("ffmpeg run: %v; stderr: %s", err, stderr)
	}
	mime, verr := ValidateFrame(stdout)
	if verr != nil {
		t.Fatalf("validate: %v (%d bytes)", verr, len(stdout))
	}
	if mime != "image/jpeg" {
		t.Errorf("mime = %q", mime)
	}

	img, _, err := image.Decode(bytes.NewReader(stdout))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 160 || b.Dy() != 120 {
		t.Errorf("dimensions = %dx%d, want 160x120", b.Dx(), b.Dy())
	}

	// The testsrc pattern is bright; a black frame means a broken pipeline.
	var sum, n int64
	for y := b.Min.Y; y < b.Max.Y; y += 8 {
		for x := b.Min.X; x < b.Max.X; x += 8 {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += int64(r/257) + int64(g/257) + int64(bl/257)
			n += 3
		}
	}
	if avg := float64(sum) / float64(n); avg < 20 {
		t.Errorf("frame looks black (avg %.1f/255)", avg)
	}
}

// TestFFmpegRealGrab_ContextKill makes sure a canceled context actually stops
// the external process (SIGKILL of the process group, no leak).
func TestFFmpegRealGrab_ContextKill(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := defaultExecer{}.Run(ctx, "ffmpeg", []string{
			"-hide_banner", "-nostdin", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc=size=64x64:rate=10",
			"-t", "60", "-f", "null", "-",
		})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// Run returned after the kill — success regardless of the error kind.
	case <-time.After(5 * time.Second):
		t.Fatal("execer did not return after context cancel")
	}
}
