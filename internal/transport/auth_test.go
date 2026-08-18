package transport

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/auth"
)

const testSecret = "development-secret-at-least-32-chars"

// authFixture is a working auth surface: one client, a real key, and a limiter
// with room to spare.
type authFixture struct {
	handler  http.Handler
	issuer   *auth.TokenIssuer
	verifier *auth.TokenVerifier
	limiter  *auth.AttemptLimiter
	logs     *bytes.Buffer
}

func newAuthFixture(t *testing.T, allowance int) authFixture {
	t.Helper()

	clients, err := auth.NewClientStore([]auth.ClientConfig{
		{ID: "sdk-web", PeerID: "peer-001", Secret: testSecret},
	})
	if err != nil {
		t.Fatalf("NewClientStore() error = %v, want nil", err)
	}

	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	issuer, err := auth.NewTokenIssuer(key, "test-issuer", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v, want nil", err)
	}

	limiter, err := auth.NewAttemptLimiter(allowance, time.Minute)
	if err != nil {
		t.Fatalf("NewAttemptLimiter() error = %v, want nil", err)
	}

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	return authFixture{
		handler:  Auth(AuthDeps{Clients: clients, Issuer: issuer, Limiter: limiter, Logger: logger}),
		issuer:   issuer,
		verifier: auth.NewTokenVerifier(issuer.PublicKey(), "test-issuer"),
		limiter:  limiter,
		logs:     logs,
	}
}

// post sends a credential pair and returns the response.
func (f authFixture) post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func credentials(id, secret string) string {
	body, _ := json.Marshal(credentialRequest{ClientID: id, ClientSecret: secret})
	return string(body)
}

func TestAuthIssuesATokenForAKnownClient(t *testing.T) {
	f := newAuthFixture(t, 10)

	rec := f.post(t, credentials("sdk-web", testSecret))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /auth = %d, want %d — body %s", rec.Code, http.StatusOK, rec.Body)
	}

	var body tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body.PeerID != "peer-001" {
		t.Errorf("peer_id = %q, want %q", body.PeerID, "peer-001")
	}
	if body.ExpiresAt.IsZero() {
		t.Error("expires_at is zero, want the moment the token stops working")
	}

	claims, err := f.verifier.Verify(body.Token)
	if err != nil {
		t.Fatalf("the issued token does not verify: %v", err)
	}
	if claims.PeerID != "peer-001" {
		t.Errorf("token names %q, want %q", claims.PeerID, "peer-001")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on a response carrying a credential", got)
	}
}

func TestAuthRefusesWithOneShape(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{name: "an unknown client", body: credentials("nobody", testSecret), status: http.StatusUnauthorized, code: codeInvalidClient},
		{name: "the wrong secret", body: credentials("sdk-web", "wrong-secret-of-a-plausible-length!!"), status: http.StatusUnauthorized, code: codeInvalidClient},
		{name: "no client identifier", body: credentials("", testSecret), status: http.StatusBadRequest, code: codeInvalidRequest},
		{name: "no secret", body: credentials("sdk-web", ""), status: http.StatusBadRequest, code: codeInvalidRequest},
		{name: "a body that is not JSON", body: "sdk-web:secret", status: http.StatusBadRequest, code: codeInvalidRequest},
		{name: "an empty body", body: "", status: http.StatusBadRequest, code: codeInvalidRequest},
		{name: "fields nobody asked for", body: `{"client_id":"sdk-web","client_secret":"x","admin":true}`, status: http.StatusBadRequest, code: codeInvalidRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAuthFixture(t, 10)

			rec := f.post(t, c.body)
			if rec.Code != c.status {
				t.Errorf("status = %d, want %d", rec.Code, c.status)
			}

			var body errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
			}
			if body.Error != c.code {
				t.Errorf("error = %q, want %q", body.Error, c.code)
			}
		})
	}
}

func TestAuthDoesNotSayWhichHalfWasWrong(t *testing.T) {
	f := newAuthFixture(t, 10)

	unknown := f.post(t, credentials("nobody", testSecret))
	wrong := f.post(t, credentials("sdk-web", "wrong-secret-of-a-plausible-length!!"))

	if unknown.Code != wrong.Code || unknown.Body.String() != wrong.Body.String() {
		t.Errorf("an unknown client answers %d %s and a wrong secret answers %d %s; they must be indistinguishable",
			unknown.Code, unknown.Body.String(), wrong.Code, wrong.Body.String())
	}
}

