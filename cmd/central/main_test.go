package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/auth"
	"github.com/protonspy/dc-streaming-p2p/internal/config"
)

// testEnv is the smallest environment that starts the server, bound to a port the
// operating system picks.
func testEnv(overrides map[string]string) func(string) string {
	vars := map[string]string{
		"CENTRAL_TOKEN_SIGNING_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), 32)),
		"CENTRAL_CLIENTS": `[{"client_id":"sdk-web","peer_id":"peer-001","secret":"` +
			strings.Repeat("s", 32) + `"}]`,
		"CENTRAL_LISTEN_ADDR": "127.0.0.1:0",
	}
	for k, v := range overrides {
		vars[k] = v
	}
	return func(key string) string { return vars[key] }
}

func TestRunVersionPrintsTheBuild(t *testing.T) {
	var out, errOut bytes.Buffer

	if err := run(context.Background(), []string{"-version"}, testEnv(nil), &out, &errOut); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}

	if got := out.String(); !strings.HasPrefix(got, "central ") {
		t.Errorf("run() printed %q, want it to start with %q", got, "central ")
	}
}

func TestRunRejectsAnUnknownFlag(t *testing.T) {
	var out, errOut bytes.Buffer

	if err := run(context.Background(), []string{"-nonsense"}, testEnv(nil), &out, &errOut); err == nil {
		t.Error("run() error = nil, want a parse error")
	}
}

func TestRunRejectsAnUnknownLogFormat(t *testing.T) {
	var out, errOut bytes.Buffer

	err := run(context.Background(), []string{"-log", "yaml"}, testEnv(nil), &out, &errOut)
	if err == nil {
		t.Fatal("run() error = nil, want a complaint about the format")
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("run() error = %q, want it to name the format", err)
	}
}

func TestRunReportsABadConfiguration(t *testing.T) {
	var out, errOut bytes.Buffer
	env := testEnv(map[string]string{"CENTRAL_TOKEN_SIGNING_KEY": "not base64!"})

	err := run(context.Background(), nil, env, &out, &errOut)
	if err == nil {
		t.Fatal("run() error = nil, want the configuration rejected")
	}
	if !strings.Contains(err.Error(), "TOKEN_SIGNING_KEY") {
		t.Errorf("run() error = %q, want it to name the setting", err)
	}
}

func TestRunServesUntilItsContextIsCancelled(t *testing.T) {
	var out bytes.Buffer
	// The logger writes from the server's goroutines while the test reads, so the
	// sink has to be safe for both.
	errOut := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, testEnv(nil), &out, errOut) }()

	// The startup line is written before the server binds, so its arrival is the
	// signal that run() got as far as serving.
	waitFor(t, func() bool { return strings.Contains(errOut.String(), "starting") })
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run() error = %v, want nil after a cancelled context", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after its context was cancelled")
	}
}

func TestNewLogger(t *testing.T) {
	cases := []struct {
		format string
		want   string
	}{
		{format: "json", want: `"msg":"hello"`},
		{format: "text", want: "msg=hello"},
	}

	for _, c := range cases {
		t.Run(c.format, func(t *testing.T) {
			var buf bytes.Buffer

			logger, err := newLogger(&buf, c.format)
			if err != nil {
				t.Fatalf("newLogger(%q) error = %v, want nil", c.format, err)
			}
			logger.Info("hello")

			if !strings.Contains(buf.String(), c.want) {
				t.Errorf("newLogger(%q) wrote %q, want it to contain %q", c.format, buf.String(), c.want)
			}
		})
	}
}

func TestNewLoggerRejectsAnUnknownFormat(t *testing.T) {
	if _, err := newLogger(io.Discard, "xml"); err == nil {
		t.Error("newLogger() error = nil for an unknown format, want an error")
	}
}

func TestNewLoggerKeepsDebugOut(t *testing.T) {
	var buf bytes.Buffer

	logger, err := newLogger(&buf, "text")
	if err != nil {
		t.Fatalf("newLogger() error = %v, want nil", err)
	}
	logger.Log(context.Background(), slog.LevelDebug, "noise")

	if buf.Len() != 0 {
		t.Errorf("newLogger() wrote %q at debug level, want nothing", buf.String())
	}
}

// syncBuffer is a bytes.Buffer that one goroutine may write while another reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls until cond holds or the test gives up.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

func TestRoutesServesTheHealthCheck(t *testing.T) {
	cfg, err := config.Load(testEnv(nil))
	if err != nil {
		t.Fatalf("config.Load() error = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	testRoutes(t, cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("GET /healthz body = %q, want it to report ok", rec.Body.String())
	}
}

func TestRoutesAnswersNothingElseYet(t *testing.T) {
	cfg, err := config.Load(testEnv(nil))
	if err != nil {
		t.Fatalf("config.Load() error = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	testRoutes(t, cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/peers", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /peers = %d, want %d until the specs land", rec.Code, http.StatusNotFound)
	}
}

func TestRunHelpIsNotAFailure(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer

	if err := run(context.Background(), []string{"-help"}, testEnv(nil), &out, &errOut); err != nil {
		t.Errorf("run(-help) error = %v, want nil — asking for the usage is not a failure", err)
	}
	if !strings.Contains(errOut.String(), "-version") {
		t.Errorf("run(-help) wrote %q, want the usage", errOut.String())
	}
}

// testRoutes builds the router the way run() does, from a loaded configuration.
func testRoutes(t *testing.T, cfg config.Config) *http.ServeMux {
	t.Helper()

	clients, err := auth.NewClientStore(cfg.Clients)
	if err != nil {
		t.Fatalf("NewClientStore() error = %v, want nil", err)
	}
	issuer, err := auth.NewTokenIssuer(cfg.TokenSigningKey, cfg.Issuer, cfg.TokenTTL)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v, want nil", err)
	}
	limiter, err := auth.NewAttemptLimiter(cfg.AuthMaxFailures, cfg.AuthFailureWindow)
	if err != nil {
		t.Fatalf("NewAttemptLimiter() error = %v, want nil", err)
	}

	return routes(cfg, clients, issuer, limiter, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRoutesServesTheKeySetAndTheAuthEndpoint(t *testing.T) {
	cfg, err := config.Load(testEnv(nil))
	if err != nil {
		t.Fatalf("config.Load() error = %v, want nil", err)
	}
	mux := testRoutes(t, cfg)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /.well-known/jwks.json = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Ed25519") {
		t.Errorf("key set = %q, want the published Ed25519 key", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	body := `{"client_id":"sdk-web","client_secret":"` + strings.Repeat("s", 32) + `"}`
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Errorf("POST /auth = %d, want %d — body %s", rec.Code, http.StatusOK, rec.Body)
	}
}
