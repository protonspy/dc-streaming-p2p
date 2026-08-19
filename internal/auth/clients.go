// Package auth turns a client credential into a peer identifier, and a token back
// into one. It knows nothing about HTTP: the transport layer calls into it.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
)

// peerIDAllowed is the character set a peer identifier may use. It is narrow
// because the identifier is not only ours: it is signed into a token, joined into
// a relay credential username, and logged by the relay. A colon in it would be
// read by the relay as the separator before the expiry, so an identifier that
// cannot be carried everywhere is refused where it is configured rather than
// where it is used — a startup refusal instead of a per-request failure a peer
// cannot act on.
func peerIDAllowed(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-', r == '_', r == '.':
		return true
	default:
		return false
	}
}

// MaxPeerIDLen bounds the identifier, which ends up in a map key, a token claim
// and a relay username.
const MaxPeerIDLen = 128

// MinSecretLen is the shortest client secret this service accepts. Secrets are
// held as a plain hash rather than a password hash, which is only defensible
// because they are machine credentials this long — see the spec's design.
const MinSecretLen = 32

// ErrUnknownClient is returned for every credential failure: an identifier nobody
// configured and a secret that does not match are the same answer, on purpose.
var ErrUnknownClient = errors.New("auth: unknown client or wrong secret")

// ClientConfig is one configured client, as it arrives from the environment.
type ClientConfig struct {
	// ID is what the client calls itself when authenticating.
	ID string `json:"client_id"`
	// PeerID is the peer identifier tokens issued to this client will name.
	PeerID string `json:"peer_id"`
	// Secret is the credential it authenticates with. It is hashed on the way in
	// and this field is not kept.
	Secret string `json:"secret"`
}

// ClientStore holds the configured clients and answers whether a presented
// credential is one of them.
type ClientStore struct {
	clients map[string]storedClient
	// dummy is compared against when the identifier is unknown, so that the
	// absent client and the wrong secret take the same path and the same time.
	dummy [sha256.Size]byte
}

type storedClient struct {
	peerID     string
	secretHash [sha256.Size]byte
}

// NewClientStore validates the configured clients and hashes their secrets. It
// reports every problem it found rather than the first, and refuses an empty list:
// a control plane nobody can authenticate against is a misconfiguration, not an
// empty state.
func NewClientStore(configs []ClientConfig) (*ClientStore, error) {
	var problems []error
	clients := make(map[string]storedClient, len(configs))

	for i, c := range configs {
		id := strings.TrimSpace(c.ID)
		peerID := strings.TrimSpace(c.PeerID)

		switch {
		case id == "":
			problems = append(problems, fmt.Errorf("client %d: no client_id", i))
			continue
		case peerID == "":
			problems = append(problems, fmt.Errorf("client %q: no peer_id", id))
			continue
		case len(peerID) > MaxPeerIDLen:
			problems = append(problems, fmt.Errorf("client %q: peer_id is %d characters, at most %d",
				id, len(peerID), MaxPeerIDLen))
			continue
		case !validPeerID(peerID):
			problems = append(problems, fmt.Errorf(
				"client %q: peer_id %q has a character outside a-z A-Z 0-9 - _ . — it has to survive a token claim and a relay username",
				id, peerID))
			continue
		case len(c.Secret) < MinSecretLen:
			problems = append(problems, fmt.Errorf("client %q: secret is %d characters, needs at least %d",
				id, len(c.Secret), MinSecretLen))
			continue
		}

		if _, taken := clients[id]; taken {
			problems = append(problems, fmt.Errorf("client %q: configured twice", id))
			continue
		}

		clients[id] = storedClient{peerID: peerID, secretHash: sha256.Sum256([]byte(c.Secret))}
	}

	if len(problems) == 0 && len(clients) == 0 {
		problems = append(problems, errors.New("no clients configured, so nothing could ever authenticate"))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("auth: %w", errors.Join(problems...))
	}

	return &ClientStore{
		clients: clients,
		dummy:   sha256.Sum256([]byte("this hash exists so that an unknown client costs what a known one costs")),
	}, nil
}

// Authenticate returns the peer identifier the credential authenticates as.
//
// An unknown identifier still performs the comparison, against a hash that cannot
// match, so that the work done is the same either way and the caller cannot learn
// which client identifiers exist by timing the answer.
func (s *ClientStore) Authenticate(id, secret string) (string, error) {
	client, known := s.clients[id]
	expected := client.secretHash
	if !known {
		expected = s.dummy
	}

	presented := sha256.Sum256([]byte(secret))
	matched := subtle.ConstantTimeCompare(presented[:], expected[:]) == 1

	if !known || !matched {
		return "", ErrUnknownClient
	}
	return client.peerID, nil
}

// Len is how many clients are configured, for a startup log line that says the
// configuration was read without saying what is in it.
func (s *ClientStore) Len() int {
	return len(s.clients)
}

// validPeerID reports whether every character of the identifier is one that
// survives everywhere the identifier goes.
func validPeerID(peerID string) bool {
	for _, r := range peerID {
		if !peerIDAllowed(r) {
			return false
		}
	}
	return peerID != ""
}
