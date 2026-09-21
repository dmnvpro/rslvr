package logx

import (
	"log/slog"
	"testing"
)

func TestMaskKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "***"},
		{"sk-abc", "***"},
		{"sk-abcdef", "sk-***def"},
		{"sk-proj-VERYSECRETKEY-1234567890", "sk-***890"},
	}
	for _, c := range cases {
		if got := MaskKey(c.in); got != c.want {
			t.Errorf("MaskKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"": slog.LevelInfo, "info": slog.LevelInfo, "INFO": slog.LevelInfo,
		"debug": slog.LevelDebug, "warn": slog.LevelWarn, "warning": slog.LevelWarn,
		"error": slog.LevelError,
	} {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v, nil", in, got, err, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("ParseLevel(loud) should fail")
	}
}

func TestNewFormats(t *testing.T) {
	for _, format := range []string{"text", "json", ""} {
		if _, err := New("info", format); err != nil {
			t.Errorf("New(info, %q): %v", format, err)
		}
	}
	if _, err := New("info", "yaml"); err == nil {
		t.Error("New with unknown format should fail")
	}
	if _, err := New("debug", "json"); err != nil {
		t.Errorf("New(debug, json): %v", err)
	}
}
