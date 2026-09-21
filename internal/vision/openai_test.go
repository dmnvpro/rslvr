package vision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func clientOpts(url string) Options {
	return Options{
		BaseURL:      url,
		APIKey:       "sk-test-secret-key-123456",
		Model:        "test/model",
		MaxTokens:    100,
		Detail:       "high",
		SystemPrompt: "sys",
		UserPrompt:   "user prompt text",
		MaxRetries:   2,
		RetryBackoff: time.Millisecond,
	}
}

func okResponse(content string) string {
	b, _ := json.Marshal(chatResponse{
		ID:    "req-1",
		Model: "test/model-actual",
		Choices: []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{{Message: struct {
			Content string `json:"content"`
		}{Content: content}, FinishReason: "stop"}},
		Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})
	return string(b)
}

func oneFrame(data string) []Frame {
	return []Frame{{Data: []byte(data), MIME: "image/jpeg"}}
}

func TestAnalyze_RequestShapeAndSuccess(t *testing.T) {
	var gotPath string
	var gotAuth string
	var req map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okResponse("B | Paris"))
	}))
	defer srv.Close()

	c := New(clientOpts(srv.URL))
	res, err := c.Analyze(context.Background(), oneFrame("img"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if gotPath != "/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test-secret-key-123456" {
		t.Errorf("auth = %q", gotAuth)
	}
	if req["model"] != "test/model" || req["max_tokens"] != float64(100) {
		t.Errorf("request fields: %v", req)
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d", len(msgs))
	}
	user := msgs[1].(map[string]any)
	parts := user["content"].([]any)
	// prompt text, "Frame 1:" marker, image
	if len(parts) != 3 {
		t.Fatalf("user parts = %d", len(parts))
	}
	if text := parts[0].(map[string]any)["text"]; text != "user prompt text" {
		t.Errorf("prompt text part = %v", text)
	}
	if marker := parts[1].(map[string]any)["text"]; marker != "Frame 1:" {
		t.Errorf("frame marker = %v", marker)
	}
	imgPart := parts[2].(map[string]any)["image_url"].(map[string]any)
	if !strings.HasPrefix(imgPart["url"].(string), "data:image/jpeg;base64,") {
		t.Errorf("image url = %v", imgPart["url"])
	}
	if imgPart["detail"] != "high" {
		t.Errorf("detail = %v", imgPart["detail"])
	}

	if res.Answer != "B | Paris" || res.Raw != "B | Paris" {
		t.Errorf("answer/raw = %q / %q", res.Answer, res.Raw)
	}
	if res.Model != "test/model-actual" {
		t.Errorf("model = %q", res.Model)
	}
	if res.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.RequestID != "req-1" {
		t.Errorf("request_id = %q", res.RequestID)
	}
	if res.ImageHash != HashFrames(oneFrame("img")) {
		t.Errorf("image hash mismatch: %q", res.ImageHash)
	}
}

func TestAnalyze_JsonReplyCarriesQuestionAndOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, okResponse(`{"question":"Capital of France?","options":["A. London","B. Paris"],`+
			`"answer":"B","answer_text":"Paris","explanation":"Paris is the capital of France."}`))
	}))
	defer srv.Close()

	res, err := New(clientOpts(srv.URL)).Analyze(context.Background(), oneFrame("img"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Answer != "B | Paris" || res.Question != "Capital of France?" {
		t.Errorf("result = %+v", res)
	}
	if res.Explanation != "Paris is the capital of France." {
		t.Errorf("explanation = %q", res.Explanation)
	}
	if want := []string{"A. London", "B. Paris"}; len(res.Options) != len(want) || res.Options[0] != want[0] || res.Options[1] != want[1] {
		t.Errorf("options = %+v, want %+v", res.Options, want)
	}
}

// Frame sets interleave "Frame N:" markers with the images in capture order.
func TestAnalyze_FrameNumbering(t *testing.T) {
	var parts []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		msgs := req["messages"].([]any)
		parts = msgs[1].(map[string]any)["content"].([]any)
		io.WriteString(w, okResponse("A | 42"))
	}))
	defer srv.Close()

	frames := []Frame{
		{Data: []byte("first"), MIME: "image/jpeg"},
		{Data: []byte("second"), MIME: "image/png"},
	}
	if _, err := New(clientOpts(srv.URL)).Analyze(context.Background(), frames); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(parts) != 5 {
		t.Fatalf("parts = %d, want 5 (text + 2×(marker, image))", len(parts))
	}
	for i, want := range []string{"Frame 1:", "Frame 2:"} {
		got := parts[1+2*i].(map[string]any)["text"]
		if got != want {
			t.Errorf("marker %d = %v, want %q", i, got, want)
		}
		url := parts[2+2*i].(map[string]any)["image_url"].(map[string]any)["url"].(string)
		wantPrefix := "data:" + frames[i].MIME + ";base64,"
		if !strings.HasPrefix(url, wantPrefix) {
			t.Errorf("frame %d url prefix = %q, want %q", i, url[:20], wantPrefix)
		}
	}
}

