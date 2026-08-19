package transport

import (
	"encoding/json"
	"net/http"
	"time"
)

// HealthDeps is what the health endpoint reports on. Each count is a function
// rather than a value because it is read at the moment of the request, and the
// registry and the session store are the ones that know it.
//
// A nil counter reports zero rather than failing: the endpoint has to answer while
// the server is still assembling itself, which is exactly when it is asked.
type HealthDeps struct {
	// PeersOnline is how many peers the registry currently holds.
	PeersOnline func() int
	// SessionsLive is how many sessions are negotiating or connected.
	SessionsLive func() int
	// RelayConfigured says whether peers can be offered a relay at all. False
	// means a peer with no direct path has nowhere to fall back to.
	RelayConfigured bool
	// Build identifies the running binary.
	Build string
	// Now is the clock, injected so a test can hold it still.
	Now func() time.Time
}

// healthResponse is the body of a health check, and it is a contract: a probe and
// a dashboard both read these names.
type healthResponse struct {
	Status          string `json:"status"`
	Build           string `json:"build"`
	UptimeSeconds   int64  `json:"uptime_seconds"`
	PeersOnline     int    `json:"peers_online"`
	SessionsLive    int    `json:"sessions_live"`
	RelayConfigured bool   `json:"relay_configured"`
}

// StatusOK is the only status this endpoint reports. The check is a liveness
// check: a server that can answer it is serving. Whether a peer can complete a
// call depends on the relay and on the peer's own network, neither of which this
// process can decide for it — so relay_configured is reported rather than folded
// into a verdict.
const StatusOK = "ok"

// Health serves the health check. It is registered without authentication, so it
// reports counts and never identities.
func Health(deps HealthDeps) http.Handler {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	started := now()

	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := healthResponse{
			Status:          StatusOK,
			Build:           deps.Build,
			UptimeSeconds:   int64(now().Sub(started).Seconds()),
			PeersOnline:     count(deps.PeersOnline),
			SessionsLive:    count(deps.SessionsLive),
			RelayConfigured: deps.RelayConfigured,
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			// The status line is already sent; there is nothing left to tell the
			// caller, and the request log carries the failure.
			return
		}
	})
}

// count reads a counter that may not be wired up yet.
func count(f func() int) int {
	if f == nil {
		return 0
	}
	return f()
}
