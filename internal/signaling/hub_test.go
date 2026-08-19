package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeConn records what was sent to it and can be told to fail.
type fakeConn struct {
	mu       sync.Mutex
	received []Message
	closed   bool
	reason   string
	fail     error
	blockFor time.Duration
}

func (f *fakeConn) Send(ctx context.Context, msg Message) error {
	if f.blockFor > 0 {
		select {
		case <-time.After(f.blockFor):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.received = append(f.received, msg)
	return nil
}

func (f *fakeConn) Close(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.reason = reason
}

func (f *fakeConn) messages() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.received...)
}

func (f *fakeConn) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// fakeSessions answers who the other side of a session is.
type fakeSessions struct {
	// pairs maps a session identifier to its two peers.
	pairs map[string][2]string
}

func (f fakeSessions) Other(sessionID, peerID string) (string, bool) {
	pair, ok := f.pairs[sessionID]
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

// fakePresence records what the hub said about who is connected.
type fakePresence struct {
	mu           sync.Mutex
	connected    []string
	disconnected []string
}

func (f *fakePresence) Connected(peerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = append(f.connected, peerID)
}

func (f *fakePresence) Disconnected(peerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = append(f.disconnected, peerID)
}

func (f *fakePresence) seen() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.connected...), append([]string(nil), f.disconnected...)
}

// testHub is a hub over one session between peer-001 and peer-002.
func testHub(t *testing.T, opts Options) (*Hub, *fakeSessions) {
	t.Helper()

	sessions := &fakeSessions{pairs: map[string][2]string{
		"sess-1": {"peer-001", "peer-002"},
	}}
	if opts.Sessions == nil {
		opts.Sessions = sessions
	}

	hub, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	return hub, sessions
}

func TestRouteDeliversToTheOtherPeer(t *testing.T) {
	hub, _ := testHub(t, Options{})

	sender, receiver, outsider := &fakeConn{}, &fakeConn{}, &fakeConn{}
	hub.Register("peer-001", sender)
	hub.Register("peer-002", receiver)
	hub.Register("peer-003", outsider)

	err := hub.Route(context.Background(), "peer-001", Message{
		Type:      Offer,
		SessionID: "sess-1",
		Payload:   json.RawMessage(`{"sdp":"v=0…"}`),
	})
	if err != nil {
		t.Fatalf("Route() error = %v, want nil", err)
	}

	delivered := receiver.messages()
	if len(delivered) != 1 {
		t.Fatalf("the other peer received %d messages, want 1", len(delivered))
	}
	if delivered[0].Type != Offer || delivered[0].SessionID != "sess-1" {
		t.Errorf("delivered %+v, want the offer for sess-1", delivered[0])
	}
	if delivered[0].From != "peer-001" {
		t.Errorf("from = %q, want the sending peer", delivered[0].From)
	}
	if len(outsider.messages()) != 0 {
		t.Error("a peer outside the session received the message")
	}
	if len(sender.messages()) != 0 {
		t.Errorf("the sender received %+v, want nothing back on a delivery", sender.messages())
	}
}

func TestRouteNamesTheSenderFromTheConnection(t *testing.T) {
	hub, _ := testHub(t, Options{})

	hub.Register("peer-001", &fakeConn{})
	receiver := &fakeConn{}
	hub.Register("peer-002", receiver)

	if err := hub.Route(context.Background(), "peer-001", Message{
		Type:      Candidate,
		SessionID: "sess-1",
		From:      "peer-999",
	}); err != nil {
		t.Fatalf("Route() error = %v, want nil", err)
	}

	delivered := receiver.messages()
	if len(delivered) != 1 || delivered[0].From != "peer-001" {
		t.Errorf("from = %+v, want the connection's identity rather than what the message claimed", delivered)
	}
}

func TestRouteCarriesThePayloadUntouched(t *testing.T) {
	hub, _ := testHub(t, Options{})

	hub.Register("peer-001", &fakeConn{})
	receiver := &fakeConn{}
	hub.Register("peer-002", receiver)

	payload := json.RawMessage(`{"candidate":"candidate:842163049 1 udp …","sdpMLineIndex":0,"unknown":{"nested":[1,2,3]}}`)
	if err := hub.Route(context.Background(), "peer-001", Message{
		Type:      Candidate,
		SessionID: "sess-1",
		Payload:   payload,
	}); err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	delivered := receiver.messages()
	if len(delivered) != 1 {
		t.Fatalf("received %d messages, want 1", len(delivered))
	}
	if string(delivered[0].Payload) != string(payload) {
		t.Errorf("payload = %s, want it carried through byte for byte", delivered[0].Payload)
	}
}

