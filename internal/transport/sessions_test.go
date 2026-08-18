package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/session"
)

// presentPeers is the registry as the session store sees it.
type presentPeers map[string]bool

func (p presentPeers) Lookup(peerID string) bool { return p[peerID] }

// sessionFixture is the session surface behind the real token middleware.
type sessionFixture struct {
	store *session.Store
	clock *time.Time
	mux   *http.ServeMux
	auth  authFixture
}

func newSessionFixture(t *testing.T, opts session.Options) sessionFixture {
	t.Helper()

	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if opts.NegotiationTimeout == 0 {
		opts.NegotiationTimeout = 2 * time.Minute
	}
	if opts.Retention == 0 {
		opts.Retention = 10 * time.Minute
	}
	if opts.Peers == nil {
		opts.Peers = presentPeers{"peer-001": true, "peer-002": true, "peer-003": true}
	}
	opts.Now = func() time.Time { return clock }

	store, err := session.New(opts)
	if err != nil {
		t.Fatalf("session.New() error = %v, want nil", err)
	}

	auth := newAuthFixture(t, 10)
	guard := RequireToken(auth.verifier)

	mux := http.NewServeMux()
	mux.Handle("POST /sessions", guard(OpenSession(store)))
	mux.Handle("GET /sessions/{id}", guard(GetSession(store)))
	mux.Handle("DELETE /sessions/{id}", guard(GetSession(store)))
	mux.Handle("POST /sessions/{id}/state", guard(ReportSession(store)))

	return sessionFixture{store: store, clock: &clock, mux: mux, auth: auth}
}

