package transport

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/protonspy/dc-streaming-p2p/internal/signaling"
)

// pairedSessions is one session between two peers.
type pairedSessions map[string][2]string

func (p pairedSessions) Other(sessionID, peerID string) (string, bool) {
	pair, ok := p[sessionID]
	if !ok {
		return "", false
	}
	switch peerID {
	case pair[0]:
		return pair[1], true
	case pair[1]:
		return pair[0], true
	default:
		return "", false
	}
}

// signalFixture is the signaling endpoint on a real server, reachable by a real
// WebSocket client.
type signalFixture struct {
	server *httptest.Server
	hub    *signaling.Hub
	auth   authFixture
}

func newSignalFixture(t *testing.T, origins []string) signalFixture {
	t.Helper()

	return newSignalFixtureWith(t, SignalDeps{AllowedOrigins: origins, AllowAnyOrigin: len(origins) == 0})
}

// newSignalFixtureWith builds the endpoint with limits a test chooses.
func newSignalFixtureWith(t *testing.T, deps SignalDeps) signalFixture {
	t.Helper()

	hub, err := signaling.New(signaling.Options{
		Sessions: pairedSessions{"sess-1": {"peer-001", "peer-002"}},
	})
	if err != nil {
		t.Fatalf("signaling.New() error = %v, want nil", err)
	}

	auth := newAuthFixture(t, 10)
	deps.Hub = hub
	deps.Verifier = auth.verifier
	if deps.PingInterval == 0 {
		deps.PingInterval = time.Hour
	}
	deps.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Signal(deps)

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return signalFixture{server: server, hub: hub, auth: auth}
}

// dial opens a signaling channel as one peer.
func (f signalFixture) dial(t *testing.T, peerID string) *websocket.Conn {
	t.Helper()

	token, err := f.auth.issuer.Issue(peerID)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	return f.dialWithToken(t, token.Value)
}

func (f signalFixture) dialWithToken(t *testing.T, token string) *websocket.Conn {
	t.Helper()

	conn, resp, err := websocket.Dial(context.Background(), wsURL(f.server.URL), &websocket.DialOptions{
		Subprotocols: []string{Subprotocol, tokenPrefix + token},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("Dial() error = %v (status %d), want nil", err, status)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

// read waits for one message, failing the test if none arrives.
func read(t *testing.T, conn *websocket.Conn) signaling.Message {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var msg signaling.Message
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("reading a message: %v", err)
	}
	return msg
}

// waitForChannels blocks until the hub holds the expected number of channels.
func waitForChannels(t *testing.T, hub *signaling.Hub, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.Count() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the hub holds %d channels, want %d", hub.Count(), want)
}

func TestSignalCarriesAnOfferToTheOtherPeer(t *testing.T) {
	f := newSignalFixture(t, nil)

	caller := f.dial(t, "peer-001")
	callee := f.dial(t, "peer-002")
	waitForChannels(t, f.hub, 2)

	sent := signaling.Message{
		Type:      signaling.Offer,
		SessionID: "sess-1",
		Payload:   json.RawMessage(`{"sdp":"v=0…","type":"offer"}`),
	}
	if err := wsjson.Write(context.Background(), caller, sent); err != nil {
		t.Fatalf("writing the offer: %v", err)
	}

	got := read(t, callee)
	if got.Type != signaling.Offer || got.SessionID != "sess-1" {
		t.Errorf("received %+v, want the offer for sess-1", got)
	}
	if got.From != "peer-001" {
		t.Errorf("from = %q, want the sending peer", got.From)
	}
	if string(got.Payload) != string(sent.Payload) {
		t.Errorf("payload = %s, want it carried through byte for byte", got.Payload)
	}
}

func TestSignalRefusesAConnectionWithoutAToken(t *testing.T) {
	f := newSignalFixture(t, nil)

	_, resp, err := websocket.Dial(context.Background(), wsURL(f.server.URL), &websocket.DialOptions{
		Subprotocols: []string{Subprotocol},
	})
	if err == nil {
		t.Fatal("Dial() error = nil with no token, want the connection refused")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %v, want %d before any upgrade", resp, http.StatusUnauthorized)
	}
}

func TestSignalRefusesATokenItDidNotSign(t *testing.T) {
	f := newSignalFixture(t, nil)

	_, resp, err := websocket.Dial(context.Background(), wsURL(f.server.URL), &websocket.DialOptions{
		Subprotocols: []string{Subprotocol, tokenPrefix + "not-a-token"},
	})
	if err == nil {
		t.Fatal("Dial() error = nil with a forged token, want the connection refused")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %v, want %d", resp, http.StatusUnauthorized)
	}
}

func TestSignalRefusesAnOriginThatIsNotAllowed(t *testing.T) {
	f := newSignalFixture(t, []string{"allowed.example"})

	token, err := f.auth.issuer.Issue("peer-001")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	header := http.Header{}
	header.Set("Origin", "http://somewhere-else.example")

	_, _, err = websocket.Dial(context.Background(), wsURL(f.server.URL), &websocket.DialOptions{
		Subprotocols: []string{Subprotocol, tokenPrefix + token.Value},
		HTTPHeader:   header,
	})
	if err == nil {
		t.Error("Dial() error = nil from an origin nobody allowed, want the connection refused")
	}
}

func TestSignalTellsASenderThatTheOtherPeerIsNotConnected(t *testing.T) {
	f := newSignalFixture(t, nil)

	caller := f.dial(t, "peer-001")
	waitForChannels(t, f.hub, 1)

	if err := wsjson.Write(context.Background(), caller, signaling.Message{
		Type:      signaling.Offer,
		SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("writing the offer: %v", err)
	}

	got := read(t, caller)
	if got.Type != signaling.Error || got.Code != signaling.CodePeerNotConnected {
		t.Errorf("received %+v, want an error saying the peer is not connected", got)
	}
}

func TestSignalRefusesASessionTheSenderIsNotIn(t *testing.T) {
	f := newSignalFixture(t, nil)

	stranger := f.dial(t, "peer-003")
	waitForChannels(t, f.hub, 1)

	if err := wsjson.Write(context.Background(), stranger, signaling.Message{
		Type:      signaling.Offer,
		SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("writing the offer: %v", err)
	}

	got := read(t, stranger)
	if got.Type != signaling.Error || got.Code != signaling.CodeNotInSession {
		t.Errorf("received %+v, want an error saying the sender is not in that session", got)
	}
}

func TestASecondChannelReplacesTheFirst(t *testing.T) {
	f := newSignalFixture(t, nil)

	first := f.dial(t, "peer-001")
	waitForChannels(t, f.hub, 1)

	second := f.dial(t, "peer-001")

	// The older connection is closed by the server, which the client sees as a
	// read failing.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var msg signaling.Message
	if err := wsjson.Read(ctx, first, &msg); err == nil {
		t.Error("the older channel is still readable; a peer that reconnects must not be stranded behind it")
	}

	waitForChannels(t, f.hub, 1)
	if err := wsjson.Write(context.Background(), second, signaling.Message{
		Type:      signaling.Offer,
		SessionID: "sess-1",
	}); err != nil {
		t.Errorf("the newer channel is not usable: %v", err)
	}
}

func TestClosingTheChannelDeregistersThePeer(t *testing.T) {
	f := newSignalFixture(t, nil)

	conn := f.dial(t, "peer-001")
	waitForChannels(t, f.hub, 1)

	if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatalf("closing: %v", err)
	}

	waitForChannels(t, f.hub, 0)
}

func TestTokenFromSubprotocols(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "the token beside the protocol", values: []string{Subprotocol + ", " + tokenPrefix + "abc"}, want: "abc"},
		{name: "separate headers", values: []string{Subprotocol, tokenPrefix + "abc"}, want: "abc"},
		{name: "no token", values: []string{Subprotocol}, want: ""},
		{name: "an empty token", values: []string{tokenPrefix}, want: ""},
		{name: "nothing offered", values: nil, want: ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/signal", nil)
			for _, value := range c.values {
				req.Header.Add("Sec-WebSocket-Protocol", value)
			}

			got, found := tokenFromSubprotocols(req)
			if got != c.want || found != (c.want != "") {
				t.Errorf("tokenFromSubprotocols() = %q, %t, want %q", got, found, c.want)
			}
		})
	}
}

