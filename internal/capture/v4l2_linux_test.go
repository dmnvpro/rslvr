//go:build linux

package capture

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeV4L2 maps device paths to capabilities, failing on "broken" paths.
func fakeV4L2(nodes map[string]v4l2Capability, broken map[string]bool) func(string) (v4l2Capability, error) {
	return func(path string) (v4l2Capability, error) {
		if broken[path] {
			return v4l2Capability{}, os.ErrPermission
		}
		c, ok := nodes[path]
		if !ok {
			return v4l2Capability{}, os.ErrNotExist
		}
		return c, nil
	}
}

func capNode(card, bus string, capture bool) v4l2Capability {
	var c v4l2Capability
	copy(c.Card[:], card)
	copy(c.BusInfo[:], bus)
	if capture {
		c.Capabilities = v4l2CapVideoCapture | 0x84200000
		c.DeviceCaps = v4l2CapVideoCapture
	}
	// metadata nodes carry no CAPTURE bit
	return c
}

func TestListV4L2_SortFilterDedupe(t *testing.T) {
	root := t.TempDir()
	// video10 > video9 numerically; video1 is a metadata twin of video0.
	for _, name := range []string{"video10", "video9", "video1", "video0"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	nodes := map[string]v4l2Capability{
		filepath.Join(root, "video0"):  capNode("USB Camera", "usb-0000:00:14.0-1", true),
		filepath.Join(root, "video1"):  capNode("USB Camera", "usb-0000:00:14.0-1", false), // metadata
		filepath.Join(root, "video9"):  capNode("Capture Card", "usb-0000:00:14.0-2", true),
		filepath.Join(root, "video10"): capNode("Second Feed", "usb-0000:00:14.0-2", true), // same bus as video9
	}
	// Same bus for video9/video10 → merged, lower path wins (video9).
	devs, err := listV4L2(root, fakeV4L2(nodes, nil))
	if err != nil {
		t.Fatalf("listV4L2: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("got %d devices: %+v, want 2 (twin nodes merged)", len(devs), devs)
	}
	if devs[0].Path != filepath.Join(root, "video0") || devs[0].Name != "USB Camera" {
		t.Errorf("devs[0] = %+v", devs[0])
	}
	if devs[1].Path != filepath.Join(root, "video9") {
		t.Errorf("dedupe must keep lower path, got %+v", devs[1])
	}
	for i, d := range devs {
		if d.Index != i || d.ID != d.Path {
			t.Errorf("devs[%d] = %+v (index/id)", i, d)
		}
	}
}

func TestListV4L2_SkipsBrokenNodes(t *testing.T) {
	root := t.TempDir()
	p0 := filepath.Join(root, "video0")
	p1 := filepath.Join(root, "video1")
	for _, p := range []string{p0, p1} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	nodes := map[string]v4l2Capability{p0: capNode("Good Cam", "bus-a", true)}
	devs, err := listV4L2(root, fakeV4L2(nodes, map[string]bool{p1: true}))
	if err != nil {
		t.Fatalf("listV4L2: %v", err)
	}
	if len(devs) != 1 || devs[0].Path != p0 {
		t.Fatalf("devs = %+v, want only video0", devs)
	}
}

func TestListV4L2_EmptyReturnsErrNoDevices(t *testing.T) {
	root := t.TempDir() // no devices at all
	if _, err := listV4L2(root, fakeV4L2(nil, nil)); err != ErrNoDevices {
		t.Fatalf("err = %v, want ErrNoDevices", err)
	}
}

func TestListV4L2_NoCaptureCapabilityFiltered(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "video0")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]v4l2Capability{p: capNode("Meta Only", "bus-b", false)}
	if _, err := listV4L2(root, fakeV4L2(nodes, nil)); err != ErrNoDevices {
		t.Fatalf("err = %v, want ErrNoDevices (metadata node must be filtered)", err)
	}
}

func TestSortVideosNumeric(t *testing.T) {
	paths := []string{"/dev/video10", "/dev/video2", "/dev/video0", "/dev/video9"}
	sortVideos(paths)
	want := []string{"/dev/video0", "/dev/video2", "/dev/video9", "/dev/video10"}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("sorted = %v, want %v", paths, want)
		}
	}
}
