// Package config reads the central server's settings from the environment and
// refuses to start on a value that would fail later. Everything the server needs
// to run is decided here, once, before anything listens.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Prefix is what every variable this package reads starts with.
const Prefix = "CENTRAL_"

// Config is the settled configuration of one process.
type Config struct {
	// ListenAddr is the host:port the HTTP surface binds.
	ListenAddr string
	// ShutdownTimeout bounds how long a graceful shutdown waits for connections.
	ShutdownTimeout time.Duration
	// AllowedOrigins are the browser origins allowed to open a control-plane
	// connection. Empty means every origin, which is only ever right in development.
	AllowedOrigins []string

	// TokenSigningKey signs the tokens issued by POST /auth.
	TokenSigningKey string
	// TokenTTL is how long an issued token stays valid.
	TokenTTL time.Duration

	// HeartbeatInterval is how often a peer is expected to report in.
	HeartbeatInterval time.Duration
	// SuspectAfter is the silence that moves a peer from online to suspect.
	SuspectAfter time.Duration
	// OfflineAfter is the silence that removes a peer from the registry.
	OfflineAfter time.Duration

	// NegotiationTimeout bounds how long a session may stay unconnected before it
	// is abandoned.
	NegotiationTimeout time.Duration

	// STUNURLs are handed to peers as-is.
	STUNURLs []string
	// TURNURLs are the relay endpoints offered to peers. Empty disables the relay.
	TURNURLs []string
	// TURNSecret is the shared secret the relay verifies ephemeral credentials
	// against. Required whenever TURNURLs is not empty, and it never leaves the
	// server: only its HMAC does.
	TURNSecret string
	// TURNCredentialTTL is how long an issued relay credential stays valid.
	TURNCredentialTTL time.Duration
}

// minSigningKeyLen is the shortest signing key this server accepts. A key shorter
// than the hash it feeds adds nothing to the security of the signature.
const minSigningKeyLen = 32

// Getenv reads one variable. It is os.Getenv in production and a map in a test.
type Getenv func(string) string

// Load reads the configuration from getenv, fills in the defaults, and validates
// the result. It reports every problem it found rather than the first.
func Load(getenv Getenv) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("config: no environment to read")
	}

	var problems []error
	get := func(key, fallback string) string {
		if v := strings.TrimSpace(getenv(Prefix + key)); v != "" {
			return v
		}
		return fallback
	}
	duration := func(key, fallback string) time.Duration {
		raw := get(key, fallback)
		d, err := time.ParseDuration(raw)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s%s: %q is not a duration", Prefix, key, raw))
			return 0
		}
		if d <= 0 {
			problems = append(problems, fmt.Errorf("%s%s: %s is not a positive duration", Prefix, key, d))
			return 0
		}
		return d
	}

	cfg := Config{
		ListenAddr:         get("LISTEN_ADDR", ":8080"),
		ShutdownTimeout:    duration("SHUTDOWN_TIMEOUT", "15s"),
		AllowedOrigins:     splitList(get("ALLOWED_ORIGINS", "")),
		TokenSigningKey:    get("TOKEN_SIGNING_KEY", ""),
		TokenTTL:           duration("TOKEN_TTL", "1h"),
		HeartbeatInterval:  duration("HEARTBEAT_INTERVAL", "15s"),
		SuspectAfter:       duration("SUSPECT_AFTER", "30s"),
		OfflineAfter:       duration("OFFLINE_AFTER", "60s"),
		NegotiationTimeout: duration("NEGOTIATION_TIMEOUT", "2m"),
		STUNURLs:           splitList(get("STUN_URLS", "stun:stun.l.google.com:19302")),
		TURNURLs:           splitList(get("TURN_URLS", "")),
		TURNSecret:         get("TURN_SECRET", ""),
		TURNCredentialTTL:  duration("TURN_CREDENTIAL_TTL", "10m"),
	}

	problems = append(problems, cfg.validate()...)
	if len(problems) > 0 {
		return Config{}, fmt.Errorf("config: %w", errors.Join(problems...))
	}
	return cfg, nil
}

