// Package ice turns the configured STUN and relay servers into the list a browser
// can hand straight to a peer connection, with relay credentials that expire on
// their own — adr:0003-use-an-external-turn-service-with-ephemeral-credentials.
package ice

import (
	"crypto/hmac"
	//nolint:gosec // SHA-1 is not a choice here: the relay's REST credential
	// mechanism is defined as HMAC-SHA1, and a credential signed any other way is
	// one the relay refuses. The scheme's security rests on the shared secret and
	// on a lifetime measured in minutes.
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// separator joins the expiry to the peer identifier in a relay username. It is
// part of the mechanism the relay implements, not a choice.
const separator = ":"

// Credential is one relay credential, valid until it expires.
type Credential struct {
	// Username is the expiry and the peer identifier, joined. The relay reads the
	// expiry from it and logs the rest, which is what makes an allocation
	// attributable to a peer.
	Username string
	// Password is the signature of that username under the shared secret.
	Password string
	// ExpiresAt is when the relay will stop accepting it.
	ExpiresAt time.Time
}

// NewCredential signs a credential for one peer. The clock is injected so a test
// can assert an exact expiry; nil takes the wall clock.
func NewCredential(peerID, secret string, lifetime time.Duration, now func() time.Time) (Credential, error) {
	switch {
	case strings.TrimSpace(peerID) == "":
		return Credential{}, errors.New("ice: no peer identifier to name in the credential")
	case strings.Contains(peerID, separator):
		// The relay splits on the first separator, so an identifier carrying one
		// would be read as an expiry. Refusing is the only safe reading.
		return Credential{}, errors.New("ice: peer identifier contains " + separator + ", which the relay reads as the expiry separator")
	case secret == "":
		return Credential{}, errors.New("ice: no shared secret to sign with")
	case lifetime <= 0:
		return Credential{}, errors.New("ice: credential lifetime is not positive")
	}

	if now == nil {
		now = time.Now
	}
	expires := now().Add(lifetime)
	username := strconv.FormatInt(expires.Unix(), 10) + separator + peerID

	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))

	return Credential{
		Username:  username,
		Password:  base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		ExpiresAt: expires,
	}, nil
}

// Server is one entry of the list a browser takes. The field names are the ones
// RTCPeerConnection reads, so a peer passes this through without rewriting it.
type Server struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Config is what the configured servers amount to for one peer.
type Config struct {
	// Servers is the ICE server list, STUN first.
	Servers []Server
	// ExpiresAt is when the relay credential stops working. Zero when there is no
	// relay: nothing in the answer expires.
	ExpiresAt time.Time
	// RelayAvailable says whether a peer with no direct path has anywhere to go.
	// It is reported rather than left to be inferred from the list, because a
	// call that cannot connect for want of a relay is not a network fault.
	RelayAvailable bool
}

// Options are the configured servers and the secret that signs for them.
type Options struct {
	STUNURLs []string
	TURNURLs []string
	// Secret is shared with the relay and never leaves this process.
	Secret string
	// CredentialTTL is how long an issued relay credential lasts.
	CredentialTTL time.Duration
	// Now is the clock, injected for tests.
	Now func() time.Time
}

// Provider builds an ICE configuration per peer.
type Provider struct {
	stun          []string
	turn          []string
	secret        string
	credentialTTL time.Duration
	now           func() time.Time
}

// NewProvider refuses a configuration that promises a relay it cannot sign for.
// A relay with no secret would hand out credentials the relay rejects, which
// fails later, on a call, in a way nobody can read.
func NewProvider(opts Options) (*Provider, error) {
	if len(opts.TURNURLs) > 0 {
		switch {
		case opts.Secret == "":
			return nil, errors.New("ice: relay servers configured with no shared secret to sign credentials")
		case opts.CredentialTTL <= 0:
			return nil, errors.New("ice: relay credential lifetime is not positive")
		}
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Provider{
		stun:          append([]string(nil), opts.STUNURLs...),
		turn:          append([]string(nil), opts.TURNURLs...),
		secret:        opts.Secret,
		credentialTTL: opts.CredentialTTL,
		now:           now,
	}, nil
}

// For builds the configuration one peer should use.
func (p *Provider) For(peerID string) (Config, error) {
	config := Config{RelayAvailable: len(p.turn) > 0}

	if len(p.stun) > 0 {
		config.Servers = append(config.Servers, Server{URLs: append([]string(nil), p.stun...)})
	}

	if !config.RelayAvailable {
		return config, nil
	}

	credential, err := NewCredential(peerID, p.secret, p.credentialTTL, p.now)
	if err != nil {
		return Config{}, err
	}

	config.Servers = append(config.Servers, Server{
		URLs:       append([]string(nil), p.turn...),
		Username:   credential.Username,
		Credential: credential.Password,
	})
	config.ExpiresAt = credential.ExpiresAt

	return config, nil
}
