// Package signaling carries the negotiation between two peers and nothing else:
// it knows which peer holds which connection, and it delivers a message to the
// other side of a session without reading what is in it.
package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// Type is what a signaling message is for. These four are the whole of what two
// browsers need to negotiate a peer connection.
type Type string

const (
	// Offer carries the calling side's session description.
	Offer Type = "offer"
	// Answer carries the answering side's.
	Answer Type = "answer"
	// Candidate carries one ICE candidate.
	Candidate Type = "candidate"
	// Bye says the sender is done with this session.
	Bye Type = "bye"
	// Error is the server telling a sender that its message went nowhere.
	Error Type = "error"
	// SessionState is the server telling a peer that a session moved.
	SessionState Type = "session"
)

// Message is the envelope. The payload is carried through untouched: a server that
// parses a session description is one that has to be updated whenever the browsers
// change theirs.
type Message struct {
	Type      Type   `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	// From is written by the server from the connection's identity. A From on an
	// inbound message is ignored rather than refused: the SDK has no reason to set
	// it, and refusing would be one more thing to get wrong on a path that already
	// knows who is talking.
	From    string          `json:"from,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// Code names why a message could not be delivered, on an Error.
	Code string `json:"code,omitempty"`
	// State is the session's state, on a SessionState.
	State string `json:"state,omitempty"`
}

// Error codes the hub sends back to a sender.
const (
	// CodeNotInSession is a message naming a session the sender is not in, or
	// naming none at all.
	CodeNotInSession = "not_in_session"
	// CodePeerNotConnected is the other peer holding no channel. Nothing is
	// queued: a signaling message is only meaningful while both sides are
	// negotiating.
	CodePeerNotConnected = "peer_not_connected"
	// CodeUnknownType is a type this server does not carry.
	CodeUnknownType = "unknown_type"
	// CodeTooFast is a sender past its allowance in the window.
	CodeTooFast = "too_many_messages"
)

// Errors the hub returns to its caller.
var (
	// ErrNoConnection is a peer the hub is not holding a connection for.
	ErrNoConnection = errors.New("signaling: peer has no channel")
	// ErrNotDelivered is a message that could not be written to the far side.
	ErrNotDelivered = errors.New("signaling: not delivered")
)

// Conn is one peer's connection, as the hub uses it. It is an interface so the hub
// can be tested without a socket, and so the WebSocket library stays in the
// transport layer where it belongs.
type Conn interface {
	// Send writes one message. It is called from whichever goroutine is
	// delivering, so an implementation has to be safe for concurrent use and has
	// to bound how long it will wait.
	Send(ctx context.Context, msg Message) error
	// Close ends the connection, saying why.
	Close(reason string)
}

// Sessions is what the hub asks about a session: whether the sender is in it, and
// who the other side is.
type Sessions interface {
	// Other returns the other peer in the session, and whether the asking peer is
	// in it at all.
	Other(sessionID, peerID string) (string, bool)
}

// Presence is what the hub tells about a peer being connected, so the registry can
// keep it online without the peer reporting in separately.
type Presence interface {
	Connected(peerID string)
	Disconnected(peerID string)
}

// Options are the hub's limits.
type Options struct {
	// Sessions answers who the other side of a session is. Required.
	Sessions Sessions
	// Presence is told when a peer connects and disconnects. Optional.
	Presence Presence
	// MessagesPerWindow is how many messages one connection may send inside
	// Window before the excess is refused.
	MessagesPerWindow int
	// Window is the span those messages are counted in.
	Window time.Duration
	// SendTimeout bounds one delivery. A peer that stops reading must not be able
	// to hold up the peer writing to it.
	SendTimeout time.Duration
	// Now is the clock, injected so a test can hold it still.
	Now func() time.Time
}

// Defaults applied when Options leaves a limit unset.
const (
	DefaultMessagesPerWindow = 120
	DefaultWindow            = time.Minute
	DefaultSendTimeout       = 5 * time.Second
)

// Hub holds one connection per peer.
type Hub struct {
	mu    sync.Mutex
	conns map[string]*holder

	sessions    Sessions
	presence    Presence
	allowance   int
	window      time.Duration
	sendTimeout time.Duration
	now         func() time.Time
}

// holder is one connection and what its sender has spent.
type holder struct {
	conn       Conn
	sent       int
	windowFrom time.Time
}

