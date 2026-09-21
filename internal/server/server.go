// Package server exposes the local HTTP API and the embedded single-page UI.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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
	mux.HandleFunc("POST /api/screenshot", s.screenshot)
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

// screenshotBody is the POST /api/screenshot payload; all fields optional.
type screenshotBody struct {
	Device         string  `json:"device"`
	PromptOverride *string `json:"prompt_override"`
	Save           *bool   `json:"save"`
}

// screenshot runs one job synchronously and returns the result with an
// inline preview of the frame.
func (s *Server) screenshot(w http.ResponseWriter, r *http.Request) {
	var body screenshotBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}

	prompt := s.cfg.Vision.UserPrompt
	if body.PromptOverride != nil && *body.PromptOverride != "" {
		prompt = *body.PromptOverride
	}
	save := s.cfg.Capture.SaveDir != ""
	if body.Save != nil {
		save = *body.Save
	}

	out, err := s.pipe.Run(r.Context(), jobs.Request{
		Device: body.Device,
		Prompt: prompt,
		Save:   save,
	})
	if err != nil {
		status, code := statusFor(err)
		s.log.Info("screenshot failed",
			slog.String("code", code), slog.String("err", err.Error()))
		writeError(w, status, code, err.Error())
		return
	}

	resp := struct {
		vision.Result
		Preview string `json:"preview,omitempty"`
	}{Result: out.Result}
	if len(out.Frame) > 0 {
		resp.Preview = "data:" + out.MIME + ";base64," + base64.StdEncoding.EncodeToString(out.Frame)
	}
	writeJSON(w, http.StatusOK, resp)
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