// validate reports every setting that is present and wrong, or absent and required.
func (c Config) validate() []error {
	var problems []error

	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		problems = append(problems, fmt.Errorf("%sLISTEN_ADDR: %q is not host:port", Prefix, c.ListenAddr))
	}
	if len(c.TokenSigningKey) < minSigningKeyLen {
		problems = append(problems, fmt.Errorf("%sTOKEN_SIGNING_KEY: needs at least %d characters, has %d",
			Prefix, minSigningKeyLen, len(c.TokenSigningKey)))
	}
	if c.SuspectAfter > 0 && c.HeartbeatInterval > 0 && c.SuspectAfter <= c.HeartbeatInterval {
		problems = append(problems, fmt.Errorf("%sSUSPECT_AFTER: %s is not longer than the %s heartbeat interval",
			Prefix, c.SuspectAfter, c.HeartbeatInterval))
	}
	if c.OfflineAfter > 0 && c.SuspectAfter > 0 && c.OfflineAfter <= c.SuspectAfter {
		problems = append(problems, fmt.Errorf("%sOFFLINE_AFTER: %s is not longer than the %s suspect window",
			Prefix, c.OfflineAfter, c.SuspectAfter))
	}

	problems = append(problems, validateICEURLs("STUN_URLS", c.STUNURLs, "stun", "stuns")...)
	problems = append(problems, validateICEURLs("TURN_URLS", c.TURNURLs, "turn", "turns")...)

	if len(c.TURNURLs) > 0 && c.TURNSecret == "" {
		problems = append(problems, fmt.Errorf("%sTURN_SECRET: required whenever %sTURN_URLS is set", Prefix, Prefix))
	}
	if len(c.TURNURLs) == 0 && c.TURNSecret != "" {
		problems = append(problems, fmt.Errorf("%sTURN_SECRET: set with no %sTURN_URLS to use it", Prefix, Prefix))
	}

	return problems
}

// RelayConfigured reports whether peers can be offered a relay at all. Without one,
// a peer with no direct path has nowhere to go.
func (c Config) RelayConfigured() bool {
	return len(c.TURNURLs) > 0 && c.TURNSecret != ""
}

// splitList reads a comma-separated setting, dropping empty entries and surrounding
// space so that "a, b," is the two entries it looks like.
func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.Split(raw, ",")
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if v := strings.TrimSpace(f); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateICEURLs checks that every entry is a URL a browser will accept in its ICE
// configuration: one of the given schemes, and a host to reach.
func validateICEURLs(key string, urls []string, schemes ...string) []error {
	var problems []error
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s%s: %q is not a URL", Prefix, key, raw))
			continue
		}
		if !containsFold(schemes, u.Scheme) {
			problems = append(problems, fmt.Errorf("%s%s: %q has scheme %q, want one of %s",
				Prefix, key, raw, u.Scheme, strings.Join(schemes, ", ")))
			continue
		}
		if host := hostOf(u); host == "" {
			problems = append(problems, fmt.Errorf("%s%s: %q names no host", Prefix, key, raw))
		}
	}
	return problems
}

// hostOf pulls the host out of an ICE URL. These are opaque URLs — "stun:host:port"
// has no authority section — so the host lands in Opaque rather than Host.
func hostOf(u *url.URL) string {
	if u.Opaque != "" {
		host, _, err := net.SplitHostPort(u.Opaque)
		if err != nil {
			return strings.TrimSpace(u.Opaque)
		}
		return strings.TrimSpace(host)
	}
	return strings.TrimSpace(u.Hostname())
}

// containsFold reports whether values holds want, ignoring case, which is how URL
// schemes compare.
func containsFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// String renders the configuration for a startup log line, with the secrets
// replaced by their length. A configuration that logs its own signing key is one
// leaked signing key.
func (c Config) String() string {
	return fmt.Sprintf(
		"listen=%s token_ttl=%s signing_key=%s heartbeat=%s suspect=%s offline=%s negotiation=%s stun=%d relay=%t turn_credential_ttl=%s",
		c.ListenAddr, c.TokenTTL, redact(c.TokenSigningKey), c.HeartbeatInterval, c.SuspectAfter,
		c.OfflineAfter, c.NegotiationTimeout, len(c.STUNURLs), c.RelayConfigured(), c.TURNCredentialTTL,
	)
}

// redact describes a secret without disclosing it.
func redact(secret string) string {
	if secret == "" {
		return "unset"
	}
	return "set(" + strconv.Itoa(len(secret)) + " chars)"
}
