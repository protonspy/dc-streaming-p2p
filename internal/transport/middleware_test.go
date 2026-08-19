package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithRequestIDGeneratesOneWhenAbsent(t *testing.T) {
	var seen string
	handler := WithRequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if seen == "" {
		t.Fatal("RequestIDFrom() = \"\", want a generated identifier")
	}
	if got := rec.Header().Get(RequestIDHeader); got != seen {
		t.Errorf("%s = %q, want the context identifier %q", RequestIDHeader, got, seen)
	}
}

func TestWithRequestIDKeepsTheCallers(t *testing.T) {
	handler := WithRequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if got := RequestIDFrom(r.Context()); got != "peer-001-call-7" {
			t.Errorf("RequestIDFrom() = %q, want the caller's identifier", got)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "peer-001-call-7")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != "peer-001-call-7" {
		t.Errorf("%s = %q, want it echoed back", RequestIDHeader, got)
	}
}

func TestWithRequestIDReplacesAnUnusableOne(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "a newline that would forge a log line", raw: "abc\ndef"},
		{name: "a header injection attempt", raw: "abc\r\nSet-Cookie: x=1"},
		{name: "longer than the limit", raw: strings.Repeat("a", maxInboundRequestID+1)},
		{name: "spaces only", raw: "   "},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var seen string
			handler := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = RequestIDFrom(r.Context())
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header[RequestIDHeader] = []string{c.raw}
			handler.ServeHTTP(httptest.NewRecorder(), req)

			if seen == c.raw || seen == "" {
				t.Errorf("RequestIDFrom() = %q, want a generated replacement", seen)
			}
		})
	}
}

func TestRequestIDFromAContextWithout(t *testing.T) {
	if got := RequestIDFrom(context.Background()); got != "" {
		t.Errorf("RequestIDFrom() = %q, want an empty string", got)
	}
}

func TestNewRequestIDIsUnique(t *testing.T) {
	seen := make(map[string]bool, 64)
	for range 64 {
		id := newRequestID()
		if len(id) != 32 {
			t.Fatalf("newRequestID() = %q, want 32 hex characters", id)
		}
		if seen[id] {
			t.Fatalf("newRequestID() repeated %q", id)
		}
		seen[id] = true
	}
}

func TestSanitizeRequestID(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "peer-001.call_7", want: "peer-001.call_7"},
		{raw: "  trimmed  ", want: "trimmed"},
		{raw: "with space", want: ""},
		{raw: "semi;colon", want: ""},
		{raw: "", want: ""},
	}

	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			if got := sanitizeRequestID(c.raw); got != c.want {
				t.Errorf("sanitizeRequestID(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// logLine is one JSON record from the test logger.
type logLine struct {
	Level     string `json:"level"`
	Msg       string `json:"msg"`
	RequestID string `json:"request_id"`
	Method    string `json:"method"`
	Route     string `json:"route"`
	Status    int    `json:"status"`
	Bytes     int64  `json:"bytes"`
}

// serveLogged runs one request through the request-identifier and logging
// middleware and returns the record that came out.
func serveLogged(t *testing.T, mux *http.ServeMux, req *http.Request) logLine {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := Chain(mux, WithRequestID, WithLogging(logger))

	handler.ServeHTTP(httptest.NewRecorder(), req)

	var line logLine
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("log line %q is not JSON: %v", buf.String(), err)
	}
	return line
}

func TestWithLoggingReportsTheRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /peers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	line := serveLogged(t, mux, httptest.NewRequest(http.MethodGet, "/peers/peer-001", nil))

	if line.Route != "GET /peers/{id}" {
		t.Errorf("route = %q, want the pattern rather than the path", line.Route)
	}
	if line.Status != http.StatusNoContent {
		t.Errorf("status = %d, want %d", line.Status, http.StatusNoContent)
	}
	if line.RequestID == "" {
		t.Error("request_id = \"\", want the identifier the middleware set")
	}
	if line.Level != slog.LevelInfo.String() {
		t.Errorf("level = %q, want %q", line.Level, slog.LevelInfo.String())
	}
}

func TestWithLoggingCountsWhatWasWritten(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /body", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("twelve bytes"))
	})

	line := serveLogged(t, mux, httptest.NewRequest(http.MethodGet, "/body", nil))

	if line.Status != http.StatusOK {
		t.Errorf("status = %d, want %d for a handler that only wrote a body", line.Status, http.StatusOK)
	}
	if line.Bytes != 12 {
		t.Errorf("bytes = %d, want 12", line.Bytes)
	}
}

func TestWithLoggingLiftsServerFailuresToError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /broken", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	line := serveLogged(t, mux, httptest.NewRequest(http.MethodGet, "/broken", nil))

	if line.Level != slog.LevelError.String() {
		t.Errorf("level = %q, want %q for a 500", line.Level, slog.LevelError.String())
	}
}

func TestWithLoggingFallsBackToThePathWhenNothingMatched(t *testing.T) {
	line := serveLogged(t, http.NewServeMux(), httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	if line.Route != "/nowhere" {
		t.Errorf("route = %q, want the path when no pattern matched", line.Route)
	}
	if line.Status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", line.Status, http.StatusNotFound)
	}
}

func TestLevelFor(t *testing.T) {
	cases := map[int]slog.Level{
		http.StatusOK:                  slog.LevelInfo,
		http.StatusNotFound:            slog.LevelInfo,
		http.StatusInternalServerError: slog.LevelError,
		http.StatusBadGateway:          slog.LevelError,
	}

	for status, want := range cases {
		if got := levelFor(status); got != want {
			t.Errorf("levelFor(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestStatusRecorderKeepsTheFirstStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	recorder.WriteHeader(http.StatusTeapot)
	recorder.WriteHeader(http.StatusInternalServerError)

	if recorder.status != http.StatusTeapot {
		t.Errorf("status = %d, want the first one written", recorder.status)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("underlying status = %d, want %d", rec.Code, http.StatusTeapot)
	}
}

func TestStatusRecorderUnwraps(t *testing.T) {
	rec := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	if recorder.Unwrap() != http.ResponseWriter(rec) {
		t.Error("Unwrap() did not return the wrapped writer, which breaks flush and hijack")
	}
}

func TestChainRunsInOrder(t *testing.T) {
	var order []string
	mark := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	handler := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	}), mark("first"), mark("second"))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := []string{"first", "second", "handler"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}
