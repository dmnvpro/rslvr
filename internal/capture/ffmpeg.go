package capture

import (
	"context"
	"fmt"
	"runtime"
)

// execer runs an external program, collecting its output. Implementations
// must kill the process subtree on context cancellation (ADR-0001).
type execer interface {
	Run(ctx context.Context, path string, args []string) (stdout, stderr []byte, err error)
}

// goos is overridden in tests.
var goos = func() string { return runtime.GOOS }

// FFMpeg is the external-process capture backend, the only implemented one.
type FFMpeg struct {
	opts Options
	exec execer
}

// New builds the backend that shells out to the configured ffmpeg binary.
func New(o Options) *FFMpeg {
	return &FFMpeg{opts: o, exec: defaultExecer{}}
}

// NewWithExecer injects a custom process runner (tests).
func NewWithExecer(o Options, e execer) *FFMpeg {
	return &FFMpeg{opts: o, exec: e}
}

// List enumerates video devices using the platform backend
// (V4L2 ioctl on Linux, dshow listing via ffmpeg on Windows).
func (f *FFMpeg) List(ctx context.Context) ([]Device, error) {
	return f.list(ctx)
}

// Grab captures one frame from dev and validates it before returning.
func (f *FFMpeg) Grab(ctx context.Context, dev Device) ([]byte, string, error) {
	args := BuildGrabArgs(goos(), dev, f.opts)
	stdout, stderr, err := f.exec.Run(ctx, f.opts.FFmpegPath, args)

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrTimeout, stderrTail(string(stderr)))
	}
	if err != nil {
		return nil, "", ClassifyGrabFailure(err, string(stderr), len(stdout))
	}
	mime, verr := ValidateFrame(stdout)
	if verr != nil {
		// ffmpeg exited cleanly but produced no usable frame.
		return nil, "", fmt.Errorf("%w: %v", ErrNoSignal, verr)
	}
	return stdout, mime, nil
}

// Close implements Capturer; processes are transient, nothing to release.
func (f *FFMpeg) Close() error { return nil }
