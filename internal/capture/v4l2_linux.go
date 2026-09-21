//go:build linux

package capture

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// V4L2 bits used for enumeration. golang.org/x/sys/unix does not export the
// V4L2 API, so the ioctl number and the capability struct are defined here
// (see docs/adr/0001: enumeration stays pure Go, no v4l2-ctl dependency).
const (
	// vidiocQuerycap is _IOR('V', 0, v4l2_capability) — 104-byte read.
	vidiocQuerycap = 0x80685600
	// v4l2CapVideoCapture is V4L2_CAP_VIDEO_CAPTURE.
	v4l2CapVideoCapture = 0x00000001
)

// v4l2Capability mirrors struct v4l2_capability (104 bytes).
type v4l2Capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	Reserved     [3]uint32
}

// list enumerates capture-capable V4L2 nodes.
func (f *FFMpeg) list(_ context.Context) ([]Device, error) {
	return listV4L2("/dev", queryCap)
}

// queryCap opens the node read-only and issues VIDIOC_QUERYCAP.
func queryCap(path string) (v4l2Capability, error) {
	f, err := os.Open(path)
	if err != nil {
		return v4l2Capability{}, err
	}
	defer f.Close()
	var c v4l2Capability
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), vidiocQuerycap, uintptr(unsafe.Pointer(&c)))
	if errno != 0 {
		return v4l2Capability{}, errno
	}
	return c, nil
}

// listV4L2 scans root/video*, keeps capture-capable nodes and merges the
// cameras that expose several nodes (capture + metadata twins share bus info).
// query is injectable so tests never need real hardware.
func listV4L2(root string, query func(string) (v4l2Capability, error)) ([]Device, error) {
	matches, _ := filepath.Glob(filepath.Join(root, "video*"))
	sortVideos(matches)

	type node struct {
		path string
		cap  v4l2Capability
	}
	var captureNodes []node
	busIndex := map[string]int{}
	for _, p := range matches {
		c, err := query(p)
		if err != nil {
			continue // node disappeared or is not readable; skip quietly
		}
		if !isCaptureNode(c) {
			continue // metadata / radio / tuner-only node
		}
		bus := cStr(c.BusInfo)
		if bus != "" {
			if _, ok := busIndex[bus]; ok {
				// Same camera behind two capture nodes: the scan is
				// numerically sorted, so the first occurrence is kept.
				continue
			}
			busIndex[bus] = len(captureNodes)
		}
		captureNodes = append(captureNodes, node{p, c})
	}
	if len(captureNodes) == 0 {
		return nil, ErrNoDevices
	}

	devs := make([]Device, 0, len(captureNodes))
	for i, n := range captureNodes {
		name := cStr(n.cap.Card)
		if name == "" {
			name = filepath.Base(n.path)
		}
		devs = append(devs, Device{Index: i, ID: n.path, Name: name, Path: n.path})
	}
	return devs, nil
}

func isCaptureNode(c v4l2Capability) bool {
	return c.Capabilities&v4l2CapVideoCapture != 0 ||
		c.DeviceCaps&v4l2CapVideoCapture != 0
}

// sortVideos orders /dev/video10 after /dev/video9 (numeric, not lexical).
func sortVideos(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		return videoNum(paths[i]) < videoNum(paths[j])
	})
}

func videoNum(p string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(p), "video"))
	return n
}

func cStr(b [32]byte) string {
	return strings.TrimRight(string(b[:]), "\x00")
}
