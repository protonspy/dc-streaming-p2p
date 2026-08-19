package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/registry"
)

// peerFixture is a registry behind the real token middleware, with a clock the
// test moves by hand.
type peerFixture struct {
	registry *registry.Registry
	clock    *time.Time
	mux      *http.ServeMux
	auth     authFixture
}

func newPeerFixture(t *testing.T, ceiling int) peerFixture {
	t.Helper()

	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	reg, err := registry.New(registry.Options{
		HeartbeatInterval: 10 * time.Second,
		SuspectAfter:      30 * time.Second,
		OfflineAfter:      60 * time.Second,
		MaxPeers:          ceiling,
		Now:               func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("registry.New() error = %v, want nil", err)
	}

	auth := newAuthFixture(t, 10)

	mux := http.NewServeMux()
	guard := RequireToken(auth.verifier)
	mux.Handle("POST /peers", guard(Peers(reg)))
	mux.Handle("DELETE /peers", guard(Peers(reg)))
	mux.Handle("POST /peers/heartbeat", guard(Heartbeat(reg)))
	mux.Handle("GET /peers/{id}", guard(PeerLookup(reg)))

	return peerFixture{registry: reg, clock: &clock, mux: mux, auth: auth}
}

// tokenFor mints a token for one peer identifier.
func (f peerFixture) tokenFor(t *testing.T, peerID string) string {
	t.Helper()

	token, err := f.auth.issuer.Issue(peerID)
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}
	return token.Value
}

// do sends a request, with the token when one is given.
func (f peerFixture) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func decodePeer(t *testing.T, rec *httptest.ResponseRecorder) peerResponse {
	t.Helper()

	var body peerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return body
}

