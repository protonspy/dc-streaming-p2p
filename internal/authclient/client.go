// Package authclient is the peer side of authentication: it pins the central
// server's public key, then exchanges a credential for a token.
//
// The order is the point. A peer starts with two things — the server's URL and
// the key it expects to find there — and it checks the key before it sends
// anything, so a server answering at the right address with the wrong key never
// sees a credential.
package authclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// JWKSPath is where the central server publishes its key set.
const JWKSPath = "/.well-known/jwks.json"

// AuthPath is where a credential is exchanged for a token.
const AuthPath = "/auth"

// maxKeySetBody bounds what will be read from the key set endpoint. One Ed25519
// key is a few hundred bytes.
const maxKeySetBody = 64 << 10

// ErrKeyMismatch is returned when the server publishes a key other than the one
// this client was configured with. It is not a retryable condition: either the
// configuration is stale or the far end is not the server it claims to be.
var ErrKeyMismatch = errors.New("authclient: the server published a different public key")

// ErrRefused is returned when the server refused the credential.
var ErrRefused = errors.New("authclient: the server refused the credential")

// Token is what a successful authentication produced.
type Token struct {
	Value     string    `json:"token"`
	PeerID    string    `json:"peer_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Client talks to one central server.
type Client struct {
	baseURL  string
	expected ed25519.PublicKey
	http     *http.Client

	// allowInsecure permits a plain http:// base URL. Off unless asked for.
	allowInsecure bool

	mu     sync.Mutex
	pinned ed25519.PublicKey
}

// Option adjusts a client at construction.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client, which is how a test points this at a
// server it started and how a deployment sets its own timeouts.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.http = hc
		}
	}
}

// WithInsecureTransport allows a plain http:// server. Pinning the published key
// defeats a server pretending to be the central one; it does nothing about
// somebody reading the credential off the wire, and the credential is sent before
// any token exists. Development and tests are the only honest uses of this.
func WithInsecureTransport() Option {
	return func(c *Client) {
		c.allowInsecure = true
	}
}

// New builds a client for one server. The expected key is required: a client with
// nothing to compare against would accept whatever it was handed, which is the
// failure this package exists to prevent.
func New(baseURL string, expected ed25519.PublicKey, opts ...Option) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("authclient: %q is not a server URL", baseURL)
	}
	if len(expected) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("authclient: expected key is %d bytes, want %d", len(expected), ed25519.PublicKeySize)
	}

	client := &Client{
		baseURL:  trimmed,
		expected: expected,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
	for _, opt := range opts {
		opt(client)
	}

	if parsed.Scheme != "https" && !client.allowInsecure {
		return nil, fmt.Errorf(
			"authclient: %q is not https, and the credential is sent before there is a token to protect it — pass WithInsecureTransport to allow it anyway",
			trimmed)
	}
	return client, nil
}

// VerifyServerKey fetches the published key set and compares it with the expected
// key. It is what Authenticate does first, and it is exported because a peer
// starting up has reason to fail early rather than at its first credential.
func (c *Client) VerifyServerKey(ctx context.Context) error {
	c.mu.Lock()
	pinned := c.pinned
	c.mu.Unlock()
	if pinned != nil {
		return nil
	}

	// fetchKey returns only a key that matched the expected one, so reaching here
	// without an error is the pin holding.
	published, err := c.fetchKey(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.pinned = published
	c.mu.Unlock()
	return nil
}

// Authenticate exchanges a credential for a token, having first confirmed the
// server holds the expected key.
func (c *Client) Authenticate(ctx context.Context, clientID, clientSecret string) (Token, error) {
	if err := c.VerifyServerKey(ctx); err != nil {
		return Token{}, err
	}

	body, err := json.Marshal(map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
	})
	if err != nil {
		return Token{}, fmt.Errorf("authclient: encoding the credential: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+AuthPath, bytes.NewReader(body))
	if err != nil {
		return Token{}, fmt.Errorf("authclient: building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("authclient: reaching the server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("%w: %s", ErrRefused, resp.Status)
	}

	var token Token
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxKeySetBody)).Decode(&token); err != nil {
		return Token{}, fmt.Errorf("authclient: reading the token: %w", err)
	}
	if token.Value == "" || token.PeerID == "" {
		return Token{}, errors.New("authclient: the server answered without a token")
	}
	return token, nil
}

// fetchKey reads the published key set and returns the one Ed25519 signing key in
// it. A set carrying several keys during a rotation is accepted, and the expected
// one only has to be among them.
func (c *Client) fetchKey(ctx context.Context) (ed25519.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+JWKSPath, nil)
	if err != nil {
		return nil, fmt.Errorf("authclient: building the key request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("authclient: fetching the key set: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authclient: the key set answered %s", resp.Status)
	}

	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxKeySetBody)).Decode(&set); err != nil {
		return nil, fmt.Errorf("authclient: reading the key set: %w", err)
	}

	for _, key := range set.Keys {
		if key.Kty != "OKP" || key.Crv != "Ed25519" {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			continue
		}
		if subtle.ConstantTimeCompare(decoded, c.expected) == 1 {
			return ed25519.PublicKey(decoded), nil
		}
	}

	// Nothing in the set matched. Returning the mismatch rather than "no key
	// found" is deliberate: from here they are the same failure, and a peer that
	// treats them differently will eventually treat one of them as retryable.
	return nil, ErrKeyMismatch
}
