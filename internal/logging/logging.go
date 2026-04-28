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
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/kardianos/service"
)

// Init sets up the default slog logger. Call once at startup.
// If svcLogger is non-nil (service mode), logs route to the system log.
// If logFile is non-empty, logs also write to that file.
// Otherwise, logs go to stderr as text.
func Init(svcLogger service.Logger, logFile string) *os.File {
	var handler slog.Handler
	var file *os.File

	if svcLogger != nil {
		handler = &serviceHandler{svc: svcLogger}
		// In service mode, also write to file if specified
		if logFile != "" {
			if f, err := openLogFile(logFile); err == nil {
				file = f
				fileHandler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
				handler = &multiHandler{handlers: []slog.Handler{handler, fileHandler}}
			}
		}
	} else if logFile != "" {
		// Interactive mode with file: write to both stderr and file
		if f, err := openLogFile(logFile); err == nil {
			file = f
			stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
			fileHandler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
			handler = &multiHandler{handlers: []slog.Handler{stderrHandler, fileHandler}}
		} else {
			handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
		}
	} else {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	}

	slog.SetDefault(slog.New(handler))
	return file
}

func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// multiHandler fans out log records to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if err := h.Handle(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
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
// It preserves http.Flusher and http.Hijacker for SSE compatibility.
type responseCapture struct {
	http.ResponseWriter
	status int
}

func (rc *responseCapture) WriteHeader(code int) {
	rc.status = code
	rc.ResponseWriter.WriteHeader(code)
}

func (rc *responseCapture) Flush() {
	if f, ok := rc.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// RequestMiddleware logs HTTP requests with method, path, status, and duration.
// SSE connections (/mcp) are logged at connect time only, not on completion.
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
