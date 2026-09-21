package server

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware outermost-first.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// WithRecover converts panics into JSON 500s instead of connection resets.
func WithRecover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic in handler",
						slog.Any("panic", rec),
						slog.String("path", r.URL.Path))
					writeError(w, http.StatusInternalServerError, "internal", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the response status for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// WithLogging logs one line per request; quiet paths log at Debug.
func WithLogging(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			if r.URL.Path == "/api/health" || r.URL.Path == "/" {
				level = slog.LevelDebug
			}
			log.Log(r.Context(), level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()))
		})
	}
}

// parseCIDRs converts config entries ("10.0.0.0/24" or "192.168.1.5") to nets.
func parseCIDRs(entries []string) []*net.IPNet {
	var nets []*net.IPNet
	for _, e := range entries {
		if strings.Contains(e, "/") {
			if _, n, err := net.ParseCIDR(e); err == nil {
				nets = append(nets, n)
			}
			continue
		}
		if ip := net.ParseIP(strings.TrimSpace(e)); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return nets
}

// WithCIDR restricts access to the whitelist (TechSpec §9). X-Forwarded-For is
// deliberately ignored: it is trivially spoofable on a LAN.
func WithCIDR(allowed []*net.IPNet) Middleware {
	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			ip := net.ParseIP(host)
			permitted := ip != nil && slices.ContainsFunc(allowed, func(n *net.IPNet) bool {
				return n.Contains(ip)
			})
			if !permitted {
				writeError(w, http.StatusForbidden, "forbidden", "source address is not allowed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// authTokenCookie is the cookie name the UI sets after ?token= handoff.
const authTokenCookie = "rslvr_token"

// WithToken enforces the shared token (TechSpec §9) via Authorization: Bearer,
// the X-Auth-Token header, or the rslvr_token cookie. Empty token = no auth.
// /api/health is exempt so liveness probes work without credentials.
func WithToken(token string) Middleware {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/health" {
				next.ServeHTTP(w, r)
				return
			}
			got := r.Header.Get("X-Auth-Token")
			if got == "" {
				if c, err := r.Cookie(authTokenCookie); err == nil {
					got = c.Value
				}
			}
			if got == "" {
				if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
					got = strings.TrimPrefix(auth, "Bearer ")
				}
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="rslvr"`)
				writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid auth token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
