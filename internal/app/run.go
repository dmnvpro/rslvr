// Package app wires configuration, capture, vision, jobs and the HTTP server
// into the CLI modes and maps failures onto the documented exit codes.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"rslvr/internal/capture"
	"rslvr/internal/config"
	"rslvr/internal/jobs"
	"rslvr/internal/logx"
	"rslvr/internal/server"
	"rslvr/internal/vision"
)

// Exit codes (TechSpec §5): 0 success, 1 general error, 2 no devices,
// 3 API error (no key / auth / upstream / unreadable), 4 timeout.
const (
	ExitOK       = 0
	ExitGeneral  = 1
	ExitNoDevice = 2
	ExitAPI      = 3
	ExitTimeout  = 4
)

// cacheTTL is the answer cache window (TechSpec §10).
const cacheTTL = 30 * time.Second

// Seams for tests: fakes replace the real capture/vision backends and the
// signal context so exit-code paths run without hardware or API keys.
var (
	newCapturer  = func(o capture.Options) capture.Capturer { return capture.New(o) }
	newAnalyzer  = func(o vision.Options) vision.Analyzer { return vision.New(o) }
	newSignalCtx = func(parent context.Context) (context.Context, context.CancelFunc) {
		return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	}
)

// Run executes the CLI and returns the process exit code.
func Run(argv []string, stdout, stderr io.Writer) int {
	// --version must work even with a broken config.
	if hasFlag(argv, "version") {
		fmt.Fprintln(stdout, "rslvr "+Version)
		return ExitOK
	}

	res, err := config.Load(argv, nil)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrVersionRequested):
			fmt.Fprintln(stdout, "rslvr "+Version)
			return ExitOK
		case errors.Is(err, flag.ErrHelp):
			fmt.Fprint(stdout, config.Usage())
			return ExitOK
		default:
			fmt.Fprintln(stderr, "config error:", err)
			return ExitGeneral
		}
	}
	cfg := res.Config

	logger, err := logx.New(cfg.Logging.Level, cfg.Logging.Format)
	if err != nil {
		fmt.Fprintln(stderr, "logger error:", err)
		return ExitGeneral
	}
	for _, w := range res.Warnings {
		logger.Warn(w)
	}

	ctx, stop := newSignalCtx(context.Background())
	defer stop()

	capt := newCapturer(capture.Options{
		FFmpegPath:   cfg.Capture.FFmpegPath,
		OutputFormat: cfg.Capture.OutputFormat,
		JPEGQuality:  cfg.Capture.JPEGQuality,
		Warmup:       cfg.Capture.Warmup,
		Log:          logger,
	})
	defer capt.Close()

	ana := newAnalyzer(vision.Options{
		BaseURL:      cfg.Vision.BaseURL,
		APIKey:       cfg.Vision.APIKey,
		Model:        cfg.Vision.Model,
		MaxTokens:    cfg.Vision.MaxTokens,
		Temperature:  cfg.Vision.Temperature,
		Detail:       cfg.Vision.Detail,
		SystemPrompt: cfg.Vision.SystemPrompt,
		UserPrompt:   cfg.Vision.UserPrompt,
		MaxRetries:   cfg.Vision.MaxRetries,
		RetryBackoff: cfg.Vision.RetryBackoff,
		Log:          logger,
	})

	switch {
	case cfg.ListDevices:
		return runListDevices(ctx, cfg, capt, stdout, stderr)
	case cfg.Once:
		return runOnce(ctx, cfg, capt, ana, logger, stdout, stderr)
	default:
		return runServe(ctx, cfg, capt, ana, logger, stderr)
	}
}

func runListDevices(ctx context.Context, cfg config.Config, capt capture.Capturer, stdout, stderr io.Writer) int {
	if err := checkFFmpeg(ctx, cfg.Capture.FFmpegPath); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return ExitGeneral
	}
	devs, err := capt.List(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		if errors.Is(err, capture.ErrNoDevices) {
			return ExitNoDevice
		}
		return ExitGeneral
	}
	printDevices(devs, stdout)
	return ExitOK
}