func TestAuthRefusesPastTheAllowance(t *testing.T) {
	f := newAuthFixture(t, 2)

	for range 2 {
		if rec := f.post(t, credentials("sdk-web", "wrong-secret-of-a-plausible-length!!")); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d while the allowance lasts", rec.Code, http.StatusUnauthorized)
		}
	}

	rec := f.post(t, credentials("sdk-web", testSecret))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d — the right secret must not lift the limit", rec.Code, http.StatusTooManyRequests)
	}

	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body.Error != codeTooManyAttempts {
		t.Errorf("error = %q, want %q", body.Error, codeTooManyAttempts)
	}
}

func TestAuthClearsTheCountOnSuccess(t *testing.T) {
	f := newAuthFixture(t, 2)

	f.post(t, credentials("sdk-web", "wrong-secret-of-a-plausible-length!!"))
	if rec := f.post(t, credentials("sdk-web", testSecret)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	// The earlier failure was cleared, so the allowance is whole again.
	f.post(t, credentials("sdk-web", "wrong-secret-of-a-plausible-length!!"))
	if rec := f.post(t, credentials("sdk-web", testSecret)); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d — a success clears what was counted", rec.Code, http.StatusOK)
	}
}

func TestAuthRefusesABodyTooLargeToBeACredential(t *testing.T) {
	f := newAuthFixture(t, 10)

	huge := credentials("sdk-web", strings.Repeat("x", maxCredentialBody*2))
	rec := f.post(t, huge)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for a body past the limit", rec.Code, http.StatusBadRequest)
	}
}

func TestAuthKeepsCredentialsOutOfTheLogs(t *testing.T) {
	f := newAuthFixture(t, 10)

	f.post(t, credentials("sdk-web", testSecret))
	f.post(t, credentials("sdk-web", "wrong-secret-of-a-plausible-length!!"))

	logs := f.logs.String()
	for _, secret := range []string{testSecret, "wrong-secret-of-a-plausible-length!!"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the logs contain a secret that was presented: %q", logs)
		}
	}
}

func TestAuthKeepsTheTokenOutOfTheLogs(t *testing.T) {
	f := newAuthFixture(t, 10)

	rec := f.post(t, credentials("sdk-web", testSecret))
	var body tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}

	if strings.Contains(f.logs.String(), body.Token) {
		t.Errorf("the logs contain the issued token: %q", f.logs.String())
	}
}

func TestJWKSPublishesThePublicKeyAndNothingElse(t *testing.T) {
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	rec := httptest.NewRecorder()
	JWKS(public).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if len(set.Keys) != 1 {
		t.Fatalf("keys = %d, want exactly one", len(set.Keys))
	}

	published := set.Keys[0]
	if published["kty"] != "OKP" || published["crv"] != "Ed25519" || published["alg"] != "EdDSA" {
		t.Errorf("key = %v, want an Ed25519 OKP key", published)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(published["x"])
	if err != nil {
		t.Fatalf("x is not base64url: %v", err)
	}
	if !bytes.Equal(decoded, public) {
		t.Error("x is not the public key that was published")
	}

	// The private half must not be anywhere in the response.
	if bytes.Contains(rec.Body.Bytes(), []byte(base64.RawURLEncoding.EncodeToString(key.Seed()))) {
		t.Error("the response contains the signing key's seed")
	}
	if published["kid"] == "" {
		t.Error("kid is empty, want a name a client can compare keys by")
	}
}

func TestKeyIDNamesTheKeyWithoutRevealingIt(t *testing.T) {
	first, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	second, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	if KeyID(first) == KeyID(second) {
		t.Error("two keys have the same identifier")
	}
	if strings.Contains(KeyID(first), base64.RawURLEncoding.EncodeToString(first)) {
		t.Error("the identifier contains the key it names")
	}
}

// protected is a handler that reports the peer identifier it was given.
func protected() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, PeerIDFrom(r.Context()))
	})
}

