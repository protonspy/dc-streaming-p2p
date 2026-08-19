package config

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/protonspy/dc-streaming-p2p/internal/auth"
)

// env turns a map into a Getenv, with the prefix already applied to the keys so a
// test names the variable the way an operator does.
func env(vars map[string]string) Getenv {
	return func(key string) string { return vars[key] }
}

// testSigningKey is a seed of the right length, base64, as an operator would
// produce with `openssl rand -base64 32`.
var testSigningKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), SigningKeySeedLen))

// testClients is one client whose secret is exactly the minimum length.
var testClients = `[{"client_id":"sdk-web","peer_id":"peer-001","secret":"` +
	strings.Repeat("s", auth.MinSecretLen) + `"}]`

// valid is the smallest environment that loads, and the base every case edits.
func valid() map[string]string {
	return map[string]string{
		"CENTRAL_TOKEN_SIGNING_KEY": testSigningKey,
		"CENTRAL_CLIENTS":           testClients,
		"CENTRAL_ALLOWED_ORIGINS":   AnyOrigin,
	}
}

func TestLoadFillsInTheDefaults(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.TokenTTL != time.Hour {
		t.Errorf("TokenTTL = %s, want %s", cfg.TokenTTL, time.Hour)
	}
	if cfg.HeartbeatInterval != 15*time.Second {
		t.Errorf("HeartbeatInterval = %s, want %s", cfg.HeartbeatInterval, 15*time.Second)
	}
	if len(cfg.STUNURLs) != 1 {
		t.Errorf("STUNURLs = %v, want one default entry", cfg.STUNURLs)
	}
	if cfg.RelayConfigured() {
		t.Error("RelayConfigured() = true with no TURN configured, want false")
	}
}

