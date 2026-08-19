// Package registry holds what the control plane knows about who is reachable right
// now. It is memory only and disposable: a peer that reconnects registers again —
// adr:0004-keep-the-registry-and-sessions-in-process-memory.
package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// State is where a peer stands, derived from how long it has been silent rather
// than stored. A stored state is a second record of one fact, and it is wrong
// between the moment it changes and the moment something notices.
type State string

const (
	// Online is a peer that has reported in recently.
	Online State = "online"
	// Suspect is a peer that has missed its window but is not written off: the
	// registry still answers for it, because a peer that missed one heartbeat is
	// far likelier to be there than not.
	Suspect State = "suspect"
	// Gone is a peer past the offline window. It is never answered with; it is
	// the state a record has on its way out.
	Gone State = "gone"
)

// Errors a caller is expected to tell apart.
var (
	// ErrNotRegistered is a report from a peer the registry does not hold. The
	// peer is being told to register again, which is the one thing a caller
	// learns about its own record.
	ErrNotRegistered = errors.New("registry: not registered")
	// ErrFull is a registration refused because the registry is at its ceiling.
	// Nothing online is evicted to make room — dropping a working peer to admit a
	// new one trades a real call for a possible one.
	ErrFull = errors.New("registry: full")
)

// Peer is one record.
type Peer struct {
	// ID is the peer identifier, taken from the verified token and never from a
	// request body.
	ID string
	// RegisteredAt is when it first registered, and is not moved by a report.
	RegisteredAt time.Time
	// LastSeen is the last time it reported in.
	LastSeen time.Time
	// State is derived at the moment it was read.
	State State
}

// Options are the windows and the ceiling this registry works to.
type Options struct {
	// HeartbeatInterval is what a peer is told to report in at.
	HeartbeatInterval time.Duration
	// SuspectAfter is the silence that moves a peer to suspect.
	SuspectAfter time.Duration
	// OfflineAfter is the silence that removes it.
	OfflineAfter time.Duration
	// MaxPeers is the ceiling. Zero takes the default.
	MaxPeers int
	// Now is the clock, injected so a test can hold it still.
	Now func() time.Time
}

// DefaultMaxPeers applies when Options leaves the ceiling unset.
const DefaultMaxPeers = 10_000

// Registry is the set of peers currently known.
type Registry struct {
	mu    sync.RWMutex
	peers map[string]record

	heartbeat time.Duration
	suspect   time.Duration
	offline   time.Duration
	ceiling   int
	now       func() time.Time
}

// record is what is stored: two timestamps, and no state.
type record struct {
	registeredAt time.Time
	lastSeen     time.Time
}

// New refuses a configuration whose windows do not order, since a suspect window
// at or past the offline window means a peer is never suspect, and a suspect window
// inside the heartbeat interval means a peer reporting on time is suspect anyway.
func New(opts Options) (*Registry, error) {
	var problems []error

	switch {
	case opts.HeartbeatInterval <= 0:
		problems = append(problems, errors.New("registry: heartbeat interval is not positive"))
	case opts.SuspectAfter <= opts.HeartbeatInterval:
		problems = append(problems, fmt.Errorf("registry: suspect window %s is not longer than the %s heartbeat interval",
			opts.SuspectAfter, opts.HeartbeatInterval))
	}
	if opts.OfflineAfter <= opts.SuspectAfter {
		problems = append(problems, fmt.Errorf("registry: offline window %s is not longer than the %s suspect window",
			opts.OfflineAfter, opts.SuspectAfter))
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	ceiling := opts.MaxPeers
	if ceiling <= 0 {
		ceiling = DefaultMaxPeers
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Registry{
		peers:     make(map[string]record),
		heartbeat: opts.HeartbeatInterval,
		suspect:   opts.SuspectAfter,
		offline:   opts.OfflineAfter,
		ceiling:   ceiling,
		now:       now,
	}, nil
}

// HeartbeatInterval is what a peer is told to report in at.
func (r *Registry) HeartbeatInterval() time.Duration {
	return r.heartbeat
}

// Register records a peer as online. Registering again replaces what was held
// rather than failing: a peer that reconnects has no way to know whether its old
// record survived, and refusing would strand it.
func (r *Registry) Register(id string) (Peer, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Peer{}, errors.New("registry: no peer identifier to register")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	existing, held := r.peers[id]

	if !held && len(r.peers) >= r.ceiling {
		// Expiring first is what keeps a full registry from refusing a peer while
		// holding records nobody will ever hear from again.
		r.expireLocked(now)
		if len(r.peers) >= r.ceiling {
			return Peer{}, ErrFull
		}
	}

	registeredAt := now
	if held {
		registeredAt = existing.registeredAt
	}
	r.peers[id] = record{registeredAt: registeredAt, lastSeen: now}

	return Peer{ID: id, RegisteredAt: registeredAt, LastSeen: now, State: Online}, nil
}

// Heartbeat records that a peer is still there. A peer that was removed is told to
// register again rather than quietly re-admitted, because the registry is what
// other peers were told about it and re-admitting hides that it was ever gone.
func (r *Registry) Heartbeat(id string) (Peer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	held, ok := r.peers[id]
	if !ok || r.stateOf(held, now) == Gone {
		delete(r.peers, id)
		return Peer{}, ErrNotRegistered
	}

	held.lastSeen = now
	r.peers[id] = held

	return Peer{ID: id, RegisteredAt: held.registeredAt, LastSeen: now, State: Online}, nil
}

// Deregister removes a peer. Removing one that is not held is not an error: the
// caller wanted it gone and it is gone.
func (r *Registry) Deregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.peers, id)
}

