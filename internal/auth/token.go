package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// SigningMethod is the one algorithm this service issues and the only one it
// verifies. Pinning it is what closes the substitution attack where a token
// arrives asking to be verified with a symmetric algorithm keyed by the public
// value everybody already has.
var SigningMethod = jwt.SigningMethodEdDSA

// Errors a caller is expected to tell apart. A token that expired means
// authenticate again with the same credentials; a token that never verified means
// the peer was never allowed.
var (
	ErrTokenExpired = errors.New("auth: token expired")
	ErrInvalidToken = errors.New("auth: invalid token")
)

// Token is what POST /auth answers with.
type Token struct {
	// Value is the signed token itself.
	Value string
	// PeerID is the peer identifier it acts as.
	PeerID string
	// ExpiresAt is the moment it stops being accepted.
	ExpiresAt time.Time
}

// Claims are the parts of a verified token the control plane reads.
type Claims struct {
	// PeerID is the subject: the peer this token acts as.
	PeerID string
	// ID is the token's own identifier, safe to write into a log where the token
	// itself is not.
	ID string
	// ExpiresAt is when it stops being accepted.
	ExpiresAt time.Time
}

// TokenIssuer mints tokens. Only the central server holds one.
type TokenIssuer struct {
	key    ed25519.PrivateKey
	issuer string
	ttl    time.Duration
	// now is the clock, injected so a test can hold it still.
	now func() time.Time
}

// NewTokenIssuer refuses anything that would produce a token nobody should
// accept: no key, a key of the wrong length, no issuer to name, or a lifetime
// that has already passed.
func NewTokenIssuer(key ed25519.PrivateKey, issuer string, ttl time.Duration) (*TokenIssuer, error) {
	switch {
	case len(key) != ed25519.PrivateKeySize:
		return nil, fmt.Errorf("auth: signing key is %d bytes, want %d", len(key), ed25519.PrivateKeySize)
	case strings.TrimSpace(issuer) == "":
		return nil, errors.New("auth: no issuer to name in the tokens")
	case ttl <= 0:
		return nil, fmt.Errorf("auth: token lifetime %s is not positive", ttl)
	}

	return &TokenIssuer{key: key, issuer: strings.TrimSpace(issuer), ttl: ttl, now: time.Now}, nil
}

// Issue mints a token for one peer identifier.
func (i *TokenIssuer) Issue(peerID string) (Token, error) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return Token{}, errors.New("auth: no peer identifier to issue for")
	}

	id, err := newTokenID()
	if err != nil {
		return Token{}, err
	}

	issued := i.now()
	expires := issued.Add(i.ttl)

	token := jwt.NewWithClaims(SigningMethod, jwt.RegisteredClaims{
		Subject:   peerID,
		Issuer:    i.issuer,
		ID:        id,
		IssuedAt:  jwt.NewNumericDate(issued),
		ExpiresAt: jwt.NewNumericDate(expires),
	})

	signed, err := token.SignedString(i.key)
	if err != nil {
		return Token{}, fmt.Errorf("auth: signing the token: %w", err)
	}

	return Token{Value: signed, PeerID: peerID, ExpiresAt: expires}, nil
}

// PublicKey is the half that verifies these tokens, and the half that is
// published — see adr:0006-sign-tokens-with-ed25519-and-publish-the-public-key.
func (i *TokenIssuer) PublicKey() ed25519.PublicKey {
	return i.key.Public().(ed25519.PublicKey)
}

// TokenVerifier decides whether a token was issued here and is still good. It
// holds only the public key, so a node that verifies cannot mint.
type TokenVerifier struct {
	key    ed25519.PublicKey
	issuer string
	now    func() time.Time
}

// NewTokenVerifier builds a verifier over the published key.
func NewTokenVerifier(key ed25519.PublicKey, issuer string) *TokenVerifier {
	return &TokenVerifier{key: key, issuer: strings.TrimSpace(issuer), now: time.Now}
}

// Verify checks the signature, the issuer, and the expiry, and reports an expired
// token distinctly from one that was never valid.
//
// The key function ignores what the token asks for and returns the one key this
// verifier has; the method is pinned separately, so a token naming another
// algorithm is refused before its signature is examined.
func (v *TokenVerifier) Verify(raw string) (Claims, error) {
	if len(v.key) != ed25519.PublicKeySize {
		return Claims{}, errors.New("auth: no public key to verify with")
	}

	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(raw, &claims,
		func(*jwt.Token) (any, error) { return v.key, nil },
		jwt.WithValidMethods([]string{SigningMethod.Alg()}),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return Claims{}, ErrTokenExpired
		}
		return Claims{}, ErrInvalidToken
	}

	if strings.TrimSpace(claims.Subject) == "" {
		return Claims{}, ErrInvalidToken
	}
	if claims.ExpiresAt == nil {
		return Claims{}, ErrInvalidToken
	}

	return Claims{
		PeerID:    claims.Subject,
		ID:        claims.ID,
		ExpiresAt: claims.ExpiresAt.Time,
	}, nil
}

// newTokenID is the token's own identifier: random, so that two tokens issued in
// the same instant are still distinguishable in a log.
func newTokenID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("auth: generating a token identifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}
