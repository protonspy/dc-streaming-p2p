// Package session records that two peers are trying to reach each other, or have.
// It holds no media and no session description — those live between the peers.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// State is where a session stands. Unlike a peer's state it is stored, because it
// follows from what a peer reported rather than from a clock.
type State string

const (
	// Negotiating is a session whose peers are still trying to reach each other.
	Negotiating State = "negotiating"
	// Connected is a session whose peers reached each other.
	Connected State = "connected"
	// Failed is a session that did not connect, by report or by timeout.
	Failed State = "failed"
	// Closed is a session a peer ended.
	Closed State = "closed"
)

// Path is how a connected session's media travels. It is observability: nothing in
// the control plane behaves differently for one or the other, because nothing in
// the control plane touches the media.
type Path string

const (
	// PathUnknown is a session that has not connected, so no path won yet.
	PathUnknown Path = ""
	// Direct is a peer connection with no relay in it.
	Direct Path = "direct"
	// Relayed is a peer connection carried by the relay.
	Relayed Path = "relayed"
)

// Errors a caller is expected to tell apart.
var (
	// ErrNotFound is a session that does not exist, or that the asking peer is
	// not in — the same answer either way, because a session identifier is not a
	// capability and the difference would say who is talking to whom.
	ErrNotFound = errors.New("session: not found")
	// ErrNotAllowed is a caller the authorizer refused. It is answered the same
	// way as a peer that is not there.
	ErrNotAllowed = errors.New("session: not allowed")
	// ErrSelf is a peer asking to reach itself.
	ErrSelf = errors.New("session: a peer cannot reach itself")
	// ErrFull is the store at its ceiling.
	ErrFull = errors.New("session: too many sessions")
	// ErrTooManyForPeer is one peer holding as many live sessions as it may.
	ErrTooManyForPeer = errors.New("session: too many sessions for this peer")
	// ErrBadTransition is a state a session cannot move to from where it is.
	ErrBadTransition = errors.New("session: not a move this session can make")
)

// Session is one record.
type Session struct {
	ID string
	// Caller is the peer that asked, Callee the peer it asked for. Which is which
	// stops mattering once the session is connected, and is kept because a log
	// that cannot say who called is hard to read.
	Caller string
	Callee string
	State  State
	Path   Path
	// OpenedAt is when the session was created, ChangedAt when its state last
	// moved.
	OpenedAt  time.Time
	ChangedAt time.Time
}

// Live reports whether this session still counts against the ceiling and the
// health count: one that ended does not.
func (s Session) Live() bool {
	return s.State == Negotiating || s.State == Connected
}

// Has reports whether a peer is in this session.
func (s Session) Has(peerID string) bool {
	return peerID == s.Caller || peerID == s.Callee
}

// Other is the peer in this session that is not the one given.
func (s Session) Other(peerID string) string {
	if peerID == s.Caller {
		return s.Callee
	}
	return s.Caller
}

// Authorizer decides whether one peer may reach another. It is an interface
// because a policy scattered across handlers is one somebody forgets to apply;
// contact lists, rooms, or an external decision replace this without touching a
// route.
type Authorizer interface {
	Allows(caller, callee string) bool
}

// AllowAll lets any authenticated peer reach any registered peer. It is what the
// first deployments need and what the demo requires, and it is a type rather than
// a nil check so that "no policy" is a decision somebody wrote down.
type AllowAll struct{}

// Allows implements Authorizer.
func (AllowAll) Allows(_, _ string) bool { return true }

// Reachable answers whether a peer can be reached at all — the registry, from the
// store's point of view.
type Reachable interface {
	Lookup(peerID string) bool
}

// Options are what a store needs.
type Options struct {
	// NegotiationTimeout is how long a session may stay negotiating before it is
	// counted as failed.
	NegotiationTimeout time.Duration
	// Retention is how long an ended session is kept before it is forgotten. It
	// exists so a peer that reconnects can still read why its call ended.
	Retention time.Duration
	// MaxSessions is the ceiling on live sessions. Zero takes the default.
	MaxSessions int
	// MaxSessionsPerPeer is how many live sessions one peer may be in. Zero takes
	// the default. It is what keeps one caller from occupying a slot against
	// every peer it can reach.
	MaxSessionsPerPeer int
	// Authorizer decides who may reach whom. Nil allows everybody.
	Authorizer Authorizer
	// Peers says whether a peer is reachable. Nil treats every peer as reachable,
	// which is only right in a test.
	Peers Reachable
	// Now is the clock, injected so a test can hold it still.
	Now func() time.Time
}

