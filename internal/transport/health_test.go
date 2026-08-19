package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// serveHealth runs one request against a health handler built from deps.
func serveHealth(t *testing.T, deps HealthDeps) (*httptest.ResponseRecorder, healthResponse) {
	t.Helper()

	rec := httptest.NewRecorder()
	Health(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return rec, body
}

func TestHealthReportsTheCounts(t *testing.T) {
	rec, body := serveHealth(t, HealthDeps{
		PeersOnline:     func() int { return 3 },
		SessionsLive:    func() int { return 1 },
		RelayConfigured: true,
		Build:           "central 1.0.0",
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body.Status != StatusOK {
		t.Errorf("status field = %q, want %q", body.Status, StatusOK)
	}
	if body.PeersOnline != 3 || body.SessionsLive != 1 {
		t.Errorf("counts = %d peers / %d sessions, want 3 / 1", body.PeersOnline, body.SessionsLive)
	}
	if !body.RelayConfigured {
		t.Error("relay_configured = false, want true")
	}
	if body.Build != "central 1.0.0" {
		t.Errorf("build = %q, want the injected build", body.Build)
	}
}

func TestHealthReadsTheCountsPerRequest(t *testing.T) {
	peers := 0
	deps := HealthDeps{PeersOnline: func() int { peers++; return peers }}
	handler := Health(deps)

	for want := 1; want <= 3; want++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

		var body healthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
		}
		if body.PeersOnline != want {
			t.Errorf("peers_online = %d on request %d, want %d — the count is read per request", body.PeersOnline, want, want)
		}
	}
}

func TestHealthWithNothingWiredUp(t *testing.T) {
	rec, body := serveHealth(t, HealthDeps{})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d even with no counters wired up", rec.Code, http.StatusOK)
	}
	if body.PeersOnline != 0 || body.SessionsLive != 0 {
		t.Errorf("counts = %d / %d, want zeroes", body.PeersOnline, body.SessionsLive)
	}
}

func TestHealthReportsUptime(t *testing.T) {
	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	deps := HealthDeps{Now: func() time.Time { return clock }}
	handler := Health(deps)

	clock = clock.Add(90 * time.Second)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body.UptimeSeconds != 90 {
		t.Errorf("uptime_seconds = %d, want 90", body.UptimeSeconds)
	}
}

func TestHealthSetsItsHeaders(t *testing.T) {
	rec, _ := serveHealth(t, HealthDeps{})

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q — a cached health check answers for a server that has stopped", got, "no-store")
	}
}

func TestHealthReportsNoIdentities(t *testing.T) {
	rec, _ := serveHealth(t, HealthDeps{
		PeersOnline:  func() int { return 2 },
		SessionsLive: func() int { return 1 },
	})

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}

	want := map[string]bool{
		"status": true, "build": true, "uptime_seconds": true,
		"peers_online": true, "sessions_live": true, "relay_configured": true,
		"channels_open": true,
	}
	for key := range raw {
		if !want[key] {
			t.Errorf("body carries %q; the health check is unauthenticated and reports counts only", key)
		}
	}
}

func TestCountWithNoCounter(t *testing.T) {
	if got := count(nil); got != 0 {
		t.Errorf("count(nil) = %d, want 0", got)
	}
}
