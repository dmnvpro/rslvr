//go:build linux

package capture

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

type defaultExecer struct{}

// Run starts the process in its own process group so cancellation kills the
// whole subtree, not just the ffmpeg parent.
func (defaultExecer) Run(ctx context.Context, path string, args []string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			// Negative PID targets the process group.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	// Bound the wait if the child ignores the pipe teardown.
	cmd.WaitDelay = 3 * time.Second

	var out, errb limitedBuffer
	out.limit = MaxFrameSize
	errb.limit = 64 << 10
	cmd.Stdout = &out
	cmd.Stderr = &errb

	runErr := cmd.Run()
	return out.data(), errb.data(), runErr
}