func TestSignalCloseReasonIsBounded(t *testing.T) {
	f := newSignalFixture(t, nil)
	conn := f.dial(t, "peer-001")
	waitForChannels(t, f.hub, 1)

	channel := &wsConn{conn: conn}
	// A close frame carries at most 123 bytes; a longer reason must be cut rather
	// than making the close fail.
	channel.Close(strings.Repeat("x", 200))
}

func TestAMessagePastTheLimitClosesTheConnection(t *testing.T) {
	f := newSignalFixtureWith(t, SignalDeps{AllowAnyOrigin: true, MaxMessageBytes: 1 << 10})

	conn := f.dial(t, "peer-001")
	waitForChannels(t, f.hub, 1)

	// Well past the limit, and nothing like signaling.
	huge := signaling.Message{
		Type:      signaling.Offer,
		SessionID: "sess-1",
		Payload:   json.RawMessage(`"` + strings.Repeat("x", 4<<10) + `"`),
	}
	// The write itself may succeed — the server refuses on read — so what is
	// asserted is that the channel does not survive it.
	_ = wsjson.Write(context.Background(), conn, huge)

	waitForChannels(t, f.hub, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var reply signaling.Message
	if err := wsjson.Read(ctx, conn, &reply); err == nil {
		t.Errorf("the channel is still readable after a message past the limit, and answered %+v", reply)
	}
}

func TestAChannelThatStopsAnsweringTheHeartbeatIsClosed(t *testing.T) {
	f := newSignalFixtureWith(t, SignalDeps{
		AllowAnyOrigin: true,
		PingInterval:   20 * time.Millisecond,
		PongTimeout:    50 * time.Millisecond,
	})

	conn := f.dial(t, "peer-001")
	waitForChannels(t, f.hub, 1)

	// A WebSocket client answers pings from inside its read loop, so a peer that
	// never reads is exactly a peer whose process is wedged: the socket is open
	// and nobody is home. Nothing here reads from conn.
	waitForChannels(t, f.hub, 0)

	_ = conn.CloseNow()
}
