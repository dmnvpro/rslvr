// Command rslvr grabs a frame from a video device, asks a vision model to
// answer the quiz question on it, and serves a local web UI.
package main

import (
	"os"

	"rslvr/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:], os.Stdout, os.Stderr))
}
