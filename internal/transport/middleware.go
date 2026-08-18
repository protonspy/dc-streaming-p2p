// Package transport is the central server's HTTP and WebSocket surface: the
// server itself, the middleware every request passes through, and the handlers
// the control plane exposes.
package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RequestIDHeader is the header a request identifier is read from and written back
// on. A caller that sets it keeps its own identifier, so one identifier follows a
// call across the peer, the server, and whatever sits in front of it.
const RequestIDHeader = "X-Request-ID"

// maxInboundRequestID bounds what is accepted from a caller. An identifier is
// echoed into logs and headers, so it is length-limited and character-limited
// rather than trusted.
const maxInboundRequestID = 64

type contextKey int

const requestIDKey contextKey = iota

// RequestIDFrom returns the identifier carried by ctx, or "" when there is none.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// withRequestID returns ctx carrying id.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// newRequestID generates an identifier for a request that arrived without one.
func newRequestID() string {
	var buf [16]byte
	rand.Read(buf[:]) //nolint:errcheck // crypto/rand.Read never returns an error
	return hex.EncodeToString(buf[:])
}

// sanitizeRequestID accepts an inbound identifier only when it is short and made
// of characters that are safe to write into a header and a log line. Anything else
// is discarded and replaced rather than rejected: a malformed identifier is not
// worth failing a request over.
func sanitizeRequestID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxInboundRequestID {
		return ""
	}
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return ""
		}
	}
	return raw
}

// WithRequestID gives every request an identifier, in its context and on the
// response, taking the caller's when it is usable and generating one when it is not.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get(RequestIDHeader))
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(withRequestID(r.Context(), id)))
	})
}

// WithLogging logs one line per request, after it completes, with its outcome and
// how long it took. It logs the routed pattern rather than the raw path, so an
// identifier in a URL does not become a log field.
func WithLogging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			logger.LogAttrs(r.Context(), levelFor(recorder.status), "request",
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("route", routeOf(r)),
				slog.Int("status", recorder.status),
				slog.Int64("bytes", recorder.written),
				slog.Duration("took", time.Since(started)),
			)
		})
	}
}

// levelFor keeps ordinary traffic at info and lifts the server's own failures to
// error, so a log filtered to errors shows only what is ours to fix.
func levelFor(status int) slog.Level {
	if status >= http.StatusInternalServerError {
		return slog.LevelError
	}
	return slog.LevelInfo
}

// routeOf is the pattern the request matched, falling back to the path when it
// matched nothing — a 404 has no pattern and the path is what says what was asked for.
func routeOf(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.URL.Path
}

// statusRecorder remembers what a handler wrote so the log line can report it.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	written     int64
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer, which is what
// keeps flushing and connection hijacking working through this wrapper. The
// WebSocket upgrade in the control plane depends on it.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Chain applies middleware so that the first named runs first.
func Chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}
