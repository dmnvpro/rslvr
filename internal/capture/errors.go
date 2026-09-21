package capture

import "errors"

// Sentinel errors surfaced by capture backends. Callers map them to user
// messages and HTTP status codes; human-readable context is attached with %w.
var (
	// ErrDeviceBusy means the device is held by another application
	// (a streaming studio keeping the capture card open is the typical case).
	ErrDeviceBusy = errors.New("video device is busy (is another application using it?)")

	// ErrNoSignal means the device opened but produced no usable frame
	// (capture card with no feed, cable out, wrong input).
	ErrNoSignal = errors.New("no signal from video device")

	// ErrTimeout means the grab did not complete within its deadline.
	ErrTimeout = errors.New("grab timed out")

	// ErrNoDevices means device enumeration found nothing.
	ErrNoDevices = errors.New("no video devices found")

	// ErrNoBackend means capture is not implemented on this platform.
	ErrNoBackend = errors.New("capture backend not implemented on this platform")
)
