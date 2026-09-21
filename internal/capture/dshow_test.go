package capture

import (
	"os"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseDshowDevices_FFMpeg7Fixture(t *testing.T) {
	devs := ParseDshowDevices(readFixture(t, "dshow_ffmpeg7.txt"))
	want := []string{"USB2.0 HD UVC WebCam", "OBS Virtual Camera", "AVerMedia USB2.0 Capture"}
	if len(devs) != len(want) {
		t.Fatalf("got %d devices %+v, want %d", len(devs), devs, len(want))
	}
	for i, name := range want {
		if devs[i].Name != name || devs[i].ID != name {
			t.Errorf("devs[%d] = %+v, want name/id %q", i, devs[i], name)
		}
		if devs[i].Index != i {
			t.Errorf("devs[%d].Index = %d", i, devs[i].Index)
		}
	}
}

func TestParseDshowDevices_FFMpeg44Fixture(t *testing.T) {
	devs := ParseDshowDevices(readFixture(t, "dshow_ffmpeg44.txt"))
	// Duplicate name must be deduped; audio section must be skipped.
	if len(devs) != 1 || devs[0].Name != "Integrated Webcam" {
		t.Fatalf("devs = %+v, want one Integrated Webcam", devs)
	}
}

func TestParseDshowDevices_IgnoresAudioSection(t *testing.T) {
	out := `[dshow @ 0x1] DirectShow audio devices
[dshow @ 0x1] "Stereo Mix" (audio)
[dshow @ 0x1] "Mic" (audio)
`
	if devs := ParseDshowDevices(out); len(devs) != 0 {
		t.Errorf("audio-only output must yield no devices, got %+v", devs)
	}
}

func TestParseDshowDevices_SkipsAlternativeNames(t *testing.T) {
	out := `[dshow @ 0x1] DirectShow video devices (some may be both audio and video)
[dshow @ 0x1] "Cam A" (video)
[dshow @ 0x1]  Alternative name "@device_pnp_xxx"
[dshow @ 0x1]   Alternative name "@device_pnp_yyy"
`
	devs := ParseDshowDevices(out)
	if len(devs) != 1 || devs[0].Name != "Cam A" {
		t.Fatalf("devs = %+v, want single Cam A", devs)
	}
}

func TestParseDshowDevices_Empty(t *testing.T) {
	if devs := ParseDshowDevices(""); len(devs) != 0 {
		t.Errorf("empty input must yield no devices, got %+v", devs)
	}
}

func TestParseDshowDevices_ToleratesMissingPrefix(t *testing.T) {
	out := `DirectShow video devices
"Plain Cam" (video)
`
	devs := ParseDshowDevices(out)
	if len(devs) != 1 || devs[0].Name != "Plain Cam" {
		t.Fatalf("devs = %+v, want Plain Cam", devs)
	}
}
