# Capture via external ffmpeg process; no gocv

Frame capture shells out to an `ffmpeg` binary instead of linking a capture
library (gocv/OpenCV) or implementing V4L2/DirectShow capture natively. ffmpeg
is the only backend: the `capture.backend` config key remains as a seam, and
any value other than `ffmpeg` is a configuration error for now. Device
enumeration itself stays pure Go (Linux: `/dev/video*` + ioctl
`VIDIOC_QUERYCAP`; Windows: parsing `ffmpeg -list_devices` stderr).

Why: keeps the binary cgo-free — simple cross-compilation to windows/amd64,
single small binary, no OpenCV toolchain — works uniformly across Linux (v4l2)
and Windows (dshow), and ffmpeg is a precondition the app already checks at
startup.

## Considered Options

- **gocv**: rejected — cgo + OpenCV dependency breaks cross-compilation and the
  "no mandatory cgo" constraint of this project.
- **Native V4L2/DirectShow capture in Go**: rejected — two platform-specific
  implementations to maintain for no user-visible gain over exec'ing ffmpeg.

## Consequences

- Users must install ffmpeg (README covers Windows/Linux installation).
- Frame grabbing inherits ffmpeg process semantics: process-group kill on
  context cancel, warmup delay before the frame is read from the pipe.