// Lookup reports one peer. A peer past the offline window is reported as absent,
// in the same shape as one that never registered — telling them apart would say who
// used to be here.
func (r *Registry) Lookup(id string) (Peer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	held, ok := r.peers[id]
	if !ok {
		return Peer{}, false
	}

	now := r.now()
	state := r.stateOf(held, now)
	if state == Gone {
		return Peer{}, false
	}

	return Peer{ID: id, RegisteredAt: held.registeredAt, LastSeen: held.lastSeen, State: state}, true
}

// Counts is how many peers are held, by state.
type Counts struct {
	Online  int
	Suspect int
	// Total is what is held right now, expired records included: it is what the
	// map costs, which is the number worth watching.
	Total int
}

// Count reports the registry's size for the health endpoint.
func (r *Registry) Count() Counts {
	r.mu.RLock()
	defer r.mu.RUnlock()

	now := r.now()
	counts := Counts{Total: len(r.peers)}
	for _, held := range r.peers {
		switch r.stateOf(held, now) {
		case Online:
			counts.Online++
		case Suspect:
			counts.Suspect++
		case Gone:
		}
	}
	return counts
}

// Expire removes every peer past the offline window and reports which ones went.
// It is called on a ticker rather than only when a record is read, so that a peer
// nobody asks about still leaves — and it names them because whatever holds
// sessions has to know which peers stopped being there.
func (r *Registry) Expire() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.expireLocked(r.now())
}

// expireLocked is Expire with the lock already held.
func (r *Registry) expireLocked(now time.Time) []string {
	var removed []string
	for id, held := range r.peers {
		if r.stateOf(held, now) == Gone {
			delete(r.peers, id)
			removed = append(removed, id)
		}
	}
	return removed
}

// stateOf derives a record's state from its silence. Callers hold the lock.
func (r *Registry) stateOf(held record, now time.Time) State {
	silence := now.Sub(held.lastSeen)
	switch {
	case silence > r.offline:
		return Gone
	case silence > r.suspect:
		return Suspect
	default:
		return Online
	}
}

// Sweep expires records on a ticker until ctx is cancelled, calling onExpired with
// how many went whenever any did. It exists because a record nobody reads is
// exactly the one that should not be counted: without it, a peer that stopped and
// that nothing asks about would sit in the map and in the health count forever.
//
// It blocks, so the caller decides where it runs and when it stops.
func (r *Registry) Sweep(ctx context.Context, every time.Duration, onExpired func([]string)) {
	if every <= 0 {
		every = r.heartbeat
	}

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if removed := r.Expire(); len(removed) > 0 && onExpired != nil {
				onExpired(removed)
			}
		}
	}
}
