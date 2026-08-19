package transport

import (
	"net/http"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/ice"
)

// iceResponse is what a peer receives. `ice_servers` is exactly the shape
// RTCPeerConnection takes, so the SDK passes it through without rewriting it.
type iceResponse struct {
	ICEServers     []ice.Server `json:"ice_servers"`
	ExpiresAt      *time.Time   `json:"expires_at,omitempty"`
	RelayAvailable bool         `json:"relay_available"`
}

// ICEConfig serves the STUN and relay servers one peer should use, with relay
// credentials that expire on their own.
func ICEConfig(provider *ice.Provider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerID := PeerIDFrom(r.Context())
		if peerID == "" {
			writeTokenError(w, http.StatusUnauthorized, codeInvalidToken)
			return
		}

		config, err := provider.For(peerID)
		if err != nil {
			// The failure is ours — a peer identifier this server issued that the
			// relay cannot read — and the detail stays in the log, not the body.
			writeError(w, http.StatusInternalServerError, codeInvalidRequest)
			return
		}

		body := iceResponse{
			ICEServers:     config.Servers,
			RelayAvailable: config.RelayAvailable,
		}
		if !config.ExpiresAt.IsZero() {
			expires := config.ExpiresAt
			body.ExpiresAt = &expires
		}

		// A credential is in here, so it is never cached.
		writeJSONStatus(w, http.StatusOK, body)
	})
}
