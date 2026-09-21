package capture

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeExecer returns canned results per invocation.
type fakeExecer struct {
	stdout     []byte
	stderr     []byte
	err        error
	gotPath    string
	gotArgs    []string
	calls      int
	blockUntil chan struct{} // if set, Run blocks until closed or ctx done
}

func (f *fakeExecer) Run(ctx context.Context, path string, args []string) ([]byte, []byte, error) {
	f.calls++
	f.gotPath, f.gotArgs = path, args
	if f.blockUntil != nil {
		select {
		case <-f.blockUntil:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	return f.stdout, f.stderr, f.err
}

func TestGrab_Success(t *testing.T) {
	frame := testJPEG(t)
	ex := &fakeExecer{stdout: frame}
	capt := NewWithExecer(Options{FFmpegPath: "ffmpeg"}, ex)

	dev := Device{ID: "/dev/video0", Path: "/dev/video0"}
	got, mime, err := capt.Grab(context.Background(), dev)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if !bytes.Equal(got, frame) || mime != "image/jpeg" {
		t.Errorf("got %d bytes mime=%s", len(got), mime)
	}
	if ex.gotPath != "ffmpeg" {
		t.Errorf("exec path = %q", ex.gotPath)
	}
	if !strings.Contains(strings.Join(ex.gotArgs, " "), "/dev/video0") {
		t.Errorf("args must reference the device, got %v", ex.gotArgs)
	}
}

func TestGrab_DeviceBusy(t *testing.T) {
	ex := &fakeExecer{err: errors.New("exit status 1"), stderr: []byte("Device or resource busy")}
	capt := NewWithExecer(grabOpts, ex)
	_, _, err := capt.Grab(context.Background(), Device{Path: "/dev/video0"})
	if !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("err = %v, want ErrDeviceBusy", err)
	}
}

func TestGrab_ContextCancelMapsToTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	ex := &fakeExecer{blockUntil: block}
	capt := NewWithExecer(grabOpts, ex)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := capt.Grab(ctx, Device{Path: "/dev/video0"})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestGrab_GarbageFrameMapsToNoSignal(t *testing.T) {
	ex := &fakeExecer{stdout: []byte("definitely not an image")}
	capt := NewWithExecer(grabOpts, ex)
	_, _, err := capt.Grab(context.Background(), Device{Path: "/dev/video0"})
	if !errors.Is(err, ErrNoSignal) {
		t.Fatalf("err = %v, want ErrNoSignal", err)
	}
}

func TestClose_Noop(t *testing.T) {
	if err := New(grabOpts).Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}
