package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mapLookup returns a lookup func over m.
func mapLookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// unsetenv removes name from the process env for the duration of the test.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	orig, had := os.LookupEnv(name)
	os.Unsetenv(name)
	t.Cleanup(func() {
		if had {
			os.Setenv(name, orig)
		} else {
			os.Unsetenv(name)
		}
	})
}

func TestLoad_MissingDefaultConfig_Warning(t *testing.T) {
	unsetenv(t, "OPENAI_API_KEY")
	t.Chdir(t.TempDir()) // no config.yaml here
	res, err := Load(nil, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "; "), "no config file") {
		t.Errorf("warnings = %v, want a 'no config file' warning", res.Warnings)
	}
	if res.Config.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q, want default", res.Config.Listen)
	}
	if res.Config.Vision.Model != "openai/gpt-5-image-mini" {
		t.Errorf("Model = %q, want default", res.Config.Vision.Model)
	}
	if res.Config.Capture.Warmup != 700*time.Millisecond {
		t.Errorf("Warmup = %s, want default 700ms", res.Config.Capture.Warmup)
	}
	if !res.Config.Serve || res.Config.Once {
		t.Errorf("Serve/Once = %v/%v, want true/false", res.Config.Serve, res.Config.Once)
	}
}

func TestLoad_ExplicitConfigMissing_Error(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := Load([]string{"--config", filepath.Join(t.TempDir(), "nope.yaml")}, nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want 'config file not found'", err)
	}
}

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_YAMLOverridesDefaults_KeepsOmitted(t *testing.T) {
	unsetenv(t, "OPENAI_API_KEY")
	t.Chdir(t.TempDir())
	writeConfig(t, ".", `
listen: "0.0.0.0:9999"
timeout: 45s
capture:
  device: "1"
  warmup_ms: 1500ms
  output_format: png
vision:
  model: test/model-x
  detail: low
server:
  max_concurrent_jobs: 2
`)
	res, err := Load(nil, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := res.Config
	if c.Listen != "0.0.0.0:9999" || c.Timeout != 45*time.Second {
		t.Errorf("Listen/Timeout = %q/%s", c.Listen, c.Timeout)
	}
	if c.Capture.Device != "1" || c.Capture.Warmup != 1500*time.Millisecond || c.Capture.OutputFormat != "png" {
		t.Errorf("capture = %+v", c.Capture)
	}
	if c.Capture.JPEGQuality != 85 || c.Capture.SaveDir != "./shots" {
		t.Errorf("omitted capture keys must keep defaults, got %+v", c.Capture)
	}
	if c.Vision.Model != "test/model-x" || c.Vision.Detail != "low" {
		t.Errorf("vision = %+v", c.Vision)
	}
	if c.Vision.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("omitted base_url must keep default, got %q", c.Vision.BaseURL)
	}
	if c.Server.MaxConcurrentJobs != 2 {
		t.Errorf("MaxConcurrentJobs = %d", c.Server.MaxConcurrentJobs)
	}
}