func TestRequireTokenPassesThePeerIdentifierOn(t *testing.T) {
	f := newAuthFixture(t, 10)

	token, err := f.issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/peers", nil)
	req.Header.Set("Authorization", "Bearer "+token.Value)
	rec := httptest.NewRecorder()

	RequireToken(f.verifier)(protected()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "peer-001" {
		t.Errorf("handler saw %q, want %q", rec.Body.String(), "peer-001")
	}
}

func TestRequireTokenIgnoresAnIdentityClaimedElsewhere(t *testing.T) {
	f := newAuthFixture(t, 10)

	token, err := f.issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/peers?peer_id=peer-999", nil)
	req.Header.Set("Authorization", "Bearer "+token.Value)
	req.Header.Set("X-Peer-Id", "peer-999")
	rec := httptest.NewRecorder()

	RequireToken(f.verifier)(protected()).ServeHTTP(rec, req)

	if rec.Body.String() != "peer-001" {
		t.Errorf("handler saw %q, want the identifier from the token and not the one in the request", rec.Body.String())
	}
}

func TestRequireTokenRefuses(t *testing.T) {
	f := newAuthFixture(t, 10)

	expiredIssuer, err := auth.NewTokenIssuer(mustKey(t), "test-issuer", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v", err)
	}
	foreign, err := expiredIssuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	cases := []struct {
		name   string
		header string
		code   string
	}{
		{name: "no Authorization header", header: "", code: codeInvalidToken},
		{name: "a scheme that is not Bearer", header: "Basic abcdef", code: codeInvalidToken},
		{name: "Bearer with nothing after it", header: "Bearer ", code: codeInvalidToken},
		{name: "a token this server did not sign", header: "Bearer " + foreign.Value, code: codeInvalidToken},
		{name: "something that is not a token", header: "Bearer not-a-token", code: codeInvalidToken},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/peers", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()

			RequireToken(f.verifier)(protected()).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			var body errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
			}
			if body.Error != c.code {
				t.Errorf("error = %q, want %q", body.Error, c.code)
			}
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("no WWW-Authenticate header, want one naming what should have been presented")
			}
		})
	}
}

func TestRequireTokenTellsExpiredApartFromInvalid(t *testing.T) {
	clients, err := auth.NewClientStore([]auth.ClientConfig{
		{ID: "sdk-web", PeerID: "peer-001", Secret: testSecret},
	})
	if err != nil {
		t.Fatalf("NewClientStore() error = %v", err)
	}
	_ = clients

	key := mustKey(t)
	issuer, err := auth.NewTokenIssuer(key, "test-issuer", time.Millisecond)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v", err)
	}
	token, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	verifier := auth.NewTokenVerifier(issuer.PublicKey(), "test-issuer")
	time.Sleep(5 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/peers", nil)
	req.Header.Set("Authorization", "Bearer "+token.Value)
	rec := httptest.NewRecorder()

	RequireToken(verifier)(protected()).ServeHTTP(rec, req)

	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body.Error != codeTokenExpired {
		t.Errorf("error = %q, want %q — an expired token means authenticate again", body.Error, codeTokenExpired)
	}
}

func TestPeerIDFromAnUnauthenticatedRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/peers", nil)

	if got := PeerIDFrom(req.Context()); got != "" {
		t.Errorf("PeerIDFrom() = %q, want an empty string", got)
	}
}

func TestPeerIDAndRequestIDDoNotCollide(t *testing.T) {
	f := newAuthFixture(t, 10)

	token, err := f.issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	var sawPeer, sawRequest string
	handler := Chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawPeer = PeerIDFrom(r.Context())
		sawRequest = RequestIDFrom(r.Context())
	}), WithRequestID, RequireToken(f.verifier))

	req := httptest.NewRequest(http.MethodGet, "/peers", nil)
	req.Header.Set("Authorization", "Bearer "+token.Value)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if sawPeer != "peer-001" {
		t.Errorf("peer identifier = %q, want %q", sawPeer, "peer-001")
	}
	if sawRequest == "" || sawRequest == sawPeer {
		t.Errorf("request identifier = %q, peer identifier = %q — two context keys have collided", sawRequest, sawPeer)
	}
}

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()

	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	return key
}
