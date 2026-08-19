package transport

import (
	"errors"
	"net/http"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/registry"
)

// Error codes the registry surface answers with.
const (
	codeNotFound             = "not_found"
	codeRegistrationRequired = "registration_required"
	codeRegistryFull         = "registry_full"
)

// peerResponse is one peer as the control plane reports it.
type peerResponse struct {
	PeerID   string    `json:"peer_id"`
	State    string    `json:"state"`
	LastSeen time.Time `json:"last_seen"`
	// HeartbeatInterval is how often this peer is expected to report in. It is
	// answered on registration so a peer does not have to be configured with a
	// number the server already knows.
	HeartbeatInterval string `json:"heartbeat_interval,omitempty"`
}

// Peers serves registration and deregistration for the calling peer.
//
// There is no route that writes a peer other than the caller: the identifier comes
// from the verified token, never from the body or the path, so a peer can register
// itself and nothing else.
func Peers(reg *registry.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerID := PeerIDFrom(r.Context())
		if peerID == "" {
			writeTokenError(w, http.StatusUnauthorized, codeInvalidToken)
			return
		}

		switch r.Method {
		case http.MethodPost:
			peer, err := reg.Register(peerID)
			if err != nil {
				if errors.Is(err, registry.ErrFull) {
					writeError(w, http.StatusServiceUnavailable, codeRegistryFull)
					return
				}
				writeError(w, http.StatusBadRequest, codeInvalidRequest)
				return
			}

			writeJSONStatus(w, http.StatusOK, peerResponse{
				PeerID:            peer.ID,
				State:             string(peer.State),
				LastSeen:          peer.LastSeen,
				HeartbeatInterval: reg.HeartbeatInterval().String(),
			})

		case http.MethodDelete:
			reg.Deregister(peerID)
			w.WriteHeader(http.StatusNoContent)

		default:
			w.Header().Set("Allow", "POST, DELETE")
			writeError(w, http.StatusMethodNotAllowed, codeInvalidRequest)
		}
	})
}

// Heartbeat keeps the calling peer online.
func Heartbeat(reg *registry.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerID := PeerIDFrom(r.Context())
		if peerID == "" {
			writeTokenError(w, http.StatusUnauthorized, codeInvalidToken)
			return
		}

		peer, err := reg.Heartbeat(peerID)
		if err != nil {
			// The one place a caller learns something about its own record, and it
			// is an instruction rather than a disclosure: register again.
			writeError(w, http.StatusConflict, codeRegistrationRequired)
			return
		}

		writeJSONStatus(w, http.StatusOK, peerResponse{
			PeerID:   peer.ID,
			State:    string(peer.State),
			LastSeen: peer.LastSeen,
		})
	})
}

// PeerLookup reports one peer to an authenticated caller.
func PeerLookup(reg *registry.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if PeerIDFrom(r.Context()) == "" {
			writeTokenError(w, http.StatusUnauthorized, codeInvalidToken)
			return
		}

		peer, held := reg.Lookup(r.PathValue("id"))
		if !held {
			// The same answer whether it never registered or has gone: the
			// difference would say who used to be here.
			writeError(w, http.StatusNotFound, codeNotFound)
			return
		}

		writeJSONStatus(w, http.StatusOK, peerResponse{
			PeerID:   peer.ID,
			State:    string(peer.State),
			LastSeen: peer.LastSeen,
		})
	})
}

// writeJSONStatus writes a body with an explicit status.
func writeJSONStatus(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	writeJSON(w, body)
}
