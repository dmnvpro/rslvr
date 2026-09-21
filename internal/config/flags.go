package config

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// flagSet holds raw CLI flag values and which of them were explicitly passed.
type flagSet struct {
	configPath     string
	configExplicit bool

	listen      string
	device      string
	listDevices bool
	once        bool
	serve       bool
	prompt      string
	model       string
	apiKey      string
	outputDir   string
	timeout     time.Duration
	logLevel    string
	openBrowser bool
	noCache     bool
	version     bool

	visited map[string]bool
}

// scanConfigPath pre-parses --config from argv. It is a manual scan because a
// first-pass FlagSet would fail on flags it does not know (--listen, ...).
func scanConfigPath(argv []string) (path string, explicit bool, err error) {
	path = DefaultConfigPath
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		name, value, hasValue := strings.Cut(a, "=")
		if name != "--config" && name != "-config" {
			continue
		}
		if !hasValue {
			if i+1 >= len(argv) {
				return "", false, fmt.Errorf("flag %s requires a value", name)
			}
			value = argv[i+1]
			i++
		}
		path, explicit = value, true
	}
	return path, explicit, nil
}

// parseFlags parses the full CLI flag set (pass two).
func parseFlags(argv []string) (fs *flagSet, version bool, err error) {
	fs = &flagSet{visited: map[string]bool{}}
	fs.configPath, fs.configExplicit, err = scanConfigPath(argv)
	if err != nil {
		return nil, false, err
	}

	f := flag.NewFlagSet("rslvr", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	registerFlags(f, fs)

	if err := f.Parse(argv); err != nil {
		return nil, false, err
	}
	f.Visit(func(fl *flag.Flag) { fs.visited[fl.Name] = true })
	return fs, fs.version, nil
}

// registerFlags binds all CLI flags to fs's storage.
func registerFlags(f *flag.FlagSet, fs *flagSet) {
	// --config is consumed by scanConfigPath before this parse; registered so
	// the parser accepts it and it shows up in -h usage.
	var cfgPath string
	f.StringVar(&cfgPath, "config", DefaultConfigPath, "path to YAML config (default ./config.yaml)")
	f.StringVar(&fs.listen, "listen", "", "listen address of the web server (e.g. 127.0.0.1:8080)")
	f.StringVar(&fs.device, "device", "", "video device id, index or name; comma list filters the web UI dropdown")
	f.BoolVar(&fs.listDevices, "list-devices", false, "list available video devices and exit")
	f.BoolVar(&fs.once, "once", false, "grab one frame, print the answer to stdout and exit")
	f.BoolVar(&fs.serve, "serve", true, "run the local web server (default, unless --once)")
	f.StringVar(&fs.prompt, "prompt", "", "override the user prompt for the model")
	f.StringVar(&fs.model, "model", "", "vision model id (overrides config)")
	f.StringVar(&fs.apiKey, "api-key", "", "API key (overrides ENV and config; prefer .env)")
	f.StringVar(&fs.outputDir, "output-dir", "", "directory to save frame copies (\"\" disables saving)")
	f.DurationVar(&fs.timeout, "timeout", 0, "overall deadline of one grab+analyze cycle")
	f.StringVar(&fs.logLevel, "log-level", "", "log level: debug|info|warn|error")
	f.BoolVar(&fs.openBrowser, "open-browser", false, "open the web page in a browser on start")
	f.BoolVar(&fs.noCache, "no-cache", false, "disable the 30s frame-hash answer cache")
	f.BoolVar(&fs.version, "version", false, "print version and exit")
}

// Usage renders the CLI help text.
func Usage() string {
	var buf strings.Builder
	buf.WriteString("rslvr — grab a frame from a video device and resolve the quiz answer via a vision model\n\nFlags:\n")
	f := flag.NewFlagSet("rslvr", flag.ContinueOnError)
	f.SetOutput(&buf)
	registerFlags(f, &flagSet{visited: map[string]bool{}})
	f.Usage = func() { f.PrintDefaults() }
	f.Usage()
	return buf.String() + `
Exit codes: 0 success; 1 general error; 2 no video devices; 3 OpenAI/API error; 4 timeout.
`
}

// applyFlags overrides config values with explicitly passed flags.
func applyFlags(cfg *Config, f *flagSet) {
	// Mode flags are applied unconditionally: serve defaults to true and
	// --once switches the mode in app code.
	cfg.Serve = f.serve
	cfg.Once = f.once
	if f.visited["listen"] {
		cfg.Listen = f.listen
	}
	if f.visited["device"] {
		cfg.Capture.Device = f.device
	}
	if f.visited["list-devices"] {
		cfg.ListDevices = f.listDevices
	}
	if f.visited["prompt"] {
		cfg.Vision.UserPrompt = f.prompt
	}
	if f.visited["model"] {
		cfg.Vision.Model = f.model
	}
	if f.visited["api-key"] {
		cfg.Vision.APIKey = f.apiKey
	}
	if f.visited["output-dir"] {
		cfg.Capture.SaveDir = f.outputDir
	}
	if f.visited["timeout"] {
		cfg.Timeout = f.timeout
	}
	if f.visited["log-level"] {
		cfg.Logging.Level = f.logLevel
	}
	if f.visited["open-browser"] {
		cfg.OpenBrowser = f.openBrowser
	}
	if f.visited["no-cache"] {
		cfg.NoCache = f.noCache
	}
}