func TestLoadReadsEverySetting(t *testing.T) {
	vars := valid()
	vars["CENTRAL_LISTEN_ADDR"] = "127.0.0.1:9000"
	vars["CENTRAL_SHUTDOWN_TIMEOUT"] = "5s"
	vars["CENTRAL_ALLOWED_ORIGINS"] = "https://a.example, https://b.example,"
	vars["CENTRAL_TOKEN_TTL"] = "30m"
	vars["CENTRAL_HEARTBEAT_INTERVAL"] = "5s"
	vars["CENTRAL_SUSPECT_AFTER"] = "10s"
	vars["CENTRAL_OFFLINE_AFTER"] = "20s"
	vars["CENTRAL_NEGOTIATION_TIMEOUT"] = "45s"
	vars["CENTRAL_STUN_URLS"] = "stun:stun.example:3478"
	vars["CENTRAL_TURN_URLS"] = "turn:relay.example:3478,turns:relay.example:5349"
	vars["CENTRAL_TURN_SECRET"] = "shared-with-the-relay"
	vars["CENTRAL_TURN_CREDENTIAL_TTL"] = "2m"

	cfg, err := Load(env(vars))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, "127.0.0.1:9000")
	}
	if len(cfg.AllowedOrigins) != 2 {
		t.Errorf("AllowedOrigins = %v, want two entries", cfg.AllowedOrigins)
	}
	if cfg.ShutdownTimeout != 5*time.Second || cfg.NegotiationTimeout != 45*time.Second {
		t.Errorf("timeouts = %s/%s, want 5s/45s", cfg.ShutdownTimeout, cfg.NegotiationTimeout)
	}
	if len(cfg.TURNURLs) != 2 || !cfg.RelayConfigured() {
		t.Errorf("TURNURLs = %v, RelayConfigured = %t, want two entries and true", cfg.TURNURLs, cfg.RelayConfigured())
	}
	if cfg.TURNCredentialTTL != 2*time.Minute {
		t.Errorf("TURNCredentialTTL = %s, want 2m", cfg.TURNCredentialTTL)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]string)
		want string
	}{
		{
			name: "a missing signing key",
			edit: func(v map[string]string) { delete(v, "CENTRAL_TOKEN_SIGNING_KEY") },
			want: "TOKEN_SIGNING_KEY",
		},
		{
			name: "a signing key that is not base64",
			edit: func(v map[string]string) { v["CENTRAL_TOKEN_SIGNING_KEY"] = "not base64!" },
			want: "not base64",
		},
		{
			name: "a signing key of the wrong length",
			edit: func(v map[string]string) {
				v["CENTRAL_TOKEN_SIGNING_KEY"] = base64.StdEncoding.EncodeToString([]byte("too short"))
			},
			want: "want 32",
		},
		{
			name: "no clients",
			edit: func(v map[string]string) { delete(v, "CENTRAL_CLIENTS") },
			want: "CLIENTS",
		},
		{
			name: "clients that are not JSON",
			edit: func(v map[string]string) { v["CENTRAL_CLIENTS"] = "sdk-web:secret" },
			want: "JSON",
		},
		{
			name: "a client whose secret is too short",
			edit: func(v map[string]string) {
				v["CENTRAL_CLIENTS"] = `[{"client_id":"sdk-web","peer_id":"peer-001","secret":"short"}]`
			},
			want: "at least 32",
		},
		{
			name: "a client with no peer identifier",
			edit: func(v map[string]string) {
				v["CENTRAL_CLIENTS"] = `[{"client_id":"sdk-web","secret":"` + strings.Repeat("s", auth.MinSecretLen) + `"}]`
			},
			want: "no peer_id",
		},
		{
			name: "an allowance that is not a number",
			edit: func(v map[string]string) { v["CENTRAL_AUTH_MAX_FAILURES"] = "many" },
			want: "AUTH_MAX_FAILURES",
		},
		{
			name: "an allowance of zero, which would refuse everyone",
			edit: func(v map[string]string) { v["CENTRAL_AUTH_MAX_FAILURES"] = "0" },
			want: "AUTH_MAX_FAILURES",
		},
		{
			name: "a peer ceiling that is not positive",
			edit: func(v map[string]string) { v["CENTRAL_MAX_PEERS"] = "-1" },
			want: "MAX_PEERS",
		},
		{
			name: "an expiry interval of zero",
			edit: func(v map[string]string) { v["CENTRAL_EXPIRY_INTERVAL"] = "0s" },
			want: "EXPIRY_INTERVAL",
		},
		{
			name: "no allowed origins, which must be a decision rather than a default",
			edit: func(v map[string]string) { delete(v, "CENTRAL_ALLOWED_ORIGINS") },
			want: "ALLOWED_ORIGINS",
		},
		{
			name: "a listen address with no port",
			edit: func(v map[string]string) { v["CENTRAL_LISTEN_ADDR"] = "localhost" },
			want: "LISTEN_ADDR",
		},
		{
			name: "a duration that does not parse",
			edit: func(v map[string]string) { v["CENTRAL_TOKEN_TTL"] = "one hour" },
			want: "TOKEN_TTL",
		},
		{
			name: "a duration that is not positive",
			edit: func(v map[string]string) { v["CENTRAL_SHUTDOWN_TIMEOUT"] = "0s" },
			want: "SHUTDOWN_TIMEOUT",
		},
		{
			name: "a suspect window inside the heartbeat interval",
			edit: func(v map[string]string) { v["CENTRAL_SUSPECT_AFTER"] = "15s" },
			want: "SUSPECT_AFTER",
		},
		{
			name: "an offline window inside the suspect window",
			edit: func(v map[string]string) { v["CENTRAL_OFFLINE_AFTER"] = "30s" },
			want: "OFFLINE_AFTER",
		},
		{
			name: "a TURN URL with the wrong scheme",
			edit: func(v map[string]string) {
				v["CENTRAL_TURN_URLS"] = "https://relay.example:3478"
				v["CENTRAL_TURN_SECRET"] = "shared"
			},
			want: "TURN_URLS",
		},
		{
			name: "a STUN URL naming no host",
			edit: func(v map[string]string) { v["CENTRAL_STUN_URLS"] = "stun:" },
			want: "STUN_URLS",
		},
		{
			name: "TURN URLs with no secret to sign credentials with",
			edit: func(v map[string]string) { v["CENTRAL_TURN_URLS"] = "turn:relay.example:3478" },
			want: "TURN_SECRET",
		},
		{
			name: "a TURN secret with no relay to use it",
			edit: func(v map[string]string) { v["CENTRAL_TURN_SECRET"] = "orphaned" },
			want: "TURN_SECRET",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vars := valid()
			c.edit(vars)

			_, err := Load(env(vars))
			if err == nil {
				t.Fatalf("Load() error = nil, want a complaint about %s", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("Load() error = %q, want it to name %s", err, c.want)
			}
		})
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	vars := valid()
	delete(vars, "CENTRAL_TOKEN_SIGNING_KEY")
	vars["CENTRAL_LISTEN_ADDR"] = "localhost"

	_, err := Load(env(vars))
	if err == nil {
		t.Fatal("Load() error = nil, want both problems reported")
	}
	for _, want := range []string{"TOKEN_SIGNING_KEY", "LISTEN_ADDR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error = %q, want it to name %s", err, want)
		}
	}
}