func TestLoad_FlagOverridesYAML(t *testing.T) {
	t.Chdir(t.TempDir())
	writeConfig(t, ".", `listen: "0.0.0.0:9999"
vision:
  model: yaml/model
`)
	res, err := Load([]string{"--listen", "127.0.0.1:7000", "--model", "flag/model", "--timeout", "12s"}, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Config.Listen != "127.0.0.1:7000" {
		t.Errorf("Listen = %q, want flag value", res.Config.Listen)
	}
	if res.Config.Vision.Model != "flag/model" {
		t.Errorf("Model = %q, want flag value", res.Config.Vision.Model)
	}
	if res.Config.Timeout != 12*time.Second {
		t.Errorf("Timeout = %s, want flag value", res.Config.Timeout)
	}
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	t.Chdir(t.TempDir())
	writeConfig(t, ".", `vision:
  api_key: "yaml-secret"
`)
	res, err := Load(nil, mapLookup(map[string]string{"OPENAI_API_KEY": "env-secret"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Config.Vision.APIKey != "env-secret" {
		t.Errorf("APIKey = %q, want env value", res.Config.Vision.APIKey)
	}
}

func TestLoad_FlagOverridesEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	res, err := Load([]string{"--api-key", "flag-secret"},
		mapLookup(map[string]string{"OPENAI_API_KEY": "env-secret"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Config.Vision.APIKey != "flag-secret" {
		t.Errorf("APIKey = %q, want flag value", res.Config.Vision.APIKey)
	}
}

func TestLoad_CustomAPIKeyEnvName(t *testing.T) {
	t.Chdir(t.TempDir())
	writeConfig(t, ".", `vision:
  api_key_env: "MY_KEY"
`)
	res, err := Load(nil, mapLookup(map[string]string{"MY_KEY": "custom"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Config.Vision.APIKey != "custom" {
		t.Errorf("APIKey = %q, want value of MY_KEY", res.Config.Vision.APIKey)
	}
}

func TestLoad_DotEnvDoesNotOverrideRealEnv(t *testing.T) {
	unsetenv(t, "OPENAI_API_KEY")
	t.Setenv("OPENAI_API_KEY", "real-env") // real env present...
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("OPENAI_API_KEY=dotenv-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(nil, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Config.Vision.APIKey != "real-env" {
		t.Errorf("APIKey = %q, want real env to win over .env", res.Config.Vision.APIKey)
	}
}

func TestLoad_DotEnvUsedWhenNoRealEnv(t *testing.T) {
	unsetenv(t, "OPENAI_API_KEY")
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("OPENAI_API_KEY=dotenv-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(nil, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Config.Vision.APIKey != "dotenv-value" {
		t.Errorf("APIKey = %q, want value from .env", res.Config.Vision.APIKey)
	}
}

func TestLoad_FlagEmptyStringDisablesSaveDir(t *testing.T) {
	t.Chdir(t.TempDir())
	res, err := Load([]string{"--output-dir", ""}, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Config.Capture.SaveDir != "" {
		t.Errorf("SaveDir = %q, want explicit empty", res.Config.Capture.SaveDir)
	}
}

func TestLoad_VersionRequested(t *testing.T) {
	_, err := Load([]string{"--version"}, nil)
	if !errors.Is(err, ErrVersionRequested) {
		t.Fatalf("err = %v, want ErrVersionRequested", err)
	}
}

func TestLoad_OnceSwitchesMode(t *testing.T) {
	t.Chdir(t.TempDir())
	res, err := Load([]string{"--once"}, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !res.Config.Once || !res.Config.Serve {
		t.Errorf("Once/Serve = %v/%v; Once must be true, Serve keeps its default", res.Config.Once, res.Config.Serve)
	}
}

func TestLoad_APIKeyInYAMLWarns(t *testing.T) {
	unsetenv(t, "OPENAI_API_KEY")
	t.Chdir(t.TempDir())
	writeConfig(t, ".", `vision:
  api_key: "file-secret"
`)
	res, err := Load(nil, mapLookup(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "api_key") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want api_key-in-file warning", res.Warnings)
	}
}

func TestScanConfigPath(t *testing.T) {
	cases := []struct {
		argv     []string
		want     string
		explicit bool
		wantErr  bool
	}{
		{nil, DefaultConfigPath, false, false},
		{[]string{"--config", "a.yaml"}, "a.yaml", true, false},
		{[]string{"-config", "b.yaml"}, "b.yaml", true, false},
		{[]string{"--config=c.yaml"}, "c.yaml", true, false},
		{[]string{"--device", "0", "--config", "d.yaml"}, "d.yaml", true, false},
		{[]string{"--config"}, "", false, true},
	}
	for _, c := range cases {
		got, explicit, err := scanConfigPath(c.argv)
		if (err != nil) != c.wantErr {
			t.Errorf("scanConfigPath(%v) err = %v, wantErr %v", c.argv, err, c.wantErr)
			continue
		}
		if got != c.want || explicit != c.explicit {
			t.Errorf("scanConfigPath(%v) = %q,%v; want %q,%v", c.argv, got, explicit, c.want, c.explicit)
		}
	}
}

func TestParseFlags_HelpGoesToErrHelp(t *testing.T) {
	_, _, err := parseFlags([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
}

func TestValidate(t *testing.T) {
	base := Defaults()

	good := base
	if err := Validate(&good); err != nil {
		t.Fatalf("defaults must validate, got %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"gocv", func(c *Config) { c.Capture.Backend = "gocv" }, "not implemented"},
		{"empty backend", func(c *Config) { c.Capture.Backend = "" }, "empty"},
		{"unknown backend", func(c *Config) { c.Capture.Backend = "avfoundation" }, "unknown backend"},
		{"bad format", func(c *Config) { c.Capture.OutputFormat = "bmp" }, "output_format"},
		{"bad quality", func(c *Config) { c.Capture.JPEGQuality = 0 }, "jpeg_quality"},
		{"negative warmup", func(c *Config) { c.Capture.Warmup = -1 }, "warmup_ms"},
		{"empty base_url", func(c *Config) { c.Vision.BaseURL = "" }, "base_url"},
		{"empty model", func(c *Config) { c.Vision.Model = "" }, "model"},
		{"bad detail", func(c *Config) { c.Vision.Detail = "ultra" }, "detail"},
		{"zero vision timeout", func(c *Config) { c.Vision.Timeout = 0 }, "vision.timeout"},
		{"bad temperature", func(c *Config) { c.Vision.Temperature = 5 }, "temperature"},
		{"zero jobs", func(c *Config) { c.Server.MaxConcurrentJobs = 0 }, "max_concurrent_jobs"},
		{"write timeout <= vision", func(c *Config) { c.Server.WriteTimeout = c.Vision.Timeout }, "write_timeout"},
		{"bad cidr", func(c *Config) { c.Server.AllowedIPs = []string{"10.0.0.0/67"} }, "allowed_ips"},
		{"good cidr ip form", func(c *Config) { c.Server.AllowedIPs = []string{"192.168.1.5"} }, ""},
		{"zero timeout", func(c *Config) { c.Timeout = 0 }, "timeout"},
		{"bad listen", func(c *Config) { c.Listen = "nope" }, "listen"},
		{"bad log level", func(c *Config) { c.Logging.Level = "loud" }, "logging.level"},
		{"bad log format", func(c *Config) { c.Logging.Format = "xml" }, "logging.format"},
	}
	for _, c := range cases {
		cfg := base
		c.mutate(&cfg)
		err := Validate(&cfg)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want contains %q", c.name, err, c.wantErr)
		}
	}
}
