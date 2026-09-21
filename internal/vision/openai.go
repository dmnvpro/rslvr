package vision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"rslvr/internal/httpx"
	"rslvr/internal/logx"
)

// Options configures the OpenAI-compatible client.
type Options struct {
	BaseURL      string // e.g. https://openrouter.ai/api/v1
	APIKey       string // never logged; use logx.MaskKey for diagnostics
	Model        string
	MaxTokens    int
	Temperature  float64
	Detail       string // low | high | auto
	SystemPrompt string
	UserPrompt   string
	MaxRetries   int
	RetryBackoff time.Duration
	// Client is the HTTP client; nil means a default with no global timeout
	// (deadlines come from the context).
	Client *http.Client
	// Log receives debug diagnostics; may be nil.
	Log *slog.Logger
}

// OpenAI is the chat-completions Analyzer implementation.
type OpenAI struct {
	o     Options
	hc    *http.Client
	retry httpx.RetryConfig
}

// New builds the client.
func New(o Options) *OpenAI {
	hc := o.Client
	if hc == nil {
		hc = &http.Client{} // no global timeout: ctx drives deadlines
	}
	return &OpenAI{
		o:  o,
		hc: hc,
		retry: httpx.RetryConfig{
			MaxRetries: o.MaxRetries,
			BaseDelay:  o.RetryBackoff,
			MaxDelay:   10 * time.Second,
		},
	}
}

// HashImage returns the frame SHA-256 used for caching and identification.
func HashImage(img []byte) string {
	sum := sha256.Sum256(img)
	return hex.EncodeToString(sum[:])
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // plain string (system) or []part (user)
}

type part struct {
	Type     string    `json:"type"` // "text" | "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *imageRef `json:"image_url,omitempty"`
}

type imageRef struct {
	URL    string `json:"url"` // data:<mime>;base64,...
	Detail string `json:"detail,omitempty"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage      `json:"usage"`
	Error *errorBody `json:"error"`
}

type errorBody struct {
	Message  string          `json:"message"`
	Type     string          `json:"type"`
	Code     errorCode       `json:"code"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// errorCode tolerates both OpenAI-style string codes ("invalid_api_key") and
// OpenRouter-style numeric ones (400) in the same field; the raw JSON token
// is kept as text.
type errorCode string

func (c *errorCode) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*c = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = errorCode(s)
		return nil
	}
	*c = errorCode(b)
	return nil
}

// Analyze posts one frame to /chat/completions and extracts the answer.
func (c *OpenAI) Analyze(ctx context.Context, img []byte, mime string) (Result, error) {
	if c.o.APIKey == "" {
		return Result{}, ErrNoAPIKey
	}

	payload, err := json.Marshal(c.buildRequest(mime, img))
	if err != nil {
		return Result{}, fmt.Errorf("encode request: %w", err)
	}

	if c.o.Log != nil {
		c.o.Log.Debug("vision request",
			slog.String("model", c.o.Model),
			slog.String("key", logx.MaskKey(c.o.APIKey)),
			slog.String("image_sha256", HashImage(img)),
			slog.Int("image_bytes", len(img)),
		)
	}

	start := time.Now()
	resp, err := c.retry.Do(ctx, func(ctx context.Context) (*http.Response, error) {
		return c.post(ctx, payload)
	})
	latency := time.Since(start)

	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return Result{}, fmt.Errorf("read vision response: %w", err)
	}

	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return Result{}, fmt.Errorf("%w: status %d, malformed body (%v): %s",
			ErrUpstream, resp.StatusCode, err, truncate(body, 300))
	}
	if cr.Error != nil {
		return Result{}, c.apiError(resp.StatusCode, cr.Error)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Result{}, fmt.Errorf("%w: status %d", ErrAuth, resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("%w: unexpected status %d: %s", ErrUpstream, resp.StatusCode, truncate(body, 300))
	}
	if len(cr.Choices) == 0 {
		return Result{}, fmt.Errorf("%w: response has no choices", ErrUpstream)
	}

	raw := cr.Choices[0].Message.Content
	answer, err := Normalize(raw)
	if err != nil {
		return Result{Raw: raw}, err
	}

	model := cr.Model
	if model == "" {
		model = c.o.Model
	}
	res := Result{
		Answer:    answer,
		Raw:       raw,
		Model:     model,
		LatencyMS: latency.Milliseconds(),
		Usage:     cr.Usage,
		RequestID: cr.ID,
		ImageHash: HashImage(img),
	}
	if c.o.Log != nil {
		c.o.Log.Debug("vision response",
			slog.String("request_id", res.RequestID),
			slog.Any("usage", res.Usage),
			slog.String("raw", raw),
			slog.Int64("latency_ms", res.LatencyMS),
		)
	}
	return res, nil
}

// Preflight performs the cheap key check (GET /models) used at startup.
// ErrAuth means the key is invalid; any other error is connectivity-level.
func (c *OpenAI) Preflight(ctx context.Context) error {
	if c.o.APIKey == "" {
		return ErrNoAPIKey
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := strings.TrimRight(c.o.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.o.APIKey)

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("key check: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))

	switch {
	case resp.StatusCode/100 == 2:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: key rejected with status %d", ErrAuth, resp.StatusCode)
	default:
		return fmt.Errorf("key check: unexpected status %d", resp.StatusCode)
	}
}

func (c *OpenAI) buildRequest(mime string, img []byte) chatRequest {
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img)
	return chatRequest{
		Model: c.o.Model,
		Messages: []message{
			{Role: "system", Content: c.o.SystemPrompt},
			{Role: "user", Content: []part{
				{Type: "text", Text: c.o.UserPrompt},
				{Type: "image_url", ImageURL: &imageRef{URL: dataURL, Detail: c.o.Detail}},
			}},
		},
		MaxTokens:   c.o.MaxTokens,
		Temperature: c.o.Temperature,
	}
}

func (c *OpenAI) post(ctx context.Context, payload []byte) (*http.Response, error) {
	url := strings.TrimRight(c.o.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.o.APIKey)
	req.Header.Set("X-Title", "rslvr")
	return c.hc.Do(req)
}

func (c *OpenAI) apiError(status int, e *errorBody) error {
	msg := e.Message
	if e.Code != "" {
		msg = fmt.Sprintf("%s (code %s)", msg, e.Code)
	}
	// OpenRouter reports provider-side failures generically; the details sit
	// in error.metadata (e.g. metadata.raw), so keep a snippet of it.
	if len(e.Metadata) > 0 {
		msg += ": " + truncate(e.Metadata, 300)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden || e.Code == "invalid_api_key" {
		return fmt.Errorf("%w: %s", ErrAuth, msg)
	}
	return fmt.Errorf("%w: status %d: %s", ErrUpstream, status, msg)
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