func TestLoadWithoutAnEnvironment(t *testing.T) {
	if _, err := Load(nil); err == nil {
		t.Error("Load(nil) error = nil, want an error")
	}
}

func TestSplitList(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty is no entries", raw: "   ", want: nil},
		{name: "trims and drops empties", raw: " a , ,b, ", want: []string{"a", "b"}},
		{name: "a single value", raw: "only", want: []string{"only"}},
		{name: "commas alone are no entries", raw: ",,,", want: nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitList(c.raw)
			if len(got) != len(c.want) {
				t.Fatalf("splitList(%q) = %v, want %v", c.raw, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("splitList(%q)[%d] = %q, want %q", c.raw, i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestValidateICEURLs(t *testing.T) {
	cases := []struct {
		name     string
		urls     []string
		problems int
	}{
		{name: "a host and port", urls: []string{"stun:stun.example:3478"}, problems: 0},
		{name: "a host with no port", urls: []string{"stun:stun.example"}, problems: 0},
		{name: "the secure scheme", urls: []string{"stuns:stun.example:5349"}, problems: 0},
		{name: "a query the browser reads", urls: []string{"stun:stun.example:3478?transport=udp"}, problems: 0},
		{name: "the wrong scheme", urls: []string{"http://stun.example"}, problems: 1},
		{name: "no host", urls: []string{"stun:"}, problems: 1},
		{name: "each bad entry counted once", urls: []string{"stun:", "http://x"}, problems: 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateICEURLs("STUN_URLS", c.urls, "stun", "stuns")
			if len(got) != c.problems {
				t.Errorf("validateICEURLs(%v) = %v, want %d problems", c.urls, got, c.problems)
			}
		})
	}
}

func TestHostOf(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "stun:stun.example:3478", want: "stun.example"},
		{raw: "stun:stun.example", want: "stun.example"},
		{raw: "turns://relay.example:5349", want: "relay.example"},
		{raw: "stun:", want: ""},
	}

	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			u, err := url.Parse(c.raw)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", c.raw, err)
			}
			if got := hostOf(u); got != c.want {
				t.Errorf("hostOf(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestContainsFold(t *testing.T) {
	if !containsFold([]string{"turn", "turns"}, "TURNS") {
		t.Error("containsFold() = false for a case difference, want true")
	}
	if containsFold([]string{"turn"}, "stun") {
		t.Error("containsFold() = true for an absent value, want false")
	}
}

func TestStringKeepsTheSigningKeyOut(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	got := cfg.String()
	if strings.Contains(got, testSigningKey) || strings.Contains(got, string(cfg.TokenSigningKey)) {
		t.Errorf("String() = %q, want no key material in it", got)
	}
	if !strings.Contains(got, "signing_key=loaded") {
		t.Errorf("String() = %q, want it to report the key as loaded", got)
	}
}

func TestLoadReadsTheSigningKeyAndClients(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if len(cfg.TokenSigningKey) != ed25519.PrivateKeySize {
		t.Errorf("TokenSigningKey is %d bytes, want %d", len(cfg.TokenSigningKey), ed25519.PrivateKeySize)
	}
	if len(cfg.Clients) != 1 || cfg.Clients[0].PeerID != "peer-001" {
		t.Errorf("Clients = %+v, want the one configured client", cfg.Clients)
	}
	if cfg.Issuer == "" || cfg.AuthMaxFailures <= 0 || cfg.AuthFailureWindow <= 0 {
		t.Errorf("issuer/allowance/window = %q/%d/%s, want defaults filled in",
			cfg.Issuer, cfg.AuthMaxFailures, cfg.AuthFailureWindow)
	}
}

func TestLoadReadsTheSigningKeyAndClientsFromFiles(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signing.key")
	clientsPath := filepath.Join(dir, "clients.json")

	if err := os.WriteFile(keyPath, []byte(testSigningKey+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.WriteFile(clientsPath, []byte(testClients), 0o600); err != nil {
		t.Fatalf("write clients: %v", err)
	}

	cfg, err := Load(env(map[string]string{
		"CENTRAL_TOKEN_SIGNING_KEY_FILE": keyPath,
		"CENTRAL_CLIENTS_FILE":           clientsPath,
		"CENTRAL_ALLOWED_ORIGINS":        AnyOrigin,
	}))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if len(cfg.TokenSigningKey) != ed25519.PrivateKeySize || len(cfg.Clients) != 1 {
		t.Errorf("key = %d bytes, clients = %d, want a loaded key and one client",
			len(cfg.TokenSigningKey), len(cfg.Clients))
	}
}

func TestLoadReportsAFileItCannotRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")

	for _, key := range []string{"CENTRAL_TOKEN_SIGNING_KEY_FILE", "CENTRAL_CLIENTS_FILE"} {
		t.Run(key, func(t *testing.T) {
			vars := valid()
			vars[key] = missing

			if _, err := Load(env(vars)); err == nil {
				t.Errorf("Load() error = nil with %s pointing at nothing, want an error", key)
			}
		})
	}
}

func TestRelayConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "URLs and a secret", cfg: Config{TURNURLs: []string{"turn:r"}, TURNSecret: "s"}, want: true},
		{name: "URLs with no secret", cfg: Config{TURNURLs: []string{"turn:r"}}, want: false},
		{name: "a secret with no URLs", cfg: Config{TURNSecret: "s"}, want: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.RelayConfigured(); got != c.want {
				t.Errorf("RelayConfigured() = %t, want %t", got, c.want)
			}
		})
	}
}

func TestIssuerFallsBackWhenBlank(t *testing.T) {
	vars := valid()
	vars["CENTRAL_ISSUER"] = "   "

	cfg, err := Load(env(vars))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil — a blank issuer takes the default", err)
	}
	if cfg.Issuer != "dc-streaming-p2p" {
		t.Errorf("Issuer = %q, want the default", cfg.Issuer)
	}
}

func TestAnyOriginIsAskedForRatherThanAssumed(t *testing.T) {
	permissive, err := Load(env(valid()))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !permissive.AnyOriginAllowed() {
		t.Error("AnyOriginAllowed() = false for the wildcard, want true")
	}

	vars := valid()
	vars["CENTRAL_ALLOWED_ORIGINS"] = "https://app.example"
	named, err := Load(env(vars))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if named.AnyOriginAllowed() {
		t.Error("AnyOriginAllowed() = true for a named origin, want false")
	}

	if strings.Contains(named.String(), "any_origin=true") {
		t.Errorf("String() = %q, want it to report the origin decision truthfully", named.String())
	}
}
