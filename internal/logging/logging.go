// Package logging provides structured logging for mind-map using log/slog.
// It bridges kardianos/service.Logger to slog for system log integration
// (journald on Linux, Event Log on Windows, syslog on macOS).
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/kardianos/service"
)

// Init sets up the default slog logger. Call once at startup.
// If svcLogger is non-nil (service mode), logs route to the system log.
// Otherwise, logs go to stderr as text.
func Init(svcLogger service.Logger) {
	var handler slog.Handler
	if svcLogger != nil {
		handler = &serviceHandler{svc: svcLogger}
	} else {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	}
	slog.SetDefault(slog.New(handler))
}

// serviceHandler adapts kardianos/service.Logger to slog.Handler.
type serviceHandler struct {
	svc   service.Logger
	attrs []slog.Attr
	group string
}

func (h *serviceHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *serviceHandler) Handle(_ context.Context, r slog.Record) error {
	msg := r.Message
	if len(h.attrs) > 0 || r.NumAttrs() > 0 {
		msg += " |"
	}
	r.Attrs(func(a slog.Attr) bool {
		msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
		return true
	})
	for _, a := range h.attrs {
		msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
	}

	switch {
	case r.Level >= slog.LevelError:
		return h.svc.Error(msg)
	case r.Level >= slog.LevelWarn:
		return h.svc.Warning(msg)
	default:
		return h.svc.Info(msg)
	}
}

func (h *serviceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &serviceHandler{
		svc:   h.svc,
		attrs: append(h.attrs, attrs...),
		group: h.group,
	}
}

func (h *serviceHandler) WithGroup(name string) slog.Handler {
	return &serviceHandler{
		svc:   h.svc,
		attrs: h.attrs,
		group: name,
	}
}

// RecoverMiddleware wraps an http.Handler with panic recovery.
// On panic, it logs the stack trace and returns 500.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				stack := string(debug.Stack())
				slog.Error("panic recovered",
					slog.Any("error", err),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("stack", stack),
				)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseCapture wraps http.ResponseWriter to capture the status code.
type responseCapture struct {
	http.ResponseWriter
	status int
}

func (rc *responseCapture) WriteHeader(code int) {
	rc.status = code
	rc.ResponseWriter.WriteHeader(code)
}

// RequestMiddleware logs HTTP requests with method, path, status, and duration.
func RequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rc := &responseCapture{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rc, r)
		slog.Debug("http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rc.status),
			slog.Duration("elapsed", time.Since(start)),
		)
	})
}

// SafeGo runs fn in a goroutine with panic recovery and logging.
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("goroutine panic",
					slog.String("goroutine", name),
					slog.Any("error", err),
					slog.String("stack", string(debug.Stack())),
				)
			}
		}()
		fn()
	}()
}
