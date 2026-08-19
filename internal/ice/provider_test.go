package ice

import (
	"strings"
	"testing"
	"time"
)

func testProvider(t *testing.T, stun, turn []string) *Provider {
	t.Helper()

	secret := ""
	if len(turn) > 0 {
		secret = testSecret
	}

	provider, err := NewProvider(Options{
		STUNURLs:      stun,
		TURNURLs:      turn,
		Secret:        secret,
		CredentialTTL: 10 * time.Minute,
		Now:           func() time.Time { return at },
	})
	if err != nil {
		t.Fatalf("NewProvider() error = %v, want nil", err)
	}
	return provider
}

func TestForListsStunAndRelay(t *testing.T) {
	provider := testProvider(t, []string{"stun:stun.example:3478"}, []string{"turn:relay.example:3478", "turns:relay.example:5349"})

	config, err := provider.For("peer-001")
	if err != nil {
		t.Fatalf("For() error = %v, want nil", err)
	}

	if len(config.Servers) != 2 {
		t.Fatalf("Servers = %+v, want STUN and relay", config.Servers)
	}
	if config.Servers[0].Username != "" || config.Servers[0].Credential != "" {
		t.Errorf("the STUN entry carries a credential: %+v — STUN needs none", config.Servers[0])
	}

	relay := config.Servers[1]
	if len(relay.URLs) != 2 {
		t.Errorf("relay URLs = %v, want both configured entries in one entry", relay.URLs)
	}
	if !strings.HasSuffix(relay.Username, ":peer-001") {
		t.Errorf("relay username = %q, want it to name the peer", relay.Username)
	}
	if relay.Credential == "" {
		t.Error("relay entry has no credential")
	}
	if !config.RelayAvailable {
		t.Error("RelayAvailable = false with a relay configured")
	}
	if !config.ExpiresAt.Equal(at.Add(10 * time.Minute)) {
		t.Errorf("ExpiresAt = %s, want %s", config.ExpiresAt, at.Add(10*time.Minute))
	}
}

func TestForWithoutARelay(t *testing.T) {
	provider := testProvider(t, []string{"stun:stun.example:3478"}, nil)

	config, err := provider.For("peer-001")
	if err != nil {
		t.Fatalf("For() error = %v, want nil", err)
	}

	if len(config.Servers) != 1 {
		t.Errorf("Servers = %+v, want the STUN entry alone", config.Servers)
	}
	if config.RelayAvailable {
		t.Error("RelayAvailable = true with no relay configured")
	}
	if !config.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %s, want zero — nothing in this answer expires", config.ExpiresAt)
	}
}

func TestForWithNothingConfigured(t *testing.T) {
	provider := testProvider(t, nil, nil)

	config, err := provider.For("peer-001")
	if err != nil {
		t.Fatalf("For() error = %v, want nil", err)
	}
	if len(config.Servers) != 0 || config.RelayAvailable {
		t.Errorf("Config = %+v, want an empty list and no relay", config)
	}
}

func TestForGivesEachPeerItsOwnCredential(t *testing.T) {
	provider := testProvider(t, nil, []string{"turn:relay.example:3478"})

	first, err := provider.For("peer-001")
	if err != nil {
		t.Fatalf("For() error = %v", err)
	}
	second, err := provider.For("peer-002")
	if err != nil {
		t.Fatalf("For() error = %v", err)
	}

	if first.Servers[0].Credential == second.Servers[0].Credential {
		t.Error("two peers were handed the same relay credential")
	}
}

func TestForRefusesAPeerIdentifierTheRelayCannotRead(t *testing.T) {
	provider := testProvider(t, nil, []string{"turn:relay.example:3478"})

	if _, err := provider.For("peer:001"); err == nil {
		t.Error("For() error = nil for an identifier carrying the separator, want a refusal")
	}
}

func TestNewProviderRefusesARelayItCannotSignFor(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{
			name: "relay servers with no secret",
			opts: Options{TURNURLs: []string{"turn:relay.example:3478"}, CredentialTTL: time.Minute},
		},
		{
			name: "relay servers with no credential lifetime",
			opts: Options{TURNURLs: []string{"turn:relay.example:3478"}, Secret: testSecret},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewProvider(c.opts); err == nil {
				t.Error("NewProvider() error = nil, want a refusal — credentials the relay rejects fail later, on a call")
			}
		})
	}
}

func TestProviderCopiesWhatItWasGiven(t *testing.T) {
	urls := []string{"stun:stun.example:3478"}
	provider := testProvider(t, urls, nil)

	urls[0] = "stun:somewhere-else:3478"

	config, err := provider.For("peer-001")
	if err != nil {
		t.Fatalf("For() error = %v", err)
	}
	if config.Servers[0].URLs[0] != "stun:stun.example:3478" {
		t.Errorf("URLs = %v, want the value configured at construction", config.Servers[0].URLs)
	}
}
