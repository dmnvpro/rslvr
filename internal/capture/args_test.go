package capture

import (
	"strings"
	"testing"
	"time"
)

var grabOpts = Options{FFmpegPath: "/usr/bin/ffmpeg"}

func TestBuildGrabArgs_LinuxWarmup(t *testing.T) {
	dev := Device{ID: "/dev/video2", Path: "/dev/video2"}
	args := BuildGrabArgs("linux", dev, Options{Warmup: 700 * time.Millisecond})
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-ss 0.70") {
		t.Errorf("warmup must appear as input-side -ss, got %s", joined)
	}
	if strings.Index(joined, "-ss") > strings.Index(joined, "-i ") {
		t.Errorf("-ss must precede -i (input option), got %s", joined)
	}
	if !strings.Contains(joined, "-f v4l2 -i /dev/video2") {
		t.Errorf("v4l2 input missing, got %s", joined)
	}
	if !strings.Contains(joined, "-frames:v 1") {
		t.Errorf("single frame limit missing, got %s", joined)
	}
	if !strings.Contains(joined, "-f image2pipe pipe:1") {
		t.Errorf("pipe output missing, got %s", joined)
	}
}

func TestBuildGrabArgs_NoWarmupOmitsSS(t *testing.T) {
	args := BuildGrabArgs("linux", Device{Path: "/dev/video0"}, grabOpts)
	if strings.Contains(strings.Join(args, " "), "-ss") {
		t.Errorf("zero warmup must omit -ss, got %v", args)
	}
}

func TestBuildGrabArgs_WindowsDshow(t *testing.T) {
	dev := Device{ID: "OBS Virtual Camera"}
	args := BuildGrabArgs("windows", dev, grabOpts)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `-f dshow -i video=OBS Virtual Camera`) {
		t.Errorf("dshow input missing, got %s", joined)
	}
	if strings.Contains(joined, "v4l2") {
		t.Errorf("windows must not use v4l2, got %s", joined)
	}
}

// Frames are always lossless PNG: no lossy jpeg encoder or quality knobs.
func TestBuildGrabArgs_AlwaysLosslessPNG(t *testing.T) {
	args := BuildGrabArgs("linux", Device{Path: "/dev/video0"},
		Options{Warmup: time.Second})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-q:v") || strings.Contains(joined, "mjpeg") {
		t.Errorf("lossless capture must not carry jpeg encoder flags, got %s", joined)
	}
	if !strings.Contains(joined, "-c:v png") {
		t.Errorf("png encoder missing, got %s", joined)
	}
}

func TestDeviceInput_FallsBackToID(t *testing.T) {
	format, path := deviceInput("linux", Device{ID: "/dev/video7"})
	if format != "v4l2" || path != "/dev/video7" {
		t.Errorf("deviceInput linux = %q, %q", format, path)
	}
}