func runOnce(ctx context.Context, cfg config.Config, capt capture.Capturer, ana vision.Analyzer, log *slog.Logger, stdout, stderr io.Writer) int {
	if err := checkFFmpeg(ctx, cfg.Capture.FFmpegPath); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return ExitGeneral
	}
	if err := requireDevices(ctx, capt); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return ExitNoDevice
	}
	if code := checkAPIKey(ctx, ana, log); code != ExitOK {
		return code
	}

	runner := jobs.NewRunner(jobs.Options{
		Capturer:      capt,
		Analyzer:      ana,
		Saver:         saverFor(cfg),
		Timeout:       cfg.Timeout,
		VisionTimeout: cfg.Vision.Timeout,
		CacheTTL:      0, // one-shot mode never reuses answers
		Log:           log,
	})

	out, err := runner.Run(ctx, jobs.Request{
		Device: cfg.Capture.Device,
		Prompt: cfg.Vision.UserPrompt,
		Save:   cfg.Capture.SaveDir != "",
	})
	if err != nil {
		log.Error("one-shot run failed", slog.String("err", err.Error()))
		return jobErrExit(err)
	}
	fmt.Fprintln(stdout, out.Result.Answer)
	if out.Result.SavedPath != "" {
		log.Info("frame saved", slog.String("path", out.Result.SavedPath))
	}
	return ExitOK
}

func runServe(ctx context.Context, cfg config.Config, capt capture.Capturer, ana vision.Analyzer, log *slog.Logger, stderr io.Writer) int {
	if err := checkFFmpeg(ctx, cfg.Capture.FFmpegPath); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return ExitGeneral
	}

	// Devices are optional in serve mode: capture cards can be hot-plugged.
	if err := requireDevices(ctx, capt); err != nil {
		log.Warn("no video devices found; the device list stays empty until one appears")
	}
	if code := checkAPIKey(ctx, ana, log); code != ExitOK {
		return code
	}

	runner := jobs.NewRunner(jobs.Options{
		Capturer:      capt,
		Analyzer:      ana,
		Saver:         saverFor(cfg),
		Timeout:       cfg.Timeout,
		VisionTimeout: cfg.Vision.Timeout,
		CacheTTL:      cacheTTLFor(cfg),
		Log:           log,
	})
	runner.SetLimiter(jobs.NewLimiter(cfg.Server.MaxConcurrentJobs))

	httpSrv := &http.Server{
		Addr: cfg.Listen,
		Handler: server.New(server.Deps{
			Cfg: cfg, Runner: runner, Capturer: capt, Log: log,
		}).Handler(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  60 * time.Second,
	}

	// Bind here so a taken port fails fast with a clear error.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Error("cannot listen", slog.String("addr", cfg.Listen), slog.String("err", err.Error()))
		return ExitGeneral
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	log.Info("serving",
		slog.String("addr", "http://"+cfg.Listen),
		slog.String("model", cfg.Vision.Model),
		slog.String("base_url", cfg.Vision.BaseURL),
		slog.String("key", logx.MaskKey(cfg.Vision.APIKey)),
		slog.Int("max_concurrent_jobs", cfg.Server.MaxConcurrentJobs))
	if cfg.OpenBrowser {
		openBrowser("http://"+cfg.Listen, log)
	}

	select {
	case err := <-serveErr:
		log.Error("server failed", slog.String("err", err.Error()))
		return ExitGeneral
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown incomplete", slog.String("err", err.Error()))
	}
	return ExitOK
}

func saverFor(cfg config.Config) *jobs.Saver {
	if cfg.Capture.SaveDir == "" {
		return nil
	}
	return jobs.NewSaver(cfg.Capture.SaveDir)
}

func cacheTTLFor(cfg config.Config) time.Duration {
	if cfg.NoCache {
		return 0
	}
	return cacheTTL
}

// jobErrExit maps a job failure to its exit code: timeouts → 4,
// vision/API failures → 3, everything else → 1.
func jobErrExit(err error) int {
	switch {
	case errors.Is(err, capture.ErrTimeout),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled):
		return ExitTimeout
	case errors.Is(err, vision.ErrUnreadableImage),
		errors.Is(err, vision.ErrNoAPIKey),
		errors.Is(err, vision.ErrAuth),
		errors.Is(err, vision.ErrUpstream):
		return ExitAPI
	default:
		return ExitGeneral
	}
}

// hasFlag reports whether argv contains --name or -name (value forms aside).
func hasFlag(argv []string, name string) bool {
	for _, a := range argv {
		bare := strings.TrimLeft(a, "-")
		if a == "--"+name || a == "-"+name || (len(a) > 1 && a[0] == '-' && strings.HasPrefix(bare, name+"=")) {
			return true
		}
	}
	return false
}