func TestRouteRefuses(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		code string
	}{
		{
			name: "a session the sender is not in",
			msg:  Message{Type: Offer, SessionID: "sess-somebody-else"},
			code: CodeNotInSession,
		},
		{
			name: "no session at all",
			msg:  Message{Type: Offer},
			code: CodeNotInSession,
		},
		{
			name: "a type the server does not carry",
			msg:  Message{Type: "shout", SessionID: "sess-1"},
			code: CodeUnknownType,
		},
		{
			name: "an error, which is the server's to send",
			msg:  Message{Type: Error, SessionID: "sess-1"},
			code: CodeUnknownType,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hub, _ := testHub(t, Options{})

			sender, receiver := &fakeConn{}, &fakeConn{}
			hub.Register("peer-001", sender)
			hub.Register("peer-002", receiver)

			if err := hub.Route(context.Background(), "peer-001", c.msg); err != nil {
				t.Fatalf("Route() error = %v, want the sender told instead", err)
			}

			told := sender.messages()
			if len(told) != 1 || told[0].Type != Error || told[0].Code != c.code {
				t.Errorf("the sender was told %+v, want an error with code %q", told, c.code)
			}
			if len(receiver.messages()) != 0 {
				t.Error("the other peer received a message that should not have been carried")
			}
		})
	}
}

func TestRouteTellsTheSenderWhenTheOtherPeerHasNoChannel(t *testing.T) {
	hub, _ := testHub(t, Options{})

	sender := &fakeConn{}
	hub.Register("peer-001", sender)

	if err := hub.Route(context.Background(), "peer-001", Message{Type: Offer, SessionID: "sess-1"}); err != nil {
		t.Fatalf("Route() error = %v, want the sender told instead", err)
	}

	told := sender.messages()
	if len(told) != 1 || told[0].Code != CodePeerNotConnected {
		t.Errorf("the sender was told %+v, want %q — nothing is queued", told, CodePeerNotConnected)
	}
}

func TestASecondConnectionReplacesTheFirst(t *testing.T) {
	hub, _ := testHub(t, Options{})

	first, second := &fakeConn{}, &fakeConn{}
	hub.Register("peer-001", first)
	hub.Register("peer-001", second)

	if !first.isClosed() {
		t.Error("the older connection is still open; a peer that reconnects must not be stranded behind it")
	}
	if second.isClosed() {
		t.Error("the newer connection was closed")
	}
	if hub.Count() != 1 {
		t.Errorf("Count() = %d, want one channel for one peer", hub.Count())
	}
}

func TestDeregisterOnlyRemovesTheConnectionItWasGiven(t *testing.T) {
	hub, _ := testHub(t, Options{})

	first, second := &fakeConn{}, &fakeConn{}
	hub.Register("peer-001", first)
	hub.Register("peer-001", second)

	// The replaced connection tidies up after itself and must not take its
	// replacement with it.
	hub.Deregister("peer-001", first)

	if !hub.Connected("peer-001") {
		t.Error("the replacement was removed by the connection it replaced")
	}

	hub.Deregister("peer-001", second)
	if hub.Connected("peer-001") {
		t.Error("the peer is still connected after its own connection deregistered")
	}
}

func TestPresenceFollowsTheChannel(t *testing.T) {
	presence := &fakePresence{}
	hub, _ := testHub(t, Options{Presence: presence})

	conn := &fakeConn{}
	hub.Register("peer-001", conn)
	hub.Deregister("peer-001", conn)

	connected, disconnected := presence.seen()
	if len(connected) != 1 || connected[0] != "peer-001" {
		t.Errorf("connected = %v, want the peer named once", connected)
	}
	if len(disconnected) != 1 || disconnected[0] != "peer-001" {
		t.Errorf("disconnected = %v, want the peer named once", disconnected)
	}
}

func TestRouteRefusesPastTheAllowance(t *testing.T) {
	hub, _ := testHub(t, Options{MessagesPerWindow: 3, Window: time.Minute})

	sender, receiver := &fakeConn{}, &fakeConn{}
	hub.Register("peer-001", sender)
	hub.Register("peer-002", receiver)

	for range 3 {
		if err := hub.Route(context.Background(), "peer-001", Message{Type: Candidate, SessionID: "sess-1"}); err != nil {
			t.Fatalf("Route() error = %v", err)
		}
	}

	if err := hub.Route(context.Background(), "peer-001", Message{Type: Candidate, SessionID: "sess-1"}); err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	told := sender.messages()
	if len(told) != 1 || told[0].Code != CodeTooFast {
		t.Errorf("the sender was told %+v, want %q", told, CodeTooFast)
	}
	if sender.isClosed() {
		t.Error("the connection was closed for sending too fast; slowing down is the answer, not reconnecting")
	}
	if len(receiver.messages()) != 3 {
		t.Errorf("the other peer received %d messages, want the three inside the allowance", len(receiver.messages()))
	}
}

