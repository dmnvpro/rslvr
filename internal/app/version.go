package app

import "runtime"

// Version is overridden at build time via -ldflags "-X rslvr/internal/app.Version=...".
var Version = "dev"

// goos is overridable in tests.
var goos = runtime.GOOS