func TestRegisterPutsTheCallerInTheRegistry(t *testing.T) {
	f := newPeerFixture(t, 10)

	rec := f.do(t, http.MethodPost, "/peers", f.tokenFor(t, "peer-001"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /peers = %d, want %d — body %s", rec.Code, http.StatusOK, rec.Body)
	}

	body := decodePeer(t, rec)
	if body.PeerID != "peer-001" || body.State != string(registry.Online) {
		t.Errorf("body = %+v, want peer-001 online", body)
	}
	if body.HeartbeatInterval != "10s" {
		t.Errorf("heartbeat_interval = %q, want %q — a peer should not have to be told twice", body.HeartbeatInterval, "10s")
	}
	if _, held := f.registry.Lookup("peer-001"); !held {
		t.Error("the registry does not hold the peer that registered")
	}
}

func TestRegisterIgnoresAnIdentityInTheBody(t *testing.T) {
	f := newPeerFixture(t, 10)

	rec := f.do(t, http.MethodPost, "/peers", f.tokenFor(t, "peer-001"), `{"peer_id":"peer-999"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /peers = %d, want %d", rec.Code, http.StatusOK)
	}

	if _, held := f.registry.Lookup("peer-999"); held {
		t.Error("a peer registered an identifier from the request body; the token is the only identity")
	}
	if _, held := f.registry.Lookup("peer-001"); !held {
		t.Error("the token's peer identifier was not the one registered")
	}
}

func TestRegistryRoutesRefuseAnUnauthenticatedCaller(t *testing.T) {
	f := newPeerFixture(t, 10)

	cases := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/peers"},
		{method: http.MethodDelete, path: "/peers"},
		{method: http.MethodPost, path: "/peers/heartbeat"},
		{method: http.MethodGet, path: "/peers/peer-001"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := f.do(t, c.method, c.path, "", "")
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d without a token", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestDeregisterRemovesTheCaller(t *testing.T) {
	f := newPeerFixture(t, 10)
	token := f.tokenFor(t, "peer-001")

	f.do(t, http.MethodPost, "/peers", token, "")

	rec := f.do(t, http.MethodDelete, "/peers", token, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /peers = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if _, held := f.registry.Lookup("peer-001"); held {
		t.Error("the peer is still held after deregistering")
	}
}

func TestDeregisterIsQuietWhenThereIsNothingToRemove(t *testing.T) {
	f := newPeerFixture(t, 10)

	rec := f.do(t, http.MethodDelete, "/peers", f.tokenFor(t, "peer-001"), "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE /peers = %d, want %d — the caller wanted it gone and it is gone", rec.Code, http.StatusNoContent)
	}
}

func TestRegisterAnswersWhenTheRegistryIsFull(t *testing.T) {
	f := newPeerFixture(t, 1)

	f.do(t, http.MethodPost, "/peers", f.tokenFor(t, "peer-001"), "")

	rec := f.do(t, http.MethodPost, "/peers", f.tokenFor(t, "peer-002"), "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /peers = %d, want %d when the registry is full", rec.Code, http.StatusServiceUnavailable)
	}

	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body.Error != codeRegistryFull {
		t.Errorf("error = %q, want %q", body.Error, codeRegistryFull)
	}
	if _, held := f.registry.Lookup("peer-001"); !held {
		t.Error("the peer that was already online was evicted to admit a new one")
	}
}

func TestHeartbeatKeepsAPeerOnline(t *testing.T) {
	f := newPeerFixture(t, 10)
	token := f.tokenFor(t, "peer-001")

	f.do(t, http.MethodPost, "/peers", token, "")
	*f.clock = f.clock.Add(45 * time.Second)

	rec := f.do(t, http.MethodPost, "/peers/heartbeat", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /peers/heartbeat = %d, want %d — body %s", rec.Code, http.StatusOK, rec.Body)
	}
	if body := decodePeer(t, rec); body.State != string(registry.Online) {
		t.Errorf("state = %q after a heartbeat, want %q", body.State, registry.Online)
	}
}

func TestHeartbeatTellsARemovedPeerToRegisterAgain(t *testing.T) {
	f := newPeerFixture(t, 10)
	token := f.tokenFor(t, "peer-001")

	f.do(t, http.MethodPost, "/peers", token, "")
	*f.clock = f.clock.Add(2 * time.Minute)

	rec := f.do(t, http.MethodPost, "/peers/heartbeat", token, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /peers/heartbeat = %d, want %d for a peer that was removed", rec.Code, http.StatusConflict)
	}

	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body.Error != codeRegistrationRequired {
		t.Errorf("error = %q, want %q — the peer is being told what to do about it", body.Error, codeRegistrationRequired)
	}
}

func TestLookupReportsAPeer(t *testing.T) {
	f := newPeerFixture(t, 10)

	f.do(t, http.MethodPost, "/peers", f.tokenFor(t, "peer-001"), "")

	rec := f.do(t, http.MethodGet, "/peers/peer-001", f.tokenFor(t, "peer-002"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /peers/peer-001 = %d, want %d", rec.Code, http.StatusOK)
	}

	body := decodePeer(t, rec)
	if body.PeerID != "peer-001" || body.State != string(registry.Online) {
		t.Errorf("body = %+v, want peer-001 online", body)
	}
	if body.LastSeen.IsZero() {
		t.Error("last_seen is zero, want the moment it reported in")
	}
}

func TestLookupReportsASuspectPeer(t *testing.T) {
	f := newPeerFixture(t, 10)

	f.do(t, http.MethodPost, "/peers", f.tokenFor(t, "peer-001"), "")
	*f.clock = f.clock.Add(45 * time.Second)

	rec := f.do(t, http.MethodGet, "/peers/peer-001", f.tokenFor(t, "peer-002"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /peers/peer-001 = %d, want %d — a suspect peer is still answered for", rec.Code, http.StatusOK)
	}
	if body := decodePeer(t, rec); body.State != string(registry.Suspect) {
		t.Errorf("state = %q, want %q", body.State, registry.Suspect)
	}
}

func TestLookupAnswersTheSameForNeverHereAndGone(t *testing.T) {
	f := newPeerFixture(t, 10)
	caller := f.tokenFor(t, "peer-002")

	f.do(t, http.MethodPost, "/peers", f.tokenFor(t, "peer-001"), "")
	*f.clock = f.clock.Add(2 * time.Minute)

	gone := f.do(t, http.MethodGet, "/peers/peer-001", caller, "")
	never := f.do(t, http.MethodGet, "/peers/peer-404", caller, "")

	if gone.Code != http.StatusNotFound || never.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d and %d, want %d for both", gone.Code, never.Code, http.StatusNotFound)
	}
	if gone.Body.String() != never.Body.String() {
		t.Errorf("a peer that has gone answers %s and one that never registered answers %s; the difference says who used to be here",
			gone.Body.String(), never.Body.String())
	}
}

func TestRegistryRoutesRefuseAnExpiredToken(t *testing.T) {
	f := newPeerFixture(t, 10)

	rec := f.do(t, http.MethodPost, "/peers", "not-a-token", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestRegisterIsIdempotentForOneCaller(t *testing.T) {
	f := newPeerFixture(t, 10)
	token := f.tokenFor(t, "peer-001")

	for i := range 3 {
		if rec := f.do(t, http.MethodPost, "/peers", token, ""); rec.Code != http.StatusOK {
			t.Fatalf("POST /peers = %d on call %d, want %d", rec.Code, i+1, http.StatusOK)
		}
	}

	if got := f.registry.Count().Total; got != 1 {
		t.Errorf("the registry holds %d records for one peer registering %d times, want 1", got, 3)
	}
}

func TestPeersRefusesAMethodItDoesNotServe(t *testing.T) {
	f := newPeerFixture(t, 10)

	// Reached directly, since the router would answer 405 before the handler.
	req := httptest.NewRequest(http.MethodPut, "/peers", nil)
	rec := httptest.NewRecorder()
	RequireToken(f.auth.verifier)(Peers(f.registry)).ServeHTTP(rec, withToken(req, f.tokenFor(t, "peer-001")))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /peers = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); !strings.Contains(got, "POST") {
		t.Errorf("Allow = %q, want it to name the methods this route serves", got)
	}
}

func TestCountsFollowRegistrations(t *testing.T) {
	f := newPeerFixture(t, 10)

	for i := range 3 {
		f.do(t, http.MethodPost, "/peers", f.tokenFor(t, "peer-"+strconv.Itoa(i)), "")
	}

	if got := f.registry.Count(); got.Online != 3 || got.Total != 3 {
		t.Errorf("Count() = %+v, want three online", got)
	}
}

// withToken puts a bearer token on a request.
func withToken(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}
