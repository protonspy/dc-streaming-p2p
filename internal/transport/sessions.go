package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/session"
)

// maxSessionBody bounds what the session routes will read. Their bodies are a peer
// identifier or a state name.
const maxSessionBody = 4 << 10

// Error codes the session surface answers with.
const (
	codeNotReachable   = "peer_not_reachable"
	codeSessionsFull   = "too_many_sessions"
	codePeerSessions   = "too_many_sessions_for_you"
	codeBadTransition  = "not_a_valid_transition"
	codeCannotCallSelf = "cannot_call_yourself"
)

// openRequest names the peer being called. It is the one identifier that comes
// from a request: the caller is the token, the callee is what the caller asked for.
type openRequest struct {
	PeerID string `json:"peer_id"`
}

// reportRequest is what a peer says happened.
type reportRequest struct {
	State string `json:"state"`
	Path  string `json:"path,omitempty"`
}

// sessionResponse is one session as the control plane reports it.
type sessionResponse struct {
	SessionID string    `json:"session_id"`
	Caller    string    `json:"caller"`
	Callee    string    `json:"callee"`
	State     string    `json:"state"`
	Path      string    `json:"path,omitempty"`
	OpenedAt  time.Time `json:"opened_at"`
	ChangedAt time.Time `json:"changed_at"`
}

func asResponse(s session.Session) sessionResponse {
	return sessionResponse{
		SessionID: s.ID,
		Caller:    s.Caller,
		Callee:    s.Callee,
		State:     string(s.State),
		Path:      string(s.Path),
		OpenedAt:  s.OpenedAt,
		ChangedAt: s.ChangedAt,
	}
}

// OpenSession serves POST /sessions.
func OpenSession(store *session.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller := PeerIDFrom(r.Context())
		if caller == "" {
			writeTokenError(w, http.StatusUnauthorized, codeInvalidToken)
			return
		}

		var body openRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSessionBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || body.PeerID == "" {
			writeError(w, http.StatusBadRequest, codeInvalidRequest)
			return
		}

		if body.PeerID == caller {
			writeError(w, http.StatusBadRequest, codeCannotCallSelf)
			return
		}

		// Reachability is checked before the session exists, and a peer that
		// cannot be reached answers exactly as one the policy refuses: a caller
		// must not be able to use either to learn who exists.
		if !store.Reachable(body.PeerID) {
			writeError(w, http.StatusNotFound, codeNotReachable)
			return
		}

		opened, err := store.Open(caller, body.PeerID)
		switch {
		case errors.Is(err, session.ErrNotAllowed):
			writeError(w, http.StatusNotFound, codeNotReachable)
			return
		case errors.Is(err, session.ErrSelf):
			writeError(w, http.StatusBadRequest, codeCannotCallSelf)
			return
		case errors.Is(err, session.ErrFull):
			writeError(w, http.StatusServiceUnavailable, codeSessionsFull)
			return
		case errors.Is(err, session.ErrTooManyForPeer):
			// Distinct from the deployment being full: this caller is at its own
			// limit and closing something of its own is what fixes it.
			writeError(w, http.StatusTooManyRequests, codePeerSessions)
			return
		case err != nil:
			writeError(w, http.StatusBadRequest, codeInvalidRequest)
			return
		}

		writeJSONStatus(w, http.StatusCreated, asResponse(opened))
	})
}

// GetSession serves GET /sessions/{id} and DELETE /sessions/{id}.
func GetSession(store *session.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerID := PeerIDFrom(r.Context())
		if peerID == "" {
			writeTokenError(w, http.StatusUnauthorized, codeInvalidToken)
			return
		}

		id := r.PathValue("id")

		switch r.Method {
		case http.MethodGet:
			found, err := store.Get(id, peerID)
			if err != nil {
				writeError(w, http.StatusNotFound, codeNotFound)
				return
			}
			writeJSONStatus(w, http.StatusOK, asResponse(found))

		case http.MethodDelete:
			closed, err := store.Report(id, peerID, session.Closed, session.PathUnknown)
			switch {
			case errors.Is(err, session.ErrNotFound):
				writeError(w, http.StatusNotFound, codeNotFound)
			case errors.Is(err, session.ErrBadTransition):
				// Already ended. The caller wanted it closed and it is not open.
				writeError(w, http.StatusConflict, codeBadTransition)
			case err != nil:
				writeError(w, http.StatusBadRequest, codeInvalidRequest)
			default:
				writeJSONStatus(w, http.StatusOK, asResponse(closed))
			}

		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeError(w, http.StatusMethodNotAllowed, codeInvalidRequest)
		}
	})
}

// ReportSession serves POST /sessions/{id}/state.
func ReportSession(store *session.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerID := PeerIDFrom(r.Context())
		if peerID == "" {
			writeTokenError(w, http.StatusUnauthorized, codeInvalidToken)
			return
		}

		var body reportRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSessionBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest)
			return
		}

		state, ok := reportableState(body.State)
		if !ok {
			writeError(w, http.StatusBadRequest, codeInvalidRequest)
			return
		}
		path, ok := reportablePath(body.Path)
		if !ok {
			writeError(w, http.StatusBadRequest, codeInvalidRequest)
			return
		}

		moved, err := store.Report(r.PathValue("id"), peerID, state, path)
		switch {
		case errors.Is(err, session.ErrNotFound):
			writeError(w, http.StatusNotFound, codeNotFound)
		case errors.Is(err, session.ErrBadTransition):
			writeError(w, http.StatusConflict, codeBadTransition)
		case err != nil:
			writeError(w, http.StatusBadRequest, codeInvalidRequest)
		default:
			writeJSONStatus(w, http.StatusOK, asResponse(moved))
		}
	})
}

// reportableState accepts only the states a peer may report. Negotiating is not
// one of them: it is where a session starts, not somewhere it returns to.
func reportableState(raw string) (session.State, bool) {
	switch session.State(raw) {
	case session.Connected:
		return session.Connected, true
	case session.Failed:
		return session.Failed, true
	case session.Closed:
		return session.Closed, true
	default:
		return "", false
	}
}

// reportablePath accepts the two paths a peer connection can end up on, or none.
func reportablePath(raw string) (session.Path, bool) {
	switch session.Path(raw) {
	case session.PathUnknown:
		return session.PathUnknown, true
	case session.Direct:
		return session.Direct, true
	case session.Relayed:
		return session.Relayed, true
	default:
		return "", false
	}
}
