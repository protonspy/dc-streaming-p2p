package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/auth"
)

// maxCredentialBody bounds what POST /auth will read. A credential pair is a few
// hundred bytes; anything larger is not one.
const maxCredentialBody = 4 << 10

// The error codes this surface answers with. Every credential failure is
// invalid_client, whatever was actually wrong with it — see R1.4. The two token
// codes are deliberately distinct, because one means authenticate again and the
// other means the peer was never allowed.
const (
	codeInvalidClient   = "invalid_client"
	codeInvalidRequest  = "invalid_request"
	codeTooManyAttempts = "too_many_attempts"
	codeInvalidToken    = "invalid_token"
	codeTokenExpired    = "token_expired"
)

// AuthDeps is what POST /auth needs.
type AuthDeps struct {
	// Clients answers whether a credential is one of the configured ones.
	Clients *auth.ClientStore
	// Issuer mints the token that is answered with.
	Issuer *auth.TokenIssuer
	// Limiter refuses an identifier that has spent its allowance.
	Limiter *auth.AttemptLimiter
	// Logger records failures without recording what was presented.
	Logger *slog.Logger
}

// credentialRequest is what a client sends.
type credentialRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// tokenResponse is what it gets back.
type tokenResponse struct {
	Token     string    `json:"token"`
	PeerID    string    `json:"peer_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// errorResponse is the one shape every refusal takes.
type errorResponse struct {
	Error string `json:"error"`
}

// Auth serves POST /auth: a credential pair in, a short-lived token out.
func Auth(deps AuthDeps) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var credentials credentialRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCredentialBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&credentials); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest)
			return
		}

		clientID := strings.TrimSpace(credentials.ClientID)
		if clientID == "" || credentials.ClientSecret == "" {
			writeError(w, http.StatusBadRequest, codeInvalidRequest)
			return
		}

		if !deps.Limiter.Allowed(clientID) {
			logger.WarnContext(r.Context(), "authentication refused, allowance spent",
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("client_id", clientID),
			)
			writeError(w, http.StatusTooManyRequests, codeTooManyAttempts)
			return
		}

		peerID, err := deps.Clients.Authenticate(clientID, credentials.ClientSecret)
		if err != nil {
			deps.Limiter.Failed(clientID)
			logger.InfoContext(r.Context(), "authentication failed",
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("client_id", clientID),
			)
			writeError(w, http.StatusUnauthorized, codeInvalidClient)
			return
		}

		token, err := deps.Issuer.Issue(peerID)
		if err != nil {
			logger.ErrorContext(r.Context(), "issuing a token failed",
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("peer_id", peerID),
				slog.String("error", err.Error()),
			)
			writeError(w, http.StatusInternalServerError, codeInvalidRequest)
			return
		}

		deps.Limiter.Succeeded(clientID)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, tokenResponse{
			Token:     token.Value,
			PeerID:    token.PeerID,
			ExpiresAt: token.ExpiresAt,
		})
	})
}

// JWKS publishes the public key that verifies this server's tokens. It is
// unauthenticated on purpose: the key is not a secret, and a client pins it before
// it has any credential to present —
// adr:0006-sign-tokens-with-ed25519-and-publish-the-public-key.
func JWKS(public ed25519.PublicKey) http.Handler {
	type jwk struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	type keySet struct {
		Keys []jwk `json:"keys"`
	}

	// Built once: the key does not change while the process runs, and a rotation
	// is a restart.
	body := keySet{Keys: []jwk{{
		Kty: "OKP",
		Crv: "Ed25519",
		X:   base64.RawURLEncoding.EncodeToString(public),
		Use: "sig",
		Alg: auth.SigningMethod.Alg(),
		Kid: KeyID(public),
	}}}

	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/jwk-set+json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		writeJSON(w, body)
	})
}

// KeyID names a public key the way a client can compare two of them without
// holding either: the hash, not the key.
func KeyID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

const peerIDKey contextKey = iota + 1

// PeerIDFrom returns the peer identifier the token on this request named, or ""
// when the request was never authenticated.
func PeerIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(peerIDKey).(string)
	return id
}

// RequireToken refuses a request that does not carry a token this server signed,
// and puts the peer identifier the token names into the request context. Every
// handler behind it takes the identity from there and from nowhere else, so a
// request cannot claim to be a peer it did not authenticate as.
func RequireToken(verifier *auth.TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				writeTokenError(w, http.StatusUnauthorized, codeInvalidToken)
				return
			}

			claims, err := verifier.Verify(raw)
			if err != nil {
				code := codeInvalidToken
				if errors.Is(err, auth.ErrTokenExpired) {
					code = codeTokenExpired
				}
				writeTokenError(w, http.StatusUnauthorized, code)
				return
			}

			ctx := context.WithValue(r.Context(), peerIDKey, claims.PeerID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken pulls the token out of the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

// writeError answers with the one refusal shape.
func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	writeJSON(w, errorResponse{Error: code})
}

// writeJSON encodes a body that is already known to be encodable. The status line
// is sent by the time this can fail, so there is nothing left to tell the caller
// and the request log carries the failure.
func writeJSON(w http.ResponseWriter, body any) {
	_ = json.NewEncoder(w).Encode(body)
}

// writeTokenError is writeError plus the header that tells a client what it should
// have presented.
func writeTokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("WWW-Authenticate", `Bearer error="`+code+`"`)
	writeError(w, status, code)
}
