package authclient

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCentral is a central server that publishes a key and answers credentials,
// recording what it was asked for.
type fakeCentral struct {
	server *httptest.Server

	mu       sync.Mutex
	paths    []string
	bodies   []string
	keySet   string
	authCode int
}

func newFakeCentral(t *testing.T, published ed25519.PublicKey) *fakeCentral {
	t.Helper()

	central := &fakeCentral{authCode: http.StatusOK}
	central.keySet = keySetFor(published)

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+JWKSPath, func(w http.ResponseWriter, _ *http.Request) {
		central.record(JWKSPath, "")
		w.Header().Set("Content-Type", "application/jwk-set+json")
		_, _ = w.Write([]byte(central.currentKeySet()))
	})
	mux.HandleFunc("POST "+AuthPath, func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 512)
		n, _ := r.Body.Read(body)
		central.record(AuthPath, string(body[:n]))

		if central.status() != http.StatusOK {
			w.WriteHeader(central.status())
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(Token{
			Value:     "a.token.value",
			PeerID:    "peer-001",
			ExpiresAt: time.Now().Add(time.Hour),
		})
	})

	central.server = httptest.NewServer(mux)
	t.Cleanup(central.server.Close)
	return central
}

func (f *fakeCentral) record(path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	if body != "" {
		f.bodies = append(f.bodies, body)
	}
}

func (f *fakeCentral) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *fakeCentral) sentBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func (f *fakeCentral) currentKeySet() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keySet
}

func (f *fakeCentral) publish(keySet string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keySet = keySet
}

func (f *fakeCentral) status() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authCode
}

func (f *fakeCentral) refuse(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authCode = status
}

// keySetFor renders a key set the way the central server does.
func keySetFor(keys ...ed25519.PublicKey) string {
	var entries []string
	for _, key := range keys {
		entries = append(entries, `{"kty":"OKP","crv":"Ed25519","use":"sig","alg":"EdDSA","x":"`+
			base64.RawURLEncoding.EncodeToString(key)+`"}`)
	}
	return `{"keys":[` + strings.Join(entries, ",") + `]}`
}

func mustKeys(t *testing.T) (ed25519.PublicKey, ed25519.PublicKey) {
	t.Helper()

	first, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	second, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	return first, second
}

func TestAuthenticateAgainstTheExpectedServer(t *testing.T) {
	expected, _ := mustKeys(t)
	central := newFakeCentral(t, expected)

	client, err := New(central.server.URL, expected, WithHTTPClient(central.server.Client()), WithInsecureTransport())
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	token, err := client.Authenticate(context.Background(), "sdk-web", "a-secret-long-enough-for-the-server")
	if err != nil {
		t.Fatalf("Authenticate() error = %v, want nil", err)
	}
	if token.Value == "" || token.PeerID != "peer-001" {
		t.Errorf("Authenticate() = %+v, want a token for peer-001", token)
	}

	asked := central.asked()
	if len(asked) < 2 || asked[0] != JWKSPath {
		t.Errorf("the client asked %v, want the key set first", asked)
	}
}

func TestAuthenticateSendsNothingToAServerWithTheWrongKey(t *testing.T) {
	expected, other := mustKeys(t)
	central := newFakeCentral(t, other)

	client, err := New(central.server.URL, expected, WithHTTPClient(central.server.Client()), WithInsecureTransport())
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	_, err = client.Authenticate(context.Background(), "sdk-web", "a-secret-long-enough-for-the-server")
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("Authenticate() error = %v, want ErrKeyMismatch", err)
	}

	for _, path := range central.asked() {
		if path == AuthPath {
			t.Error("the client sent a credential to a server whose key did not match")
		}
	}
	if bodies := central.sentBodies(); len(bodies) != 0 {
		t.Errorf("the client sent %v, want nothing at all", bodies)
	}
}

func TestVerifyServerKeyAcceptsAKeySetInRotation(t *testing.T) {
	expected, other := mustKeys(t)
	central := newFakeCentral(t, other)
	central.publish(keySetFor(other, expected))

	client, err := New(central.server.URL, expected, WithHTTPClient(central.server.Client()), WithInsecureTransport())
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	if err := client.VerifyServerKey(context.Background()); err != nil {
		t.Errorf("VerifyServerKey() error = %v, want nil when the expected key is one of several published", err)
	}
}

