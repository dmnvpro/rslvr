//go:build windows

package capture

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

type defaultExecer struct{}

const createNewProcessGroup = 0x00000200

// Run starts ffmpeg in a new process group; cancellation kills the direct
// child (ffmpeg does not normally spawn grandchildren for frame grabs).
func (defaultExecer) Run(ctx context.Context, path string, args []string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 3 * time.Second

	var out, errb limitedBuffer
	out.limit = MaxFrameSize
	errb.limit = 64 << 10
	cmd.Stdout = &out
	cmd.Stderr = &errb

	runErr := cmd.Run()
	return out.data(), errb.data(), runErr
}
