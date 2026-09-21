package capture

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestClassifyGrabFailure(t *testing.T) {
	cases := []struct {
		name     string
		exitErr  error
		stderr   string
		gotBytes int
		want     error
	}{
		{
			name:    "busy",
			exitErr: errors.New("exit status 1"),
			stderr:  "/dev/video0: Device or resource busy",
			want:    ErrDeviceBusy,
		},
		{
			name:    "busy dshow wording",
			exitErr: errors.New("exit status 1"),
			stderr:  `Could not open video capture device: the device is being used by another application`,
			want:    ErrDeviceBusy,
		},
		{
			name:    "no signal",
			exitErr: errors.New("exit status 1"),
			stderr:  "ioctl(VIDIOC_DQBUF): Input/output error",
			want:    ErrNoSignal,
		},
		{
			name:    "no such device",
			exitErr: errors.New("exit status 1"),
			stderr:  "No such device",
			want:    ErrNoSignal,
		},
		{
			name: "silent zero bytes with success exit",
			want: ErrNoSignal,
		},
		{
			name:     "clean exit but garbage bytes",
			exitErr:  nil,
			stderr:   "weird noise",
			gotBytes: 10,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ClassifyGrabFailure(c.exitErr, c.stderr, c.gotBytes)
			if err == nil {
				t.Fatal("expected an error")
			}
			if c.want == nil {
				for _, sentinel := range []error{ErrDeviceBusy, ErrNoSignal, ErrTimeout} {
					if errors.Is(err, sentinel) {
						t.Fatalf("got sentinel %v, want generic", sentinel)
					}
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestClassifyGrabFailure_ContextDeadline(t *testing.T) {
	wrapped := fmt.Errorf("grab: %w", context.DeadlineExceeded)
	if err := ClassifyGrabFailure(wrapped, "", 0); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if err := ClassifyGrabFailure(context.DeadlineExceeded, "", 0); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if err := ClassifyGrabFailure(fmt.Errorf("grab: %w", context.Canceled), "", 0); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestStderrTail_TrimsAndShortens(t *testing.T) {
	if got := stderrTail("\n\n"); got != "" {
		t.Errorf("blank tail = %q", got)
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += fmt.Sprintf("line%d more noise padding padding\n", i)
	}
	got := stderrTail(long)
	if len(got) > 400 {
		t.Errorf("tail too long: %d", len(got))
	}
	if !containsAny(got, []string{"line99"}) {
		t.Errorf("tail must keep the last lines, got %q", got)
	}
}
