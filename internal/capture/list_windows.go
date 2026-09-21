//go:build windows

package capture

import "context"

// list enumerates DirectShow devices through ffmpeg. ffmpeg exits non-zero
// for -list_devices by design, so only the stderr text matters here.
func (f *FFMpeg) list(ctx context.Context) ([]Device, error) {
	path, args := BuildListArgs(f.opts.FFmpegPath)
	_, stderr, err := f.exec.Run(ctx, path, args)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil && len(stderr) == 0 {
		return nil, err
	}
	devs := ParseDshowDevices(string(stderr))
	if len(devs) == 0 {
		return nil, ErrNoDevices
	}
	return devs, nil
}
