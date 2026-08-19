package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/ice"
)

const relaySecret = "the-secret-the-relay-also-holds"

// iceFixture is the endpoint behind the real token middleware.
type iceFixture struct {
	handler http.Handler
	auth    authFixture
}

func newICEFixture(t *testing.T, turn []string) iceFixture {
	t.Helper()

	secret := ""
	if len(turn) > 0 {
		secret = relaySecret
	}

	provider, err := ice.NewProvider(ice.Options{
		STUNURLs:      []string{"stun:stun.example:3478"},
		TURNURLs:      turn,
		Secret:        secret,
		CredentialTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ice.NewProvider() error = %v, want nil", err)
	}

	auth := newAuthFixture(t, 10)
	return iceFixture{handler: RequireToken(auth.verifier)(ICEConfig(provider)), auth: auth}
}

func (f iceFixture) get(t *testing.T, peerID string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/ice-config", nil)
	if peerID != "" {
		token, err := f.auth.issuer.Issue(peerID)
		if err != nil {
			t.Fatalf("Issue() error = %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.Value)
	}

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func TestICEConfigNamesTheCallingPeer(t *testing.T) {
	f := newICEFixture(t, []string{"turn:relay.example:3478"})

	rec := f.get(t, "peer-001")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ice-config = %d, want %d — body %s", rec.Code, http.StatusOK, rec.Body)
	}

	var body iceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}

	if !body.RelayAvailable {
		t.Error("relay_available = false with a relay configured")
	}
	if body.ExpiresAt == nil || body.ExpiresAt.Before(time.Now()) {
		t.Errorf("expires_at = %v, want a moment in the future", body.ExpiresAt)
	}

	var relay ice.Server
	for _, server := range body.ICEServers {
		if server.Username != "" {
			relay = server
		}
	}
	if !strings.HasSuffix(relay.Username, ":peer-001") {
		t.Errorf("relay username = %q, want it to name the calling peer", relay.Username)
	}
}

func TestICEConfigWithoutARelaySaysSo(t *testing.T) {
	f := newICEFixture(t, nil)

	rec := f.get(t, "peer-001")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ice-config = %d, want %d", rec.Code, http.StatusOK)
	}

	var body iceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body.RelayAvailable {
		t.Error("relay_available = true with no relay configured")
	}
	if body.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want it absent — nothing in this answer expires", body.ExpiresAt)
	}
	if len(body.ICEServers) != 1 {
		t.Errorf("ice_servers = %+v, want the STUN entry alone", body.ICEServers)
	}
}

func TestICEConfigRefusesAnUnauthenticatedCaller(t *testing.T) {
	f := newICEFixture(t, []string{"turn:relay.example:3478"})

	if rec := f.get(t, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d without a token", rec.Code, http.StatusUnauthorized)
	}
}

func TestICEConfigNeverCarriesTheSecret(t *testing.T) {
	f := newICEFixture(t, []string{"turn:relay.example:3478"})

	rec := f.get(t, "peer-001")
	if strings.Contains(rec.Body.String(), relaySecret) {
		t.Errorf("the response carries the shared secret: %s", rec.Body.String())
	}
}

func TestICEConfigIsNotCached(t *testing.T) {
	f := newICEFixture(t, []string{"turn:relay.example:3478"})

	rec := f.get(t, "peer-001")
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on a response carrying a credential", got)
	}
}

func TestICEConfigGivesEachPeerItsOwn(t *testing.T) {
	f := newICEFixture(t, []string{"turn:relay.example:3478"})

	first := f.get(t, "peer-001").Body.String()
	second := f.get(t, "peer-002").Body.String()

	if first == second {
		t.Error("two peers were handed the same configuration, credential included")
	}
}
