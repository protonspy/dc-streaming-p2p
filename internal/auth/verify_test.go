package auth

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// verifierPair builds an issuer and a verifier over the same key, sharing one
// clock the test moves by hand.
func verifierPair(t *testing.T, ttl time.Duration) (*TokenIssuer, *TokenVerifier, *time.Time) {
	t.Helper()

	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	issuer, err := NewTokenIssuer(key, "test-issuer", ttl)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v, want nil", err)
	}
	issuer.now = now

	verifier := NewTokenVerifier(issuer.PublicKey(), "test-issuer")
	verifier.now = now

	return issuer, verifier, &clock
}

func TestVerifyAcceptsAFreshToken(t *testing.T) {
	issuer, verifier, _ := verifierPair(t, time.Hour)

	token, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	claims, err := verifier.Verify(token.Value)
	if err != nil {
		t.Fatalf("Verify() error = %v, want nil", err)
	}
	if claims.PeerID != "peer-001" {
		t.Errorf("PeerID = %q, want %q", claims.PeerID, "peer-001")
	}
	if claims.ID == "" {
		t.Error("ID = \"\", want the token's own identifier")
	}
	if !claims.ExpiresAt.Equal(token.ExpiresAt) {
		t.Errorf("ExpiresAt = %s, want %s", claims.ExpiresAt, token.ExpiresAt)
	}
}

func TestVerifyReportsAnExpiredTokenDistinctly(t *testing.T) {
	issuer, verifier, clock := verifierPair(t, time.Minute)

	token, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	*clock = clock.Add(2 * time.Minute)

	_, err = verifier.Verify(token.Value)
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("Verify() error = %v, want ErrTokenExpired — a peer has to tell this apart from never having been allowed", err)
	}
}

func TestVerifyAcceptsATokenUpToItsExpiry(t *testing.T) {
	issuer, verifier, clock := verifierPair(t, time.Minute)

	token, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	*clock = clock.Add(59 * time.Second)

	if _, err := verifier.Verify(token.Value); err != nil {
		t.Errorf("Verify() error = %v, want nil a second before expiry", err)
	}
}

func TestVerifyRefuses(t *testing.T) {
	issuer, verifier, _ := verifierPair(t, time.Hour)

	good, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	_, otherKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	otherIssuer, err := NewTokenIssuer(otherKey, "test-issuer", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v", err)
	}
	otherIssuer.now = issuer.now
	forged, err := otherIssuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	strangerIssuer, err := NewTokenIssuer(issuer.key, "somebody-else", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v", err)
	}
	strangerIssuer.now = issuer.now
	wrongIssuer, err := strangerIssuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	cases := []struct {
		name  string
		token string
	}{
		{name: "nothing at all", token: ""},
		{name: "something that is not a token", token: "not-a-token"},
		{name: "a token signed by another key", token: forged.Value},
		{name: "a token from another issuer", token: wrongIssuer.Value},
		{name: "a token with its signature cut off", token: good.Value[:len(good.Value)-8]},
		{name: "a token with its payload swapped", token: tamper(t, good.Value)},
		{name: "an unsigned token", token: unsignedToken(t)},
		{name: "a token signed with the public key as a shared secret", token: symmetricToken(t, issuer.PublicKey())},
		{name: "a token with no subject", token: subjectlessToken(t, issuer.key)},
		{name: "a token with no expiry", token: neverExpiringToken(t, issuer.key)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			claims, err := verifier.Verify(c.token)
			if !errors.Is(err, ErrInvalidToken) {
				t.Errorf("Verify() error = %v, want ErrInvalidToken", err)
			}
			if claims.PeerID != "" {
				t.Errorf("Verify() = %+v, want no claims alongside the refusal", claims)
			}
		})
	}
}

func TestVerifyWithoutAKey(t *testing.T) {
	verifier := NewTokenVerifier(nil, "test-issuer")

	if _, err := verifier.Verify("anything"); err == nil {
		t.Error("Verify() error = nil with no public key, want an error")
	}
}

// tamper swaps one character of the payload, leaving the signature in place.
func tamper(t *testing.T, token string) string {
	t.Helper()

	parts := []byte(token)
	for i, b := range parts {
		if b == '.' {
			// The first character after the first dot is inside the payload.
			if parts[i+1] == 'e' {
				parts[i+1] = 'f'
			} else {
				parts[i+1] = 'e'
			}
			return string(parts)
		}
	}
	t.Fatal("token has no payload to tamper with")
	return ""
}

// unsignedToken is the classic "alg: none" attack.
func unsignedToken(t *testing.T) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{
		Subject:   "peer-001",
		Issuer:    "test-issuer",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("signing an unsigned token: %v", err)
	}
	return signed
}

// symmetricToken signs with HMAC keyed by the published public key — the attack
// pinning the algorithm exists to stop.
func symmetricToken(t *testing.T, public ed25519.PublicKey) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "peer-001",
		Issuer:    "test-issuer",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	signed, err := token.SignedString([]byte(public))
	if err != nil {
		t.Fatalf("signing with the public key: %v", err)
	}
	return signed
}

// subjectlessToken is properly signed and names nobody.
func subjectlessToken(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()

	token := jwt.NewWithClaims(SigningMethod, jwt.RegisteredClaims{
		Issuer:    "test-issuer",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing a subjectless token: %v", err)
	}
	return signed
}

// neverExpiringToken is properly signed and carries no expiry at all.
func neverExpiringToken(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()

	token := jwt.NewWithClaims(SigningMethod, jwt.RegisteredClaims{
		Subject: "peer-001",
		Issuer:  "test-issuer",
	})
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing a token with no expiry: %v", err)
	}
	return signed
}
