//go:build !windows

package app

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"rslvr/internal/capture"
	"rslvr/internal/vision"
)

func TestServe_GracefulShutdownOnSIGINT_Exit0(t *testing.T) {
	installBackends(t, &fakeCap{
		devices: []capture.Device{{Index: 0, ID: "d0", Path: "d0"}},
	}, &fakeAna{res: vision.Result{Answer: "A | x"}})

	port := freePort(t)
	code := make(chan int, 1)
	go func() {
		c, _, _ := runApp(t, "--api-key", "k",
			"--listen", fmt.Sprintf("127.0.0.1:%d", port))
		code <- c
	}()

	// Wait until the server answers, then emulate Ctrl+C.
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(3 * time.Second)
	serving := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/health")
		if err == nil {
			resp.Body.Close()
			serving = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !serving {
		t.Fatal("server did not come up")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	select {
	case c := <-code:
		if c != ExitOK {
			t.Fatalf("serve exit = %d, want 0", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit after SIGINT")
	}
}

// freePort grabs an ephemeral TCP port for the test server.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
