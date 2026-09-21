package app

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"rslvr/internal/capture"
	"rslvr/internal/vision"
)

// ---- fakes ----

type fakeCap struct {
	devices []capture.Device
}

func (f *fakeCap) List(context.Context) ([]capture.Device, error) {
	if len(f.devices) == 0 {
		return nil, capture.ErrNoDevices
	}
	return f.devices, nil
}

func (f *fakeCap) Grab(context.Context, capture.Device) ([]byte, string, error) {
	return nil, "", nil
}

func (f *fakeCap) Close() error { return nil }

type fakeAna struct {
	res vision.Result
	err error
}

func (f *fakeAna) Analyze(context.Context, []vision.Frame) (vision.Result, error) {
	return f.res, f.err
}

// installBackends swaps the capture/vision seams for the duration of the test.
func installBackends(t *testing.T, capt capture.Capturer, ana vision.Analyzer) {
	t.Helper()
	oldCap, oldAna := newCapturer, newAnalyzer
	newCapturer = func(capture.Options) capture.Capturer { return capt }
	newAnalyzer = func(vision.Options) vision.Analyzer { return ana }
	t.Cleanup(func() { newCapturer, newAnalyzer = oldCap, oldAna })
}

// runApp runs Run with a clean working dir and returns exit code + outputs.
func runApp(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	t.Chdir(t.TempDir()) // isolate from real .env / config.yaml
	var stdout, stderr bytes.Buffer
	code := Run(argv, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestVersionFlag(t *testing.T) {
	code, out, _ := runApp(t, "--version")
	if code != ExitOK || !strings.Contains(out, "rslvr "+Version) {
		t.Errorf("code=%d out=%q", code, out)
	}
	code, out, _ = runApp(t, "-version")
	if code != ExitOK || !strings.Contains(out, Version) {
		t.Errorf("short form: code=%d out=%q", code, out)
	}
}

func TestHelpFlag(t *testing.T) {
	code, out, _ := runApp(t, "--help")
	if code != ExitOK || !strings.Contains(out, "-list-devices") {
		t.Errorf("code=%d out=%q", code, out)
	}
}

func TestListDevices_Exit0Prints(t *testing.T) {
	installBackends(t, &fakeCap{
		devices: []capture.Device{
			{Index: 0, ID: "/dev/video0", Name: "USB Cam", Path: "/dev/video0"},
			{Index: 1, ID: "/dev/video2", Name: "Capture Card", Path: "/dev/video2"},
		},
	}, &fakeAna{})
	code, out, errOut := runApp(t, "--list-devices")
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{"[0]", "/dev/video0", "USB Cam", "[1]", "Capture Card"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestListDevices_NoDevices_Exit2(t *testing.T) {
	installBackends(t, &fakeCap{}, &fakeAna{})
	code, _, errOut := runApp(t, "--list-devices")
	if code != ExitNoDevice {
		t.Fatalf("code=%d, want %d; stderr=%q", code, ExitNoDevice, errOut)
	}
	if !strings.Contains(errOut, "no video devices") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestHasFlag(t *testing.T) {
	if !hasFlag([]string{"--config=x", "--timeout=5s"}, "timeout") {
		t.Error("value form not detected")
	}
	if !hasFlag([]string{"-version"}, "version") {
		t.Error("single dash not detected")
	}
	if hasFlag([]string{"--version"}, "versionX") {
		t.Error("false positive")
	}
}

func TestPrintDevices(t *testing.T) {
	var buf bytes.Buffer
	printDevices([]capture.Device{{Index: 2, ID: "x", Name: "Webcam"}}, &buf)
	if !strings.Contains(buf.String(), "[2]\tx\tWebcam") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestVersionDefault(t *testing.T) {
	if Version == "" {
		t.Error("Version must have a default")
	}
	if _, err := os.Stat("."); err != nil {
		t.Error("cwd vanished")
	}
}
