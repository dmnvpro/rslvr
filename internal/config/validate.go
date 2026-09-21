package config

import (
	"errors"
	"fmt"
	"net"
)

// Validate checks the resolved configuration for semantic errors.
func Validate(cfg *Config) error {
	switch cfg.Capture.Backend {
	case "ffmpeg":
		// the only implemented backend
	case "gocv":
		return errors.New(`capture.backend: "gocv" is not implemented (only the external ffmpeg backend is available)`)
	case "":
		return errors.New("capture.backend must not be empty")
	default:
		return fmt.Errorf("capture.backend: unknown backend %q (implemented: ffmpeg)", cfg.Capture.Backend)
	}

	if cfg.Capture.FFmpegPath == "" {
		return errors.New("capture.ffmpeg_path must not be empty")
	}
	if cfg.Capture.Warmup < 0 {
		return errors.New("capture.warmup_ms must not be negative")
	}

	if cfg.Vision.BaseURL == "" {
		return errors.New("vision.base_url must not be empty")
	}
	if cfg.Vision.Model == "" {
		return errors.New("vision.model must not be empty")
	}
	switch cfg.Vision.Detail {
	case "low", "high", "auto":
	default:
		return fmt.Errorf("vision.detail: %q not supported (want low|high|auto)", cfg.Vision.Detail)
	}
	if cfg.Vision.Timeout <= 0 {
		return errors.New("vision.timeout must be positive")
	}
	if cfg.Vision.MaxRetries < 0 {
		return errors.New("vision.max_retries must not be negative")
	}
	if cfg.Vision.RetryBackoff < 0 {
		return errors.New("vision.retry_backoff must not be negative")
	}
	if cfg.Vision.MaxTokens <= 0 {
		return errors.New("vision.max_tokens must be positive")
	}

	if cfg.Server.MaxConcurrentJobs < 1 {
		return errors.New("server.max_concurrent_jobs must be >= 1")
	}
	if cfg.Server.ReadTimeout <= 0 || cfg.Server.WriteTimeout <= 0 {
		return errors.New("server.read_timeout/write_timeout must be positive")
	}
	if cfg.Server.WriteTimeout <= cfg.Vision.Timeout {
		return fmt.Errorf("server.write_timeout (%s) must exceed vision.timeout (%s)", cfg.Server.WriteTimeout, cfg.Vision.Timeout)
	}
	for _, cidr := range cfg.Server.AllowedIPs {
		if !validCIDR(cidr) {
			return fmt.Errorf("server.allowed_ips: %q is not a valid IP or CIDR", cidr)
		}
	}

	if cfg.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return fmt.Errorf("listen %q: %w", cfg.Listen, err)
	}
	return validateLogFields(cfg.Logging)
}

func validCIDR(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

// validateLogFields reuses logx level/format rules without an import cycle:
// config stays dependency-free, so the checks live here and logx re-parses.
func validateLogFields(l Logging) error {
	switch l.Format {
	case "", "text", "json":
	default:
		return fmt.Errorf("logging.format: %q not supported (want text|json)", l.Format)
	}
	switch l.Level {
	case "", "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("logging.level: %q not supported (want debug|info|warn|error)", l.Level)
	}
	return nil
}
