//go:build !linux && !windows

package capture

import "context"

// list is a stub for platforms outside the supported set (macOS is out of
// scope for now — see the settled platform decision in the project docs).
func (f *FFMpeg) list(ctx context.Context) ([]Device, error) {
	return nil, ErrNoBackend
}
