// Package web embeds the single-file UI served by internal/server.
package web

import _ "embed"

// Index is web/index.html, compiled into the binary.
//
//go:embed index.html
var Index []byte
