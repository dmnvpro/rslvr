// Package config loads rslvr configuration with the precedence chain:
// flags > environment > .env file > YAML > built-in defaults.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// DefaultConfigPath is used when --config is not passed explicitly.
const DefaultConfigPath = "./config.yaml"

// EnvFile is loaded from the working directory at startup when present.
const EnvFile = ".env"

// Config is the fully resolved runtime configuration.
type Config struct {
	Listen  string        `yaml:"listen"`
	Timeout time.Duration `yaml:"timeout"` // overall deadline of one grab+analyze cycle
	NoCache bool          `yaml:"no_cache"`

	Capture Capture `yaml:"capture"`
	Vision  Vision  `yaml:"vision"`
	Server  Server  `yaml:"server"`
	Logging Logging `yaml:"logging"`

	// CLI-only mode switches; never come from YAML.
	ListDevices bool `yaml:"-"`
	Once        bool `yaml:"-"`
	Serve       bool `yaml:"-"`
	OpenBrowser bool `yaml:"-"`
}

// Capture configures frame grabbing.
type Capture struct {
	Backend      string        `yaml:"backend"` // only "ffmpeg" is implemented
	FFmpegPath   string        `yaml:"ffmpeg_path"`
	Device       string        `yaml:"device"` // "" = first available; id, index or name
	OutputFormat string        `yaml:"output_format"`
	JPEGQuality  int           `yaml:"jpeg_quality"` // 1..100
	SaveDir      string        `yaml:"save_dir"`     // "" = do not save frames
	Warmup       time.Duration `yaml:"warmup_ms"`    // pause between device open and frame grab
}

// Vision configures the OpenAI-compatible analysis endpoint.
type Vision struct {
	BaseURL      string        `yaml:"base_url"`
	APIKeyEnv    string        `yaml:"api_key_env"`
	APIKey       string        `yaml:"api_key"` // never logged; prefer ENV / .env
	Model        string        `yaml:"model"`
	Timeout      time.Duration `yaml:"timeout"`
	MaxRetries   int           `yaml:"max_retries"`
	RetryBackoff time.Duration `yaml:"retry_backoff"`
	MaxTokens    int           `yaml:"max_tokens"`
	Temperature  float64       `yaml:"temperature"`
	Detail       string        `yaml:"detail"` // low | high | auto
	SystemPrompt string        `yaml:"system_prompt"`
	UserPrompt   string        `yaml:"user_prompt"`
}

// Server configures the local HTTP server and job admission.
type Server struct {
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	MaxConcurrentJobs int           `yaml:"max_concurrent_jobs"`
	AllowedIPs        []string      `yaml:"allowed_ips"` // CIDR whitelist; empty = allow all
	AuthToken         string        `yaml:"auth_token"`  // "" = no auth
}

// Logging configures slog output.
type Logging struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"` // text | json
}

// Result of Load: the resolved config plus non-fatal warnings.
type Result struct {
	Config   Config
	Warnings []string
}

// ErrVersionRequested is returned by Load when --version is present.
var ErrVersionRequested = errors.New("version requested")

// Injectable for tests; production values load ".env" via godotenv.
var (
	dotenvFile = EnvFile
	dotenvLoad = godotenv.Load
)

// Load resolves configuration from argv, environment and YAML files.
// lookup is injectable for tests; pass nil to use os.LookupEnv.
func Load(argv []string, lookup func(string) (string, bool)) (Result, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	res := Result{Config: Defaults()}

	// .env is merged only for variables not already present in the real
	// environment, so real ENV wins over .env by construction.
	_ = dotenvLoad(dotenvFile)

	flags, version, err := parseFlags(argv)
	if err != nil {
		return res, err
	}
	if version {
		return res, ErrVersionRequested
	}

	warnings, err := applyYAML(&res.Config, flags.configPath, flags.configExplicit)
	if err != nil {
		return res, err
	}
	res.Warnings = append(res.Warnings, warnings...)

	res.Warnings = append(res.Warnings, applyEnv(&res.Config, lookup)...)

	applyFlags(&res.Config, flags)
	if err := Validate(&res.Config); err != nil {
		return res, err
	}
	return res, nil
}

// Defaults returns the built-in configuration used when nothing overrides it.
func Defaults() Config {
	return Config{
		Listen:  "127.0.0.1:8080",
		Timeout: 30 * time.Second,
		Capture: Capture{
			Backend:      "ffmpeg",
			FFmpegPath:   "ffmpeg",
			Device:       "",
			OutputFormat: "jpeg",
			JPEGQuality:  85,
			SaveDir:      "./shots",
			Warmup:       700 * time.Millisecond,
		},
		Vision: Vision{
			BaseURL:      "https://openrouter.ai/api/v1",
			APIKeyEnv:    "OPENAI_API_KEY",
			Model:        "openai/gpt-5-image-mini",
			Timeout:      30 * time.Second,
			MaxRetries:   3,
			RetryBackoff: 800 * time.Millisecond,
			MaxTokens:    500,
			Temperature:  0.0,
			Detail:       "high",
			SystemPrompt: "You are an assistant that reads text on images.",
			UserPrompt: `The image shows a quiz question. Read the question and the answer options.
Reply on a single line: first the letter(s) of the correct option, then a vertical
bar "|", then the answer text. If the image is unreadable, reply exactly "UNREADABLE".`,
		},
		Server: Server{
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      60 * time.Second,
			MaxConcurrentJobs: 1,
		},
		Logging: Logging{Level: "info", Format: "text"},
	}
}

// applyYAML merges the config file over defaults and returns warnings.
// A missing default-path file is fine; a missing explicitly passed path is an error.
func applyYAML(cfg *Config, path string, explicit bool) ([]string, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		// fall through to decode
	case errors.Is(err, os.ErrNotExist):
		if explicit {
			return nil, fmt.Errorf("config file not found: %s", path)
		}
		return []string{fmt.Sprintf("no config file at %s, using built-in defaults", path)}, nil
	default:
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// Decode over the already-defaulted struct so omitted keys keep defaults.
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	var warns []string
	if cfg.Vision.APIKey != "" {
		warns = append(warns, "api_key is set in the config file; prefer OPENAI_API_KEY env or .env")
	}
	return warns, nil
}

// applyEnv applies environment overrides on top of YAML values.
func applyEnv(cfg *Config, lookup func(string) (string, bool)) []string {
	name := cfg.Vision.APIKeyEnv
	if name == "" {
		name = "OPENAI_API_KEY"
	}
	if v, ok := lookup(name); ok && v != "" {
		cfg.Vision.APIKey = v
	}
	return nil
}