// Defaults applied when Options leaves a ceiling unset.
const (
	DefaultMaxSessions        = 10_000
	DefaultMaxSessionsPerPeer = 64
)

// retainedPerLive is how many records the store may hold for each live session it
// allows. Ended sessions stay readable for their retention, so the map is larger
// than the live count by design — this bounds how much larger, so a caller that
// opens and closes in a loop cannot grow it without limit.
const retainedPerLive = 4

// Store holds the sessions.
type Store struct {
	mu       sync.Mutex
	sessions map[string]Session
	// pairs indexes the live session between two peers, under a key that does not
	// depend on which of them asked first — one pair, one session, resolved under
	// the same lock as the insert.
	pairs map[string]string
	// live is how many sessions have not ended, and perPeer indexes the live ones
	// by participant. Both are maintained on every move, so neither the ceiling
	// nor the health count costs a scan: a caller opening and closing in a loop
	// must not make every other caller pay for the map's size. perPeer holds
	// identifiers rather than a count because a peer at its cap has to be able to
	// find out whether what it holds has quietly timed out, and that has to cost
	// the cap rather than the whole map.
	live    int
	perPeer map[string]map[string]bool
	// ended is the identifiers of ended sessions in the order they ended, so the
	// oldest can be dropped without searching for it.
	ended []string

	timeout    time.Duration
	retention  time.Duration
	ceiling    int
	perPeerCap int
	authorizer Authorizer
	peers      Reachable
	now        func() time.Time
}

