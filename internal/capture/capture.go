// Package capture lists video devices and grabs single frames through an
// external ffmpeg process (see docs/adr/0001-capture-via-external-ffmpeg.md).
package capture

import (
	"bytes"
	"context"
	"fmt"
	"image"
	// Decoders for image.DecodeConfig frame validation (TechSpec §7).
	_ "image/jpeg"
	_ "image/png"
	"strconv"
	"strings"
)

// MaxFrameSize bounds the in-memory frame read from ffmpeg.
const MaxFrameSize = 20 << 20 // 20 MB

// Device is a video input visible to the OS (see CONTEXT.md).
type Device struct {
	Index int    `json:"index"`
	ID    string `json:"id"`             // stable selector: /dev/video0 | dshow name | path
	Name  string `json:"name"`           // human-readable card name
	Path  string `json:"path,omitempty"` // device node, when the platform has one
}

// Capturer abstracts the frame source; tests inject fakes.
type Capturer interface {
	List(ctx context.Context) ([]Device, error)
	// Grab returns one frame (JPEG or PNG bytes) and its MIME type.
	Grab(ctx context.Context, dev Device) ([]byte, string, error)
	Close() error
}

// Resolve maps a user selector ("", index string, id or name substring) to a
// device from the list. Empty selector picks the first device.
func Resolve(selector string, devs []Device) (Device, error) {
	if len(devs) == 0 {
		return Device{}, ErrNoDevices
	}
	if selector == "" {
		return devs[0], nil
	}
	for _, d := range devs {
		if d.ID == selector || d.Path == selector {
			return d, nil
		}
	}
	if idx, err := strconv.Atoi(selector); err == nil {
		for _, d := range devs {
			if d.Index == idx {
				return d, nil
			}
		}
		return Device{}, fmt.Errorf("video device with index %d not found", idx)
	}
	sel := strings.ToLower(selector)
	for _, d := range devs {
		if strings.Contains(strings.ToLower(d.Name), sel) {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("video device %q not found", selector)
}

// ValidateFrame checks size bounds and that the bytes decode as an image.
// It returns the detected MIME type ("image/jpeg" or "image/png").
func ValidateFrame(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("frame is empty")
	}
	if len(data) > MaxFrameSize {
		return "", fmt.Errorf("frame exceeds %d bytes limit", MaxFrameSize)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("frame is not a decodable image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return "", fmt.Errorf("frame has invalid dimensions %dx%d", cfg.Width, cfg.Height)
	}
	switch format {
	case "jpeg":
		return "image/jpeg", nil
	case "png":
		return "image/png", nil
	default:
		return "", fmt.Errorf("frame format %q is not supported", format)
	}
}
