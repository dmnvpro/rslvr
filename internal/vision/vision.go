// Package vision asks an OpenAI-compatible chat-completions endpoint to read
// a captured frame and answer the quiz question on it.
package vision

import (
	"context"
	"errors"
)

// Usage mirrors the token accounting returned by the API.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Frame is one image of a Frame set (see CONTEXT.md).
type Frame struct {
	Data []byte // raw JPEG/PNG bytes
	MIME string
}

// Result is the outcome of one analysis (see CONTEXT.md).
type Result struct {
	Answer      string   `json:"answer"`
	Question    string   `json:"question,omitempty"`    // recognized question text
	Options     []string `json:"options,omitempty"`     // recognized answer options
	Explanation string   `json:"explanation,omitempty"` // why the answer is correct
	Raw         string   `json:"raw"`                   // unprocessed model reply
	Model       string   `json:"model"`
	LatencyMS   int64    `json:"latency_ms"`
	Usage       Usage    `json:"usage"`
	RequestID   string   `json:"request_id"`
	ImageHash   string   `json:"image_sha256"` // identity of the whole Frame set
}

// Sentinel errors mapped to HTTP status codes by internal/server.
var (
	// ErrUnreadableImage: the model answered UNREADABLE (TechSpec §8).
	ErrUnreadableImage = errors.New("model could not read the image")
	// ErrNoAPIKey: no key configured anywhere in the precedence chain.
	ErrNoAPIKey = errors.New("no API key configured (set OPENAI_API_KEY or use .env)")
	// ErrAuth: the endpoint rejected the key (401/403).
	ErrAuth = errors.New("API authentication failed")
	// ErrUpstream: any other upstream (API/network) failure → HTTP 502.
	ErrUpstream = errors.New("vision request failed")
)

// Analyzer abstracts the answer source; tests inject fakes.
type Analyzer interface {
	Analyze(ctx context.Context, frames []Frame) (Result, error)
}