func TestVerifyServerKeyIsAskedOnce(t *testing.T) {
	expected, _ := mustKeys(t)
	central := newFakeCentral(t, expected)

	client, err := New(central.server.URL, expected, WithHTTPClient(central.server.Client()), WithInsecureTransport())
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	for range 3 {
		if err := client.VerifyServerKey(context.Background()); err != nil {
			t.Fatalf("VerifyServerKey() error = %v, want nil", err)
		}
	}

	var fetches int
	for _, path := range central.asked() {
		if path == JWKSPath {
			fetches++
		}
	}
	if fetches != 1 {
		t.Errorf("the key set was fetched %d times, want once — it is pinned after the first", fetches)
	}
}

func TestVerifyServerKeyRefuses(t *testing.T) {
	expected, _ := mustKeys(t)

	cases := []struct {
		name   string
		keySet string
	}{
		{name: "an empty set", keySet: `{"keys":[]}`},
		{name: "a set with a key of another type", keySet: `{"keys":[{"kty":"RSA","n":"…"}]}`},
		{name: "a key that is not base64", keySet: `{"keys":[{"kty":"OKP","crv":"Ed25519","x":"not base64"}]}`},
		{name: "a key of the wrong length", keySet: `{"keys":[{"kty":"OKP","crv":"Ed25519","x":"c2hvcnQ"}]}`},
		{name: "a body that is not a key set", keySet: `not json`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			central := newFakeCentral(t, expected)
			central.publish(c.keySet)

			client, err := New(central.server.URL, expected, WithHTTPClient(central.server.Client()), WithInsecureTransport())
			if err != nil {
				t.Fatalf("New() error = %v, want nil", err)
			}

			if err := client.VerifyServerKey(context.Background()); err == nil {
				t.Error("VerifyServerKey() error = nil, want a refusal")
			}
		})
	}
}

func TestAuthenticateReportsARefusal(t *testing.T) {
	expected, _ := mustKeys(t)
	central := newFakeCentral(t, expected)
	central.refuse(http.StatusUnauthorized)

	client, err := New(central.server.URL, expected, WithHTTPClient(central.server.Client()), WithInsecureTransport())
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	_, err = client.Authenticate(context.Background(), "sdk-web", "wrong")
	if !errors.Is(err, ErrRefused) {
		t.Errorf("Authenticate() error = %v, want ErrRefused", err)
	}
}

func TestNewRejects(t *testing.T) {
	expected, _ := mustKeys(t)

	cases := []struct {
		name string
		url  string
		key  ed25519.PublicKey
	}{
		{name: "no URL", url: "", key: expected},
		{name: "a URL with no scheme", url: "central.example", key: expected},
		{name: "no expected key", url: "https://central.example", key: nil},
		{name: "a key of the wrong length", url: "https://central.example", key: ed25519.PublicKey("short")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.url, c.key); err == nil {
				t.Error("New() error = nil, want a refusal")
			}
		})
	}
}

func TestAuthenticateHonoursACancelledContext(t *testing.T) {
	expected, _ := mustKeys(t)
	central := newFakeCentral(t, expected)

	client, err := New(central.server.URL, expected, WithHTTPClient(central.server.Client()), WithInsecureTransport())
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Authenticate(ctx, "sdk-web", "secret"); err == nil {
		t.Error("Authenticate() error = nil with a cancelled context, want an error")
	}
}

func TestNewRefusesPlainHTTPUnlessAsked(t *testing.T) {
	expected, _ := mustKeys(t)

	if _, err := New("http://central.example", expected); err == nil {
		t.Error("New() error = nil for an http:// server, want a refusal — the credential goes out before any token exists")
	}
	if _, err := New("http://central.example", expected, WithInsecureTransport()); err != nil {
		t.Errorf("New() error = %v with WithInsecureTransport, want nil", err)
	}
	if _, err := New("https://central.example", expected); err != nil {
		t.Errorf("New() error = %v for an https:// server, want nil", err)
	}
}
