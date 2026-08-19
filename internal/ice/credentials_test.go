package ice

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testSecret = "the-secret-the-relay-also-holds"

// at is a fixed instant, so an expiry can be asserted exactly.
var at = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

func TestCredentialNamesTheExpiryAndThePeer(t *testing.T) {
	credential, err := NewCredential("peer-001", testSecret, 10*time.Minute, func() time.Time { return at })
	if err != nil {
		t.Fatalf("NewCredential() error = %v, want nil", err)
	}

	wantExpiry := at.Add(10 * time.Minute)
	if !credential.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %s, want %s", credential.ExpiresAt, wantExpiry)
	}

	unix, peerID, found := strings.Cut(credential.Username, ":")
	if !found {
		t.Fatalf("Username = %q, want <expiry>:<peer identifier>", credential.Username)
	}
	if peerID != "peer-001" {
		t.Errorf("username names %q, want %q — the relay logs this, and it is what makes an allocation attributable", peerID, "peer-001")
	}
	if unix != strconv.FormatInt(wantExpiry.Unix(), 10) {
		t.Errorf("username expiry = %q, want %d", unix, wantExpiry.Unix())
	}
}

func TestCredentialIsTheSignatureTheRelayWillCheck(t *testing.T) {
	credential, err := NewCredential("peer-001", testSecret, time.Minute, func() time.Time { return at })
	if err != nil {
		t.Fatalf("NewCredential() error = %v, want nil", err)
	}

	// The relay recomputes exactly this; anything else is a credential it refuses.
	mac := hmac.New(sha1.New, []byte(testSecret))
	mac.Write([]byte(credential.Username))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if credential.Password != want {
		t.Errorf("Password = %q, want %q", credential.Password, want)
	}
}

func TestCredentialDiffersPerPeerAndPerMoment(t *testing.T) {
	first, err := NewCredential("peer-001", testSecret, time.Minute, func() time.Time { return at })
	if err != nil {
		t.Fatalf("NewCredential() error = %v", err)
	}
	other, err := NewCredential("peer-002", testSecret, time.Minute, func() time.Time { return at })
	if err != nil {
		t.Fatalf("NewCredential() error = %v", err)
	}
	later, err := NewCredential("peer-001", testSecret, time.Minute, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatalf("NewCredential() error = %v", err)
	}

	if first.Password == other.Password {
		t.Error("two peers were given the same password at the same instant")
	}
	if first.Password == later.Password {
		t.Error("the same peer was given the same password a second later; the expiry is part of what is signed")
	}
}

func TestCredentialNeverCarriesTheSecret(t *testing.T) {
	credential, err := NewCredential("peer-001", testSecret, time.Minute, func() time.Time { return at })
	if err != nil {
		t.Fatalf("NewCredential() error = %v", err)
	}

	for _, field := range []string{credential.Username, credential.Password} {
		if strings.Contains(field, testSecret) {
			t.Errorf("a credential field carries the shared secret: %q", field)
		}
	}
}

func TestNewCredentialRejects(t *testing.T) {
	cases := []struct {
		name     string
		peerID   string
		secret   string
		lifetime time.Duration
	}{
		{name: "no peer identifier", peerID: "", secret: testSecret, lifetime: time.Minute},
		{name: "no secret", peerID: "peer-001", secret: "", lifetime: time.Minute},
		{name: "a lifetime of zero", peerID: "peer-001", secret: testSecret, lifetime: 0},
		{name: "a negative lifetime", peerID: "peer-001", secret: testSecret, lifetime: -time.Minute},
		{name: "a peer identifier carrying the separator", peerID: "peer:001", secret: testSecret, lifetime: time.Minute},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewCredential(c.peerID, c.secret, c.lifetime, nil); err == nil {
				t.Error("NewCredential() error = nil, want a refusal")
			}
		})
	}
}
