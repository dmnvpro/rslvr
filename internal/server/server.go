// Package server exposes the local HTTP API and the embedded single-page UI.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rslvr/internal/capture"
	"rslvr/internal/config"
	"rslvr/internal/jobs"
	"rslvr/internal/vision"
	"rslvr/web"
)

// Deps wires the server to its collaborators.
type Deps struct {
	Cfg      config.Config
	Runner   *jobs.Runner
	Capturer capture.Capturer
	Log      *slog.Logger
}

// Server holds the HTTP-facing state.
type Server struct {
	cfg  config.Config
	pipe *jobs.Runner
	cap  capture.Capturer
	log  *slog.Logger
}

// New builds the server.
func New(d Deps) *Server {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: d.Cfg, pipe: d.Runner, cap: d.Capturer, log: log}
}

// Handler returns the fully chained HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/devices", s.devices)
	mux.HandleFunc("POST /api/frame", s.frame)
	mux.HandleFunc("POST /api/screenshot", s.screenshot)
	mux.HandleFunc("POST /api/history", s.saveHistory)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/health", s.health)

	return Chain(mux,
		WithRecover(s.log),
		WithLogging(s.log),
		WithCIDR(parseCIDRs(s.cfg.Server.AllowedIPs)),
		WithToken(s.cfg.Server.AuthToken),
	)
}

// index serves the embedded single-file UI.
func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(web.Index)
}

// status reports the job runner state.
func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.pipe.Status())
}

// health is the liveness probe.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// devices lists video devices and the preselected one.
func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.cap.List(r.Context())
	if err != nil {
		// An empty list still renders a usable page; log and continue.
		s.log.Warn("device list failed", slog.String("err", err.Error()))
		devs = []capture.Device{}
	}
	selected := ""
	if len(devs) > 0 {
		if dev, err := capture.Resolve(s.cfg.Capture.Device, devs); err == nil {
			selected = dev.ID
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"devices":  devs,
		"selected": selected,
	})
}

// maxScreenshotBody bounds the multi-frame upload (several PNG frames in
// base64 grow fast; 64 MB is far beyond any sane set).
const maxScreenshotBody = 64 << 20

// frameBody is the POST /api/frame payload; all fields optional.
type frameBody struct {
	Device string `json:"device"`
	Save   *bool  `json:"save"`
}

// frame captures one frame without analyzing it. The web UI accumulates the
// frames of a question client-side and sends them back with /api/screenshot.
func (s *Server) frame(w http.ResponseWriter, r *http.Request) {
	var body frameBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	save := s.cfg.Capture.SaveDir != ""
	if body.Save != nil {
		save = *body.Save
	}

	g, err := s.pipe.Grab(r.Context(), body.Device, save)
	if err != nil {
		status, code := statusFor(err)
		s.log.Info("frame grab failed",
			slog.String("code", code), slog.String("err", err.Error()))
		writeError(w, status, code, err.Error())
		return
	}

	b64 := base64.StdEncoding.EncodeToString(g.Frame.Data)
	writeJSON(w, http.StatusOK, map[string]any{
		"mime":       g.Frame.MIME,
		"sha256":     g.SHA256,
		"image":      b64,
		"preview":    "data:" + g.Frame.MIME + ";base64," + b64,
		"saved_path": g.SavedPath,
	})
}

// screenshotBody is the POST /api/screenshot payload: the Frame set gathered
// via /api/frame plus an optional prompt override.
type screenshotBody struct {
	Frames         []frameRef `json:"frames"`
	PromptOverride *string    `json:"prompt_override"`
}

type frameRef struct {
	Data string `json:"data"` // base64 frame bytes
}

// screenshot analyzes an ordered Frame set as one question and returns the
// Result; the previews are already known to the client.
func (s *Server) screenshot(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxScreenshotBody)
	var body screenshotBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if len(body.Frames) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "frames: at least one frame is required")
		return
	}

	frames := make([]vision.Frame, 0, len(body.Frames))
	for i, ref := range body.Frames {
		data, err := base64.StdEncoding.DecodeString(ref.Data)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("frames[%d].data: invalid base64: %v", i, err))
			return
		}
		// Re-validate every frame server-side; the MIME is derived from the
		// bytes, never trusted from the client.
		mime, err := capture.ValidateFrame(data)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("frames[%d]: %v", i, err))
			return
		}
		frames = append(frames, vision.Frame{Data: data, MIME: mime})
	}

	prompt := s.cfg.Vision.UserPrompt
	if body.PromptOverride != nil && *body.PromptOverride != "" {
		prompt = *body.PromptOverride
	}

	res, err := s.pipe.Resolve(r.Context(), frames, prompt)
	if err != nil {
		status, code := statusFor(err)
		s.log.Info("screenshot failed",
			slog.String("code", code), slog.String("err", err.Error()))
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// maxHistoryBody bounds the history export upload.
const maxHistoryBody = 4 << 20

// historyBody is the POST /api/history payload: the history export text
// exactly as the UI renders it for download.
type historyBody struct {
	Text string `json:"text"`
}

// saveHistory persists the session history on the server disk, into
// capture.save_dir with a timestamped name. Disabled when save_dir is unset.
func (s *Server) saveHistory(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Capture.SaveDir == "" {
		writeError(w, http.StatusForbidden, "saving_disabled",
			"history saving is disabled: capture.save_dir is not set")
		return
	}
	var body historyBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxHistoryBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "text: history text is empty")
		return
	}

	name := "rslvr-history-" + time.Now().Format("2006-01-02_150405") + ".txt"
	path := filepath.Join(s.cfg.Capture.SaveDir, name)
	if err := os.MkdirAll(s.cfg.Capture.SaveDir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "create save dir: "+err.Error())
		return
	}
	if err := os.WriteFile(path, []byte(body.Text), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "write history: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved_path": path})
}

// statusFor maps pipeline errors onto HTTP statuses per TechSpec §9.
func statusFor(err error) (int, string) {
	switch {
	case errors.Is(err, jobs.ErrBusy):
		return http.StatusConflict, "busy"
	case errors.Is(err, capture.ErrDeviceBusy):
		return http.StatusConflict, "device_busy"
	case errors.Is(err, capture.ErrNoSignal):
		return http.StatusUnprocessableEntity, "no_signal"
	case errors.Is(err, vision.ErrUnreadableImage):
		return http.StatusUnprocessableEntity, "unreadable"
	case errors.Is(err, capture.ErrNoDevices):
		return http.StatusServiceUnavailable, "no_devices"
	case errors.Is(err, capture.ErrTimeout),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled):
		return http.StatusGatewayTimeout, "timeout"
	case errors.Is(err, vision.ErrNoAPIKey),
		errors.Is(err, vision.ErrAuth),
		errors.Is(err, vision.ErrUpstream):
		return http.StatusBadGateway, "upstream_error"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