// New refuses a configuration that would make a session impossible to finish.
func New(opts Options) (*Store, error) {
	if opts.NegotiationTimeout <= 0 {
		return nil, errors.New("session: negotiation timeout is not positive")
	}
	if opts.Retention < 0 {
		return nil, errors.New("session: retention is negative")
	}

	ceiling := opts.MaxSessions
	if ceiling <= 0 {
		ceiling = DefaultMaxSessions
	}
	perPeerCap := opts.MaxSessionsPerPeer
	if perPeerCap <= 0 {
		perPeerCap = DefaultMaxSessionsPerPeer
	}
	authorizer := opts.Authorizer
	if authorizer == nil {
		authorizer = AllowAll{}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Store{
		sessions:   make(map[string]Session),
		pairs:      make(map[string]string),
		perPeer:    make(map[string]map[string]bool),
		timeout:    opts.NegotiationTimeout,
		retention:  opts.Retention,
		ceiling:    ceiling,
		perPeerCap: perPeerCap,
		authorizer: authorizer,
		peers:      opts.Peers,
		now:        now,
	}, nil
}

// Open creates a session from caller to callee, or returns the one they already
// have. A callee that is unreachable and a callee the caller may not reach are the
// same answer to the caller, so that a policy cannot be used to enumerate who
// exists.
func (s *Store) Open(caller, callee string) (Session, error) {
	caller = strings.TrimSpace(caller)
	callee = strings.TrimSpace(callee)

	switch {
	case caller == "":
		return Session{}, errors.New("session: no caller")
	case callee == "":
		return Session{}, errors.New("session: no callee")
	case caller == callee:
		return Session{}, ErrSelf
	}

	if !s.authorizer.Allows(caller, callee) {
		return Session{}, ErrNotAllowed
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	if existing, found := s.pairLocked(caller, callee, now); found {
		return existing, nil
	}

	if s.live >= s.ceiling {
		return Session{}, ErrFull
	}
	for _, peerID := range []string{caller, callee} {
		if len(s.perPeer[peerID]) < s.perPeerCap {
			continue
		}
		// At the cap, and some of what is held may have timed out with nothing
		// reading it. Settling costs this peer's own sessions, which is exactly
		// what the cap bounds.
		s.settlePeerLocked(peerID, now)
		if len(s.perPeer[peerID]) >= s.perPeerCap {
			return Session{}, ErrTooManyForPeer
		}
	}

	// Ended sessions stay readable for their retention, so the map outlives the
	// live count on purpose. Dropping the oldest ended record when the map grows
	// past its bound is what keeps that from being unbounded, and it costs a pop
	// rather than a scan.
	s.trimLocked()

	id, err := newID()
	if err != nil {
		return Session{}, err
	}

	created := Session{
		ID:        id,
		Caller:    caller,
		Callee:    callee,
		State:     Negotiating,
		Path:      PathUnknown,
		OpenedAt:  now,
		ChangedAt: now,
	}
	s.sessions[id] = created
	s.pairs[pairKey(caller, callee)] = id
	s.live++
	s.trackLocked(caller, id)
	s.trackLocked(callee, id)

	return created, nil
}

// Reachable reports whether the callee can be reached, which the caller of Open
// checks first so that an unreachable peer is refused before a session exists.
func (s *Store) Reachable(peerID string) bool {
	if s.peers == nil {
		return true
	}
	return s.peers.Lookup(peerID)
}

// Get reports a session to a peer in it. To anybody else it does not exist.
func (s *Store) Get(id, askedBy string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	found, ok := s.sessions[id]
	if !ok || !found.Has(askedBy) {
		return Session{}, ErrNotFound
	}
	return s.settleLocked(found, s.now()), nil
}

// Report moves a session to the state a peer in it says it reached. The path is
// recorded only with a connection, since it is the peer connection's answer.
func (s *Store) Report(id, reportedBy string, state State, path Path) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	found, ok := s.sessions[id]
	if !ok || !found.Has(reportedBy) {
		return Session{}, ErrNotFound
	}

	now := s.now()
	found = s.settleLocked(found, now)

	if !canMove(found.State, state) {
		return Session{}, ErrBadTransition
	}

	wasLive := found.Live()
	found.State = state
	found.ChangedAt = now
	if state == Connected {
		found.Path = path
	}
	s.sessions[id] = found

	if wasLive && !found.Live() {
		s.endedLocked(found)
	}

	return found, nil
}

// ClosePeer ends every session a peer was in, which is what happens when it leaves
// the registry. It reports how many it closed.
func (s *Store) ClosePeer(peerID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	var closed int

	for id, held := range s.sessions {
		if !held.Has(peerID) || !held.Live() {
			continue
		}
		held.State = Closed
		held.ChangedAt = now
		s.sessions[id] = held
		s.endedLocked(held)
		closed++
	}

	return closed
}

// Count is how many sessions are live right now. It is a counter rather than a
// scan, because the health endpoint is unauthenticated and a scan there is a
// stranger making everybody else wait on the lock.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.live
}

// Held is how many records the store is holding, live and ended alike. It is what
// the map costs, which is the number worth watching.
func (s *Store) Held() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.sessions)
}

// Reap settles timed-out sessions and forgets the ones past their retention. This
// is the only full pass over the map, and it runs on a ticker: every read settles
// what it touches anyway, so this bounds the memory rather than the correctness.
func (s *Store) Reap() (failed, forgotten int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	for id, held := range s.sessions {
		before := held.State
		held = s.settleLocked(held, now)
		if before == Negotiating && held.State == Failed {
			failed++
		}

		if !held.Live() && now.Sub(held.ChangedAt) > s.retention {
			s.forgetLocked(id, held)
			forgotten++
		}
	}

	s.compactEndedLocked()
	return failed, forgotten
}

// pairLocked finds the live session between two peers. Callers hold the lock.
func (s *Store) pairLocked(a, b string, now time.Time) (Session, bool) {
	key := pairKey(a, b)
	id, ok := s.pairs[key]
	if !ok {
		return Session{}, false
	}

	held, ok := s.sessions[id]
	if !ok {
		delete(s.pairs, key)
		return Session{}, false
	}

	held = s.settleLocked(held, now)
	if !held.Live() {
		return Session{}, false
	}
	return held, true
}

