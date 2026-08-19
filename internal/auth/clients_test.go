package auth

import (
	"errors"
	"strings"
	"testing"
)

// secret is a credential of the minimum accepted length, distinct per name.
func secret(name string) string {
	s := name + strings.Repeat("x", MinSecretLen)
	return s[:MinSecretLen]
}

func oneClient() []ClientConfig {
	return []ClientConfig{{ID: "sdk-web", PeerID: "peer-001", Secret: secret("a")}}
}

func TestAuthenticateReturnsThePeerIdentifier(t *testing.T) {
	store, err := NewClientStore(oneClient())
	if err != nil {
		t.Fatalf("NewClientStore() error = %v, want nil", err)
	}

	peerID, err := store.Authenticate("sdk-web", secret("a"))
	if err != nil {
		t.Fatalf("Authenticate() error = %v, want nil", err)
	}
	if peerID != "peer-001" {
		t.Errorf("Authenticate() = %q, want %q", peerID, "peer-001")
	}
}

func TestAuthenticateRefuses(t *testing.T) {
	store, err := NewClientStore(oneClient())
	if err != nil {
		t.Fatalf("NewClientStore() error = %v, want nil", err)
	}

	cases := []struct {
		name   string
		id     string
		secret string
	}{
		{name: "an unknown identifier", id: "nobody", secret: secret("a")},
		{name: "the wrong secret", id: "sdk-web", secret: secret("b")},
		{name: "an empty secret", id: "sdk-web", secret: ""},
		{name: "a secret that is a prefix of the right one", id: "sdk-web", secret: secret("a")[:8]},
		{name: "both empty", id: "", secret: ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			peerID, err := store.Authenticate(c.id, c.secret)
			if !errors.Is(err, ErrUnknownClient) {
				t.Errorf("Authenticate() error = %v, want ErrUnknownClient", err)
			}
			if peerID != "" {
				t.Errorf("Authenticate() = %q, want no peer identifier", peerID)
			}
		})
	}
}

func TestAuthenticateAnswersTheSameForAbsentAndWrong(t *testing.T) {
	store, err := NewClientStore(oneClient())
	if err != nil {
		t.Fatalf("NewClientStore() error = %v, want nil", err)
	}

	_, absent := store.Authenticate("nobody", secret("a"))
	_, wrong := store.Authenticate("sdk-web", secret("b"))

	if absent.Error() != wrong.Error() {
		t.Errorf("errors differ: absent = %q, wrong secret = %q — the caller must not learn which client identifiers exist",
			absent, wrong)
	}
}

func TestNewClientStoreRejects(t *testing.T) {
	cases := []struct {
		name    string
		clients []ClientConfig
		want    string
	}{
		{
			name:    "no clients at all",
			clients: nil,
			want:    "no clients configured",
		},
		{
			name:    "an empty list",
			clients: []ClientConfig{},
			want:    "no clients configured",
		},
		{
			name:    "a client with no identifier",
			clients: []ClientConfig{{PeerID: "peer-001", Secret: secret("a")}},
			want:    "no client_id",
		},
		{
			name:    "a client with no peer identifier",
			clients: []ClientConfig{{ID: "sdk-web", Secret: secret("a")}},
			want:    "no peer_id",
		},
		{
			name:    "a secret shorter than the minimum",
			clients: []ClientConfig{{ID: "sdk-web", PeerID: "peer-001", Secret: "short"}},
			want:    "at least 32",
		},
		{
			name:    "a peer identifier the relay would misread",
			clients: []ClientConfig{{ID: "sdk-web", PeerID: "peer:001", Secret: secret("a")}},
			want:    "outside a-z",
		},
		{
			name:    "a peer identifier with a space in it",
			clients: []ClientConfig{{ID: "sdk-web", PeerID: "peer 001", Secret: secret("a")}},
			want:    "outside a-z",
		},
		{
			name:    "a peer identifier longer than the limit",
			clients: []ClientConfig{{ID: "sdk-web", PeerID: strings.Repeat("p", MaxPeerIDLen+1), Secret: secret("a")}},
			want:    "at most 128",
		},
		{
			name: "the same identifier twice",
			clients: []ClientConfig{
				{ID: "sdk-web", PeerID: "peer-001", Secret: secret("a")},
				{ID: "sdk-web", PeerID: "peer-002", Secret: secret("b")},
			},
			want: "configured twice",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, err := NewClientStore(c.clients)
			if err == nil {
				t.Fatalf("NewClientStore() error = nil, want a complaint about %q", c.want)
			}
			if store != nil {
				t.Error("NewClientStore() returned a store beside the error, want nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("NewClientStore() error = %q, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestNewClientStoreNamesTheClientItRefused(t *testing.T) {
	_, err := NewClientStore([]ClientConfig{
		{ID: "good", PeerID: "peer-001", Secret: secret("a")},
		{ID: "bad", PeerID: "peer-002", Secret: "short"},
	})
	if err == nil {
		t.Fatal("NewClientStore() error = nil, want the short secret refused")
	}
	if !strings.Contains(err.Error(), `"bad"`) {
		t.Errorf("NewClientStore() error = %q, want it to name the client it refused", err)
	}
}

func TestNewClientStoreReportsEveryProblem(t *testing.T) {
	_, err := NewClientStore([]ClientConfig{
		{ID: "", PeerID: "peer-001", Secret: secret("a")},
		{ID: "second", Secret: secret("b")},
	})
	if err == nil {
		t.Fatal("NewClientStore() error = nil, want both problems")
	}
	for _, want := range []string{"no client_id", "no peer_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("NewClientStore() error = %q, want it to report %q", err, want)
		}
	}
}

func TestNewClientStoreDoesNotKeepTheSecret(t *testing.T) {
	plain := secret("a")
	store, err := NewClientStore([]ClientConfig{{ID: "sdk-web", PeerID: "peer-001", Secret: plain}})
	if err != nil {
		t.Fatalf("NewClientStore() error = %v, want nil", err)
	}

	for id, client := range store.clients {
		if strings.Contains(string(client.secretHash[:]), plain) {
			t.Errorf("client %q holds its secret rather than a hash of it", id)
		}
	}
}

func TestLenCountsTheConfiguredClients(t *testing.T) {
	store, err := NewClientStore([]ClientConfig{
		{ID: "one", PeerID: "peer-001", Secret: secret("a")},
		{ID: "two", PeerID: "peer-002", Secret: secret("b")},
	})
	if err != nil {
		t.Fatalf("NewClientStore() error = %v, want nil", err)
	}
	if got := store.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}
}

func TestPeerIdentifiersThatAreAccepted(t *testing.T) {
	for _, peerID := range []string{"peer-001", "peer_001", "peer.001", "PEER001", strings.Repeat("p", MaxPeerIDLen)} {
		t.Run(peerID, func(t *testing.T) {
			if _, err := NewClientStore([]ClientConfig{{ID: "sdk-web", PeerID: peerID, Secret: secret("a")}}); err != nil {
				t.Errorf("NewClientStore() error = %v for %q, want nil", err, peerID)
			}
		})
	}
}
