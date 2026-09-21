package capture

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.White)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateFrame(t *testing.T) {
	mime, err := ValidateFrame(testJPEG(t))
	if err != nil || mime != "image/jpeg" {
		t.Errorf("jpeg: mime=%q err=%v", mime, err)
	}
	mime, err = ValidateFrame(testPNG(t))
	if err != nil || mime != "image/png" {
		t.Errorf("png: mime=%q err=%v", mime, err)
	}
	if _, err := ValidateFrame(nil); err == nil {
		t.Error("empty frame must fail")
	}
	if _, err := ValidateFrame([]byte("not an image at all")); err == nil {
		t.Error("garbage must fail")
	}
}

func TestResolve(t *testing.T) {
	devs := []Device{
		{Index: 0, ID: "/dev/video0", Name: "USB Camera", Path: "/dev/video0"},
		{Index: 1, ID: "/dev/video2", Name: "AVerMedia Capture", Path: "/dev/video2"},
	}

	d, err := Resolve("", devs)
	if err != nil || d.ID != "/dev/video0" {
		t.Errorf("empty selector: %+v %v", d, err)
	}
	d, err = Resolve("/dev/video2", devs)
	if err != nil || d.ID != "/dev/video2" {
		t.Errorf("id selector: %+v %v", d, err)
	}
	d, err = Resolve("1", devs)
	if err != nil || d.ID != "/dev/video2" {
		t.Errorf("index selector: %+v %v", d, err)
	}
	d, err = Resolve("avermedia", devs)
	if err != nil || d.ID != "/dev/video2" {
		t.Errorf("name substring selector: %+v %v", d, err)
	}
	if _, err := Resolve("9", devs); err == nil {
		t.Error("missing index must fail")
	}
	if _, err := Resolve("nonexistent", devs); err == nil {
		t.Error("missing name must fail")
	}
	if _, err := Resolve("", nil); err == nil {
		t.Error("empty device list must fail")
	}
}
