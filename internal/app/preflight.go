package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"rslvr/internal/capture"
	"rslvr/internal/vision"
)

// preflightTimeout bounds each startup check.
const preflightTimeout = 5 * time.Second

// checkFFmpeg verifies the external binary is present and runnable.
func checkFFmpeg(ctx context.Context, path string) error {
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("ffmpeg not found: install it (Windows: winget install Gyan.FFMBuilds / https://www.gyan.dev/ffmpeg/builds, Linux: apt install ffmpeg) and make sure %q is on PATH", path)
	}
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return fmt.Errorf("ffmpeg found at %q but does not run: %w", path, err)
	}
	first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	slog.Debug("ffmpeg ok", slog.String("version", first))
	return nil
}

// requireDevices fails with the "no devices" error when enumeration is empty.
func requireDevices(ctx context.Context, capt capture.Capturer) error {
	devs, err := capt.List(ctx)
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		return capture.ErrNoDevices
	}
	return nil
}

// checkAPIKey implements the settled startup policy: a missing or rejected
// key is fatal (exit 3), a network-level failure is only a warning because
// the app may still be useful once connectivity returns.
func checkAPIKey(ctx context.Context, ana vision.Analyzer, log *slog.Logger) int {
	openai, ok := ana.(*vision.OpenAI)
	if !ok {
		return ExitOK
	}
	err := openai.Preflight(ctx)
	switch {
	case err == nil:
		log.Debug("API key check passed")
		return ExitOK
	case errors.Is(err, vision.ErrNoAPIKey):
		log.Error("no API key configured: set OPENAI_API_KEY, put it in .env, or pass --api-key")
		return ExitAPI
	case errors.Is(err, vision.ErrAuth):
		log.Error("API rejected the key", slog.String("err", err.Error()))
		return ExitAPI
	default:
		log.Warn("API key check failed (connectivity?) — continuing, will retry per request",
			slog.String("err", err.Error()))
		return ExitOK
	}
}

// printDevices writes the device table.
func printDevices(devs []capture.Device, stdout io.Writer) {
	for _, d := range devs {
		fmt.Fprintf(stdout, "[%d]\t%s\t%s\n", d.Index, d.ID, d.Name)
	}
}

// openBrowser opens the URL with the platform default browser; failure is
// logged at debug level because it is purely a convenience.
func openBrowser(url string, log *slog.Logger) {
	var cmd *exec.Cmd
	switch goos {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Debug("cannot open browser", slog.String("url", url), slog.String("err", err.Error()))
		return
	}
	// Do not wait for the browser; detach.
	go func() { _ = cmd.Wait() }()
}