func (f sessionFixture) do(t *testing.T, method, path, peerID, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if peerID != "" {
		token, err := f.auth.issuer.Issue(peerID)
		if err != nil {
			t.Fatalf("Issue() error = %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.Value)
	}

	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

// open is a session between two peers, through the HTTP surface.
func (f sessionFixture) open(t *testing.T, caller, callee string) sessionResponse {
	t.Helper()

	rec := f.do(t, http.MethodPost, "/sessions", caller, `{"peer_id":"`+callee+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /sessions = %d, want %d — body %s", rec.Code, http.StatusCreated, rec.Body)
	}
	return decodeSession(t, rec)
}

func decodeSession(t *testing.T, rec *httptest.ResponseRecorder) sessionResponse {
	t.Helper()

	var body sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return body
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return body.Error
}

func TestOpenSessionNamesTheCallerFromTheToken(t *testing.T) {
	f := newSessionFixture(t, session.Options{})

	opened := f.open(t, "peer-001", "peer-002")

	if opened.Caller != "peer-001" || opened.Callee != "peer-002" {
		t.Errorf("session = %+v, want peer-001 calling peer-002", opened)
	}
	if opened.State != string(session.Negotiating) {
		t.Errorf("state = %q, want %q", opened.State, session.Negotiating)
	}
	if opened.SessionID == "" {
		t.Error("session_id is empty")
	}
}

func TestOpenSessionIgnoresACallerInTheBody(t *testing.T) {
	f := newSessionFixture(t, session.Options{})

	// caller is a field nobody asked for, so the request is refused outright
	// rather than quietly honoured in part.
	rec := f.do(t, http.MethodPost, "/sessions", "peer-001", `{"peer_id":"peer-002","caller":"peer-003"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /sessions = %d, want %d for a body carrying fields nobody asked for", rec.Code, http.StatusBadRequest)
	}
}

func TestOpenSessionRefuses(t *testing.T) {
	cases := []struct {
		name   string
		caller string
		body   string
		status int
		code   string
	}{
		{name: "a peer calling itself", caller: "peer-001", body: `{"peer_id":"peer-001"}`, status: http.StatusBadRequest, code: codeCannotCallSelf},
		{name: "a peer nobody registered", caller: "peer-001", body: `{"peer_id":"peer-404"}`, status: http.StatusNotFound, code: codeNotReachable},
		{name: "no peer named", caller: "peer-001", body: `{}`, status: http.StatusBadRequest, code: codeInvalidRequest},
		{name: "a body that is not JSON", caller: "peer-001", body: `peer-002`, status: http.StatusBadRequest, code: codeInvalidRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})

			rec := f.do(t, http.MethodPost, "/sessions", c.caller, c.body)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d — body %s", rec.Code, c.status, rec.Body)
			}
			if got := decodeError(t, rec); got != c.code {
				t.Errorf("error = %q, want %q", got, c.code)
			}
		})
	}
}

// refuseAll is a policy that allows nothing.
type refuseAll struct{}

func (refuseAll) Allows(_, _ string) bool { return false }

func TestAPolicyRefusalLooksLikeAPeerThatIsNotThere(t *testing.T) {
	refused := newSessionFixture(t, session.Options{Authorizer: refuseAll{}})
	absent := newSessionFixture(t, session.Options{})

	byPolicy := refused.do(t, http.MethodPost, "/sessions", "peer-001", `{"peer_id":"peer-002"}`)
	byAbsence := absent.do(t, http.MethodPost, "/sessions", "peer-001", `{"peer_id":"peer-404"}`)

	if byPolicy.Code != byAbsence.Code || byPolicy.Body.String() != byAbsence.Body.String() {
		t.Errorf("a refusal by policy answers %d %s and a peer that does not exist answers %d %s; a caller must not tell them apart",
			byPolicy.Code, byPolicy.Body.String(), byAbsence.Code, byAbsence.Body.String())
	}
}

func TestOpeningTwiceAnswersTheSameSession(t *testing.T) {
	f := newSessionFixture(t, session.Options{})

	first := f.open(t, "peer-001", "peer-002")

	// The other peer asks for the same pair, and gets the session that exists.
	rec := f.do(t, http.MethodPost, "/sessions", "peer-002", `{"peer_id":"peer-001"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /sessions = %d, want %d", rec.Code, http.StatusCreated)
	}
	if second := decodeSession(t, rec); second.SessionID != first.SessionID {
		t.Errorf("session_id = %q, want the existing %q", second.SessionID, first.SessionID)
	}
}

func TestGetSessionIsForItsParticipantsOnly(t *testing.T) {
	f := newSessionFixture(t, session.Options{})

	opened := f.open(t, "peer-001", "peer-002")

	for _, peerID := range []string{"peer-001", "peer-002"} {
		rec := f.do(t, http.MethodGet, "/sessions/"+opened.SessionID, peerID, "")
		if rec.Code != http.StatusOK {
			t.Errorf("GET as %q = %d, want %d", peerID, rec.Code, http.StatusOK)
		}
	}

	outsider := f.do(t, http.MethodGet, "/sessions/"+opened.SessionID, "peer-003", "")
	absent := f.do(t, http.MethodGet, "/sessions/no-such-session", "peer-003", "")

	if outsider.Code != http.StatusNotFound {
		t.Fatalf("GET as an outsider = %d, want %d", outsider.Code, http.StatusNotFound)
	}
	if outsider.Body.String() != absent.Body.String() {
		t.Errorf("a session that exists answers %s to an outsider and a missing one answers %s; a session identifier is not a capability",
			outsider.Body.String(), absent.Body.String())
	}
}

func TestReportConnectedRecordsThePath(t *testing.T) {
	f := newSessionFixture(t, session.Options{})

	opened := f.open(t, "peer-001", "peer-002")

	rec := f.do(t, http.MethodPost, "/sessions/"+opened.SessionID+"/state", "peer-002",
		`{"state":"connected","path":"relayed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST state = %d, want %d — body %s", rec.Code, http.StatusOK, rec.Body)
	}

	reported := decodeSession(t, rec)
	if reported.State != string(session.Connected) || reported.Path != string(session.Relayed) {
		t.Errorf("session = %+v, want connected over the relay", reported)
	}
}

func TestReportRefuses(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{name: "a state nobody defined", body: `{"state":"dancing"}`, status: http.StatusBadRequest, code: codeInvalidRequest},
		{name: "a path nobody defined", body: `{"state":"connected","path":"carrier-pigeon"}`, status: http.StatusBadRequest, code: codeInvalidRequest},
		{name: "negotiating, which is not something to report", body: `{"state":"negotiating"}`, status: http.StatusBadRequest, code: codeInvalidRequest},
		{name: "a body that is not JSON", body: `connected`, status: http.StatusBadRequest, code: codeInvalidRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			opened := f.open(t, "peer-001", "peer-002")

			rec := f.do(t, http.MethodPost, "/sessions/"+opened.SessionID+"/state", "peer-001", c.body)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d — body %s", rec.Code, c.status, rec.Body)
			}
			if got := decodeError(t, rec); got != c.code {
				t.Errorf("error = %q, want %q", got, c.code)
			}
		})
	}
}

func TestReportFromAPeerOutsideTheSession(t *testing.T) {
	f := newSessionFixture(t, session.Options{})

	opened := f.open(t, "peer-001", "peer-002")

	rec := f.do(t, http.MethodPost, "/sessions/"+opened.SessionID+"/state", "peer-003", `{"state":"closed"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d — an outsider must not move a session", rec.Code, http.StatusNotFound)
	}
}

func TestReportAMoveTheSessionCannotMake(t *testing.T) {
	f := newSessionFixture(t, session.Options{})

	opened := f.open(t, "peer-001", "peer-002")
	f.do(t, http.MethodPost, "/sessions/"+opened.SessionID+"/state", "peer-001", `{"state":"closed"}`)

	rec := f.do(t, http.MethodPost, "/sessions/"+opened.SessionID+"/state", "peer-001", `{"state":"connected","path":"direct"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d for a closed session", rec.Code, http.StatusConflict)
	}
	if got := decodeError(t, rec); got != codeBadTransition {
		t.Errorf("error = %q, want %q", got, codeBadTransition)
	}
}

func TestDeleteClosesTheSession(t *testing.T) {
	f := newSessionFixture(t, session.Options{})

	opened := f.open(t, "peer-001", "peer-002")

	rec := f.do(t, http.MethodDelete, "/sessions/"+opened.SessionID, "peer-002", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, want %d — body %s", rec.Code, http.StatusOK, rec.Body)
	}
	if closed := decodeSession(t, rec); closed.State != string(session.Closed) {
		t.Errorf("state = %q, want %q", closed.State, session.Closed)
	}
}

func TestDeleteASessionThatAlreadyEnded(t *testing.T) {
	f := newSessionFixture(t, session.Options{})

	opened := f.open(t, "peer-001", "peer-002")
	f.do(t, http.MethodDelete, "/sessions/"+opened.SessionID, "peer-001", "")

	rec := f.do(t, http.MethodDelete, "/sessions/"+opened.SessionID, "peer-001", "")
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d — it is already not open", rec.Code, http.StatusConflict)
	}
}

func TestSessionRoutesRefuseAnUnauthenticatedCaller(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	opened := f.open(t, "peer-001", "peer-002")

	cases := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/sessions"},
		{method: http.MethodGet, path: "/sessions/" + opened.SessionID},
		{method: http.MethodDelete, path: "/sessions/" + opened.SessionID},
		{method: http.MethodPost, path: "/sessions/" + opened.SessionID + "/state"},
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

func TestASessionThatTimedOutReadsAsFailed(t *testing.T) {
	f := newSessionFixture(t, session.Options{NegotiationTimeout: time.Minute})

	opened := f.open(t, "peer-001", "peer-002")
	*f.clock = f.clock.Add(90 * time.Second)

	rec := f.do(t, http.MethodGet, "/sessions/"+opened.SessionID, "peer-001", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := decodeSession(t, rec); got.State != string(session.Failed) {
		t.Errorf("state = %q, want %q past the negotiation timeout", got.State, session.Failed)
	}
}

func TestOpenAnswersWhenTheStoreIsFull(t *testing.T) {
	f := newSessionFixture(t, session.Options{MaxSessions: 1})

	f.open(t, "peer-001", "peer-002")

	rec := f.do(t, http.MethodPost, "/sessions", "peer-003", `{"peer_id":"peer-001"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := decodeError(t, rec); got != codeSessionsFull {
		t.Errorf("error = %q, want %q", got, codeSessionsFull)
	}
}

func TestGetSessionRefusesAMethodItDoesNotServe(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	opened := f.open(t, "peer-001", "peer-002")

	token, err := f.auth.issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/sessions/"+opened.SessionID, nil)
	req.Header.Set("Authorization", "Bearer "+token.Value)
	rec := httptest.NewRecorder()
	RequireToken(f.auth.verifier)(GetSession(f.store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