func TestTheAllowanceRefillsWithTheWindow(t *testing.T) {
	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	hub, _ := testHub(t, Options{
		MessagesPerWindow: 1,
		Window:            time.Minute,
		Now:               func() time.Time { return clock },
	})

	sender, receiver := &fakeConn{}, &fakeConn{}
	hub.Register("peer-001", sender)
	hub.Register("peer-002", receiver)

	for range 2 {
		if err := hub.Route(context.Background(), "peer-001", Message{Type: Candidate, SessionID: "sess-1"}); err != nil {
			t.Fatalf("Route() error = %v", err)
		}
	}
	clock = clock.Add(2 * time.Minute)
	if err := hub.Route(context.Background(), "peer-001", Message{Type: Candidate, SessionID: "sess-1"}); err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	if got := len(receiver.messages()); got != 2 {
		t.Errorf("the other peer received %d messages, want one from each window", got)
	}
}

func TestADeliveryThatFailsClosesTheConnectionItFailedOn(t *testing.T) {
	hub, _ := testHub(t, Options{})

	sender := &fakeConn{}
	receiver := &fakeConn{fail: errors.New("socket is gone")}
	hub.Register("peer-001", sender)
	hub.Register("peer-002", receiver)

	if err := hub.Route(context.Background(), "peer-001", Message{Type: Offer, SessionID: "sess-1"}); err != nil {
		t.Fatalf("Route() error = %v, want the sender told instead", err)
	}

	if !receiver.isClosed() {
		t.Error("the connection that could not be written to is still held")
	}
	told := sender.messages()
	if len(told) != 1 || told[0].Code != CodePeerNotConnected {
		t.Errorf("the sender was told %+v, want %q", told, CodePeerNotConnected)
	}
}

func TestADeliveryIsBoundedByTheSendTimeout(t *testing.T) {
	hub, _ := testHub(t, Options{SendTimeout: 20 * time.Millisecond})

	sender := &fakeConn{}
	slow := &fakeConn{blockFor: 2 * time.Second}
	hub.Register("peer-001", sender)
	hub.Register("peer-002", slow)

	done := make(chan struct{})
	go func() {
		_ = hub.Route(context.Background(), "peer-001", Message{Type: Offer, SessionID: "sess-1"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Route() waited on a peer that stopped reading; a slow receiver must not hold up the sender")
	}
}

func TestSessionEndedTellsBothPeers(t *testing.T) {
	hub, _ := testHub(t, Options{})

	first, second := &fakeConn{}, &fakeConn{}
	hub.Register("peer-001", first)
	hub.Register("peer-002", second)

	hub.SessionEnded(context.Background(), "sess-1", "closed", "peer-001", "peer-002")

	for name, conn := range map[string]*fakeConn{"peer-001": first, "peer-002": second} {
		told := conn.messages()
		if len(told) != 1 || told[0].Type != SessionState || told[0].State != "closed" {
			t.Errorf("%s was told %+v, want the session reported closed", name, told)
		}
	}
}

func TestSessionEndedIsQuietForAPeerWithNoChannel(t *testing.T) {
	hub, _ := testHub(t, Options{})

	held := &fakeConn{}
	hub.Register("peer-001", held)

	// peer-002 holds no channel, and that is not an error worth reporting.
	hub.SessionEnded(context.Background(), "sess-1", "closed", "peer-001", "peer-002")

	if len(held.messages()) != 1 {
		t.Errorf("the connected peer was told %d times, want once", len(held.messages()))
	}
}

func TestCountAndConnected(t *testing.T) {
	hub, _ := testHub(t, Options{})

	if hub.Count() != 0 || hub.Connected("peer-001") {
		t.Error("a new hub reports channels it does not hold")
	}

	conn := &fakeConn{}
	hub.Register("peer-001", conn)

	if hub.Count() != 1 || !hub.Connected("peer-001") {
		t.Errorf("Count() = %d, Connected() = %t, want one open channel", hub.Count(), hub.Connected("peer-001"))
	}
}

func TestRouteFromAPeerWithNoChannel(t *testing.T) {
	hub, _ := testHub(t, Options{})

	// Nothing to answer on, so the error is returned rather than sent.
	err := hub.Route(context.Background(), "peer-001", Message{Type: Offer, SessionID: "sess-1"})
	if !errors.Is(err, ErrNoConnection) {
		t.Errorf("Route() error = %v, want ErrNoConnection", err)
	}
}

func TestNewRefusesAHubWithNothingToAsk(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("New() error = nil with no session store, want a refusal")
	}
}

func TestHubIsSafeUnderConcurrentUse(t *testing.T) {
	sessions := fakeSessions{pairs: map[string][2]string{"sess-1": {"peer-001", "peer-002"}}}
	hub, err := New(Options{Sessions: sessions, MessagesPerWindow: 10_000})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			peerID := "peer-00" + strconv.Itoa(1+n%2)
			conn := &fakeConn{}

			hub.Register(peerID, conn)
			_ = hub.Route(context.Background(), peerID, Message{Type: Candidate, SessionID: "sess-1"})
			hub.Count()
			hub.Connected(peerID)
			hub.Deregister(peerID, conn)
		}(i)
	}
	wg.Wait()
}
