package auth

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

// testIssuer builds an issuer over a deterministic key, with a clock the test
// holds still.
func testIssuer(t *testing.T, ttl time.Duration) (*TokenIssuer, *time.Time) {
	t.Helper()

	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	issuer, err := NewTokenIssuer(key, "test-issuer", ttl)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v, want nil", err)
	}
	issuer.now = func() time.Time { return clock }

	return issuer, &clock
}

func TestIssueNamesThePeerAndItsExpiry(t *testing.T) {
	issuer, clock := testIssuer(t, time.Hour)

	token, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	if token.PeerID != "peer-001" {
		t.Errorf("PeerID = %q, want %q", token.PeerID, "peer-001")
	}
	if want := clock.Add(time.Hour); !token.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %s, want %s", token.ExpiresAt, want)
	}
	if strings.Count(token.Value, ".") != 2 {
		t.Errorf("Value = %q, want three dot-separated parts", token.Value)
	}
}

func TestIssueRefusesAnEmptyPeerIdentifier(t *testing.T) {
	issuer, _ := testIssuer(t, time.Hour)

	if _, err := issuer.Issue(""); err == nil {
		t.Error("Issue(\"\") error = nil, want a refusal — a token naming nobody authorizes everybody")
	}
}

func TestIssueGivesEachTokenItsOwnIdentifier(t *testing.T) {
	issuer, _ := testIssuer(t, time.Hour)

	first, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}
	second, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	if first.Value == second.Value {
		t.Error("two tokens issued at the same instant are identical, so neither can be named in a log without naming both")
	}
}

func TestIssueSignsWithTheConfiguredKey(t *testing.T) {
	issuer, _ := testIssuer(t, time.Hour)

	token, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	verifier := NewTokenVerifier(issuer.PublicKey(), "test-issuer")
	verifier.now = issuer.now

	claims, err := verifier.Verify(token.Value)
	if err != nil {
		t.Fatalf("Verify() error = %v, want nil for a token this issuer signed", err)
	}
	if claims.PeerID != "peer-001" {
		t.Errorf("PeerID = %q, want %q", claims.PeerID, "peer-001")
	}
}

func TestIssueBoundsTheLifetime(t *testing.T) {
	issuer, clock := testIssuer(t, 30*time.Second)

	token, err := issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	if token.ExpiresAt.After(clock.Add(30 * time.Second)) {
		t.Errorf("ExpiresAt = %s, want no later than the configured lifetime past %s", token.ExpiresAt, clock)
	}
}

func TestNewTokenIssuerRejects(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	cases := []struct {
		name   string
		key    ed25519.PrivateKey
		issuer string
		ttl    time.Duration
	}{
		{name: "no key", key: nil, issuer: "test-issuer", ttl: time.Hour},
		{name: "a key of the wrong length", key: ed25519.PrivateKey("short"), issuer: "test-issuer", ttl: time.Hour},
		{name: "no issuer", key: key, issuer: "", ttl: time.Hour},
		{name: "a lifetime of zero", key: key, issuer: "test-issuer", ttl: 0},
		{name: "a negative lifetime", key: key, issuer: "test-issuer", ttl: -time.Second},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewTokenIssuer(c.key, c.issuer, c.ttl); err == nil {
				t.Error("NewTokenIssuer() error = nil, want a refusal")
			}
		})
	}
}

func TestPublicKeyIsThePublicHalf(t *testing.T) {
	issuer, _ := testIssuer(t, time.Hour)

	public := issuer.PublicKey()
	if len(public) != ed25519.PublicKeySize {
		t.Fatalf("PublicKey() is %d bytes, want %d", len(public), ed25519.PublicKeySize)
	}
	if strings.Contains(string(issuer.key.Seed()), string(public)) {
		t.Error("PublicKey() returned something containing the seed")
	}
}