// settleLocked applies the negotiation timeout to one session, so a session past it
// reads as failed whether or not the ticker has run. It writes, which is why every
// path that reads a session takes the write lock. Callers hold the lock.
func (s *Store) settleLocked(held Session, now time.Time) Session {
	if held.State != Negotiating || now.Sub(held.OpenedAt) <= s.timeout {
		return held
	}

	held.State = Failed
	held.ChangedAt = held.OpenedAt.Add(s.timeout)
	s.sessions[held.ID] = held
	s.endedLocked(held)
	return held
}

// endedLocked records that a session stopped being live: the counters, the pair
// index, and the order it ended in. Callers hold the lock.
func (s *Store) endedLocked(held Session) {
	s.live--
	s.untrackLocked(held.Caller, held.ID)
	s.untrackLocked(held.Callee, held.ID)
	delete(s.pairs, pairKey(held.Caller, held.Callee))
	s.ended = append(s.ended, held.ID)
}

// forgetLocked drops a record entirely. Callers hold the lock.
func (s *Store) forgetLocked(id string, held Session) {
	delete(s.sessions, id)
	if existing, ok := s.pairs[pairKey(held.Caller, held.Callee)]; ok && existing == id {
		delete(s.pairs, pairKey(held.Caller, held.Callee))
	}
}

// settlePeerLocked applies the negotiation timeout to everything one peer holds,
// which is how a peer at its cap discovers that some of it has expired. Callers
// hold the lock.
func (s *Store) settlePeerLocked(peerID string, now time.Time) {
	ids := make([]string, 0, len(s.perPeer[peerID]))
	for id := range s.perPeer[peerID] {
		ids = append(ids, id)
	}

	for _, id := range ids {
		held, ok := s.sessions[id]
		if !ok {
			s.untrackLocked(peerID, id)
			continue
		}
		s.settleLocked(held, now)
	}
}

// trackLocked records that a peer is in a live session. Callers hold the lock.
func (s *Store) trackLocked(peerID, id string) {
	held, ok := s.perPeer[peerID]
	if !ok {
		held = make(map[string]bool, 1)
		s.perPeer[peerID] = held
	}
	held[id] = true
}

// untrackLocked drops a session from a peer's live set, and the peer with it once
// it holds nothing, so the map keeps no row for a peer that is in no session.
// Callers hold the lock.
func (s *Store) untrackLocked(peerID, id string) {
	held, ok := s.perPeer[peerID]
	if !ok {
		return
	}
	delete(held, id)
	if len(held) == 0 {
		delete(s.perPeer, peerID)
	}
}

// trimLocked drops the oldest ended sessions while the map is larger than its
// bound. Callers hold the lock.
func (s *Store) trimLocked() {
	bound := s.ceiling * retainedPerLive

	for len(s.sessions) >= bound && len(s.ended) > 0 {
		oldest := s.ended[0]
		s.ended = s.ended[1:]

		held, ok := s.sessions[oldest]
		if !ok || held.Live() {
			continue
		}
		s.forgetLocked(oldest, held)
	}
}

// compactEndedLocked drops identifiers the store no longer holds, so the order
// list does not outgrow the map it points into. Callers hold the lock.
func (s *Store) compactEndedLocked() {
	kept := s.ended[:0]
	for _, id := range s.ended {
		if _, ok := s.sessions[id]; ok {
			kept = append(kept, id)
		}
	}
	s.ended = kept
}

// canMove reports whether a session in one state may move to another. Everything
// not named here is refused and the session is left as it was.
func canMove(from, to State) bool {
	switch from {
	case Negotiating:
		return to == Connected || to == Failed || to == Closed
	case Connected:
		// A renegotiation is this session carrying on, so there is no way back to
		// negotiating; a connected session ends or fails.
		return to == Closed || to == Failed
	case Failed, Closed:
		return false
	default:
		return false
	}
}

// pairKey names the pair without saying which of them asked first, so two peers
// calling each other at the same instant resolve to one session.
func pairKey(a, b string) string {
	pair := []string{a, b}
	sort.Strings(pair)
	return pair[0] + "\x00" + pair[1]
}

// newID is the session identifier: random, because it is handed to two peers and
// must not be guessable by a third.
func newID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("session: generating an identifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}