func TestAnalyze_UnreadableCarriesRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, okResponse("UNREADABLE"))
	}))
	defer srv.Close()

	res, err := New(clientOpts(srv.URL)).Analyze(context.Background(), oneFrame("img"))
	if !errors.Is(err, ErrUnreadableImage) {
		t.Fatalf("err = %v, want ErrUnreadableImage", err)
	}
	if res.Raw != "UNREADABLE" {
		t.Errorf("raw = %q, want preserved", res.Raw)
	}
}

func TestAnalyze_Auth401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid key","code":"invalid_api_key"}}`)
	}))
	defer srv.Close()

	_, err := New(clientOpts(srv.URL)).Analyze(context.Background(), oneFrame("img"))
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

// OpenRouter error bodies carry a numeric code and provider details in
// metadata; both must surface instead of failing to unmarshal.
func TestAnalyze_UpstreamErrorNumericCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":400,"message":"Provider returned error","metadata":{"raw":"context length exceeded","provider_name":"openai"}}}`)
	}))
	defer srv.Close()

	_, err := New(clientOpts(srv.URL)).Analyze(context.Background(), oneFrame("img"))
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	msg := err.Error()
	for _, want := range []string{"status 400", "Provider returned error", "code 400", "context length exceeded"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must contain %q", msg, want)
		}
	}
}

func TestAnalyze_NumericCode401IsAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":401,"message":"No auth credentials found"}}`)
	}))
	defer srv.Close()

	_, err := New(clientOpts(srv.URL)).Analyze(context.Background(), oneFrame("img"))
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestAnalyze_GarbageBodyIncludesSnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `<html>proxy exploded</html>`)
	}))
	defer srv.Close()

	_, err := New(clientOpts(srv.URL)).Analyze(context.Background(), oneFrame("img"))
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	if !strings.Contains(err.Error(), "proxy exploded") {
		t.Errorf("error %q must include the body snippet", err)
	}
}

func TestErrorCode_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		in   string
		want errorCode
	}{
		{`"invalid_api_key"`, "invalid_api_key"},
		{`400`, "400"},
		{`null`, ""},
	}
	for _, c := range cases {
		var got struct {
			Code errorCode `json:"code"`
		}
		if err := json.Unmarshal([]byte(`{"code":`+c.in+`}`), &got); err != nil {
			t.Errorf("unmarshal %s: %v", c.in, err)
			continue
		}
		if got.Code != c.want {
			t.Errorf("code for %s = %q, want %q", c.in, got.Code, c.want)
		}
	}
}

func TestAnalyze_NoAPIKey(t *testing.T) {
	o := clientOpts("http://unused")
	o.APIKey = ""
	_, err := New(o).Analyze(context.Background(), oneFrame("img"))
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
}

func TestAnalyze_RetriesOn429ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		io.WriteString(w, okResponse("A | 42"))
	}))
	defer srv.Close()

	o := clientOpts(srv.URL)
	o.RetryBackoff = time.Millisecond
	res, err := New(o).Analyze(context.Background(), oneFrame("img"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
	if res.Answer != "A | 42" {
		t.Errorf("answer = %q", res.Answer)
	}
}

func TestAnalyze_NoRetryOn401(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	_, err := New(clientOpts(srv.URL)).Analyze(context.Background(), oneFrame("img"))
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on auth)", calls.Load())
	}
}

func TestAnalyze_DoesNotLogAPIKey(t *testing.T) {
	var logLines strings.Builder
	o := clientOpts("http://127.0.0.1:1") // connection refused
	o.Log = newTestLogger(&logLines)
	_, _ = New(o).Analyze(context.Background(), oneFrame("img"))

	out := logLines.String()
	if strings.Contains(out, "sk-test-secret-key-123456") {
		t.Errorf("API key leaked into logs: %s", out)
	}
}

func TestPreflight(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/models" {
				t.Errorf("path = %q", r.URL.Path)
			}
			if r.Header.Get("Authorization") == "" {
				t.Error("missing auth header")
			}
			io.WriteString(w, `{"data":[]}`)
		}))
		defer srv.Close()
		if err := New(clientOpts(srv.URL)).Preflight(context.Background()); err != nil {
			t.Fatalf("Preflight: %v", err)
		}
	})
	t.Run("401 is ErrAuth", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		err := New(clientOpts(srv.URL)).Preflight(context.Background())
		if !errors.Is(err, ErrAuth) {
			t.Fatalf("err = %v, want ErrAuth", err)
		}
	})
	t.Run("no key", func(t *testing.T) {
		o := clientOpts("http://unused")
		o.APIKey = ""
		if err := New(o).Preflight(context.Background()); !errors.Is(err, ErrNoAPIKey) {
			t.Fatalf("err = %v, want ErrNoAPIKey", err)
		}
	})
	t.Run("network error is not ErrAuth", func(t *testing.T) {
		err := New(clientOpts("http://127.0.0.1:1")).Preflight(context.Background())
		if err == nil || errors.Is(err, ErrAuth) {
			t.Fatalf("err = %v, want connectivity error", err)
		}
	})
}
