package capture

import (
	"fmt"
	"strings"
	"time"
)

// BuildGrabArgs returns the ffmpeg argument list that produces exactly one
// frame on stdout as image2pipe (no program path — the caller supplies it).
//
// Warmup is the input-side "-ss" placed before -i: on a live, non-seekable
// input ffmpeg drops frames until the target timestamp, which is exactly the
// "let auto-exposure settle" pause. Output-side -ss would silently degenerate
// into "give me frame 1" with v4l2's monotonic timestamps.
//
// goos is injectable for tests ("linux", "windows", ...).
func BuildGrabArgs(goos string, dev Device, o Options) []string {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats",
	}
	if o.Warmup > 0 {
		args = append(args, "-ss", seconds(o.Warmup))
	}
	inputFormat, inputPath := deviceInput(goos, dev)
	args = append(args, "-f", inputFormat, "-i", inputPath)
	args = append(args, "-frames:v", "1")
	switch strings.ToLower(o.OutputFormat) {
	case "png":
		args = append(args, "-c:v", "png", "-f", "image2pipe")
	default: // jpeg
		args = append(args,
			"-c:v", "mjpeg",
			"-q:v", fmt.Sprint(QualityToQV(o.JPEGQuality)),
			"-pix_fmt", "yuvj420p",
			"-f", "image2pipe",
		)
	}
	return append(args, "pipe:1")
}

// QualityToQV maps a 1..100 quality onto ffmpeg's -q:v scale (2 best..31 worst).
func QualityToQV(quality int) int {
	q := 31 - quality*29/100
	if q < 2 {
		q = 2
	}
	if q > 31 {
		q = 31
	}
	return q
}

// deviceInput returns the libavdevice input format and path for the platform.
func deviceInput(goos string, dev Device) (format, path string) {
	if goos == "windows" {
		// dev.ID carries the DirectShow device name on Windows.
		return "dshow", "video=" + dev.ID
	}
	p := dev.Path
	if p == "" {
		p = dev.ID
	}
	return "v4l2", p
}

// BuildListArgs returns the dshow device-list command (Windows only); ffmpeg
// is expected to exit non-zero here — that is not an error.
func BuildListArgs(ffmpegPath string) (string, []string) {
	return ffmpegPath, []string{
		"-hide_banner", "-loglevel", "info", "-nostats",
		"-list_devices", "true", "-f", "dshow", "-i", "dummy",
	}
}

// seconds renders a duration the way ffmpeg accepts timestamps.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.2f", d.Seconds())
}