// New refuses a hub that cannot answer the one question it exists to ask.
func New(opts Options) (*Hub, error) {
	if opts.Sessions == nil {
		return nil, errors.New("signaling: no session store to ask who the other peer is")
	}

	allowance := opts.MessagesPerWindow
	if allowance <= 0 {
		allowance = DefaultMessagesPerWindow
	}
	window := opts.Window
	if window <= 0 {
		window = DefaultWindow
	}
	sendTimeout := opts.SendTimeout
	if sendTimeout <= 0 {
		sendTimeout = DefaultSendTimeout
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Hub{
		conns:       make(map[string]*holder),
		sessions:    opts.Sessions,
		presence:    opts.Presence,
		allowance:   allowance,
		window:      window,
		sendTimeout: sendTimeout,
		now:         now,
	}, nil
}

// Register takes a peer's connection. A second connection for a peer closes the
// first: refusing the new one instead would strand a peer whose old connection is
// half-dead, and reconnecting is exactly what a peer does when it doubts.
func (h *Hub) Register(peerID string, conn Conn) {
	h.mu.Lock()
	existing, held := h.conns[peerID]
	h.conns[peerID] = &holder{conn: conn, windowFrom: h.now()}
	h.mu.Unlock()

	if held {
		existing.conn.Close("replaced by a newer connection")
	}
	if h.presence != nil {
		h.presence.Connected(peerID)
	}
}

// Deregister drops a peer's connection, if the one given is still the one held. A
// connection that was already replaced must not remove its replacement on the way
// out.
func (h *Hub) Deregister(peerID string, conn Conn) {
	h.mu.Lock()
	held, ok := h.conns[peerID]
	if ok && held.conn == conn {
		delete(h.conns, peerID)
	} else {
		ok = false
	}
	h.mu.Unlock()

	if ok && h.presence != nil {
		h.presence.Disconnected(peerID)
	}
}

// Count is how many channels are open.
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.conns)
}

// Connected reports whether a peer holds a channel.
func (h *Hub) Connected(peerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	_, held := h.conns[peerID]
	return held
}

// Peers names every peer holding a channel. It is what lets the registry be told
// that a connected peer is still there without that peer reporting in separately.
func (h *Hub) Peers() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	peers := make([]string, 0, len(h.conns))
	for peerID := range h.conns {
		peers = append(peers, peerID)
	}
	return peers
}

// Route carries one message from a peer to the other side of the session it names.
// It answers the sender rather than returning an error for anything the sender is
// expected to see: a refusal is a message, not a broken connection.
func (h *Hub) Route(ctx context.Context, from string, msg Message) error {
	if !carriable(msg.Type) {
		return h.tell(ctx, from, msg.SessionID, CodeUnknownType)
	}

	if !h.allowed(from) {
		return h.tell(ctx, from, msg.SessionID, CodeTooFast)
	}

	other, inSession := h.sessions.Other(msg.SessionID, from)
	if msg.SessionID == "" || !inSession {
		return h.tell(ctx, from, msg.SessionID, CodeNotInSession)
	}

	// The sender is named by the connection it came in on, never by what the
	// message claimed.
	msg.From = from
	msg.Code = ""
	msg.State = ""

	if err := h.send(ctx, other, msg); err != nil {
		return h.tell(ctx, from, msg.SessionID, CodePeerNotConnected)
	}
	return nil
}

// Tell sends a peer a message from the server itself, such as a session that
// ended. A peer with no channel is not an error worth reporting: it will read the
// session's state when it comes back.
func (h *Hub) Tell(ctx context.Context, peerID string, msg Message) {
	_ = h.send(ctx, peerID, msg)
}

// SessionEnded tells both peers in a session that it has ended.
func (h *Hub) SessionEnded(ctx context.Context, sessionID, state string, peers ...string) {
	for _, peerID := range peers {
		h.Tell(ctx, peerID, Message{Type: SessionState, SessionID: sessionID, State: state})
	}
}

// tell answers the sender with an error code.
func (h *Hub) tell(ctx context.Context, peerID, sessionID, code string) error {
	return h.send(ctx, peerID, Message{Type: Error, SessionID: sessionID, Code: code})
}

// send writes to one peer's connection, bounded by the send timeout so that a peer
// which has stopped reading cannot hold up whoever is writing to it.
func (h *Hub) send(ctx context.Context, peerID string, msg Message) error {
	h.mu.Lock()
	held, ok := h.conns[peerID]
	h.mu.Unlock()

	if !ok {
		return ErrNoConnection
	}

	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()

	if err := held.conn.Send(ctx, msg); err != nil {
		// A connection that cannot be written to is not a connection. Closing it
		// is what frees the peer to reconnect rather than sit behind a socket
		// nobody is reading.
		held.conn.Close("write failed")
		return errors.Join(ErrNotDelivered, err)
	}
	return nil
}

// allowed counts one message against the sender's allowance.
func (h *Hub) allowed(peerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	held, ok := h.conns[peerID]
	if !ok {
		return false
	}

	now := h.now()
	if now.Sub(held.windowFrom) > h.window {
		held.windowFrom = now
		held.sent = 0
	}

	held.sent++
	return held.sent <= h.allowance
}

// carriable reports whether this is a type the server carries between peers.
// Anything the server itself sends is not something it accepts.
func carriable(t Type) bool {
	switch t {
	case Offer, Answer, Candidate, Bye:
		return true
	default:
		return false
	}
}
