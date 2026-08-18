package auth

import (
	"fmt"
	"sync"
	"time"
)

// sweepThreshold is how many identifiers may be tracked before a write also
// clears the ones whose window has passed. Sweeping on every write would be waste;
// never sweeping is a map that grows with every identifier anyone ever guessed.
const sweepThreshold = 1024

// AttemptLimiter counts failed authentications per client identifier and refuses
// an identifier that has spent its allowance inside the window — correct
// credentials included, because a limit that lifts for the right secret tells an
// attacker when they have found it.
//
// Counting per identifier rather than per address is deliberate: peers arrive from
// anywhere, and what is being guessed is one named client's secret. The cost is
// that whoever knows a client identifier can lock it out, which is accepted against
// the alternative of unlimited guessing.
//
// The state is in this process. A restart forgives everything counted so far, and a
// second instance would count separately — the same single-node assumption as the
// registry, and it degrades the same way.
type AttemptLimiter struct {
	mu        sync.Mutex
	allowance int
	window    time.Duration
	failures  map[string]failureRecord
	now       func() time.Time
}

// failureRecord is one identifier's failures inside the window that started at
// startedAt.
type failureRecord struct {
	count     int
	startedAt time.Time
}

// NewAttemptLimiter refuses a configuration that would refuse everybody or count
// forever.
func NewAttemptLimiter(allowance int, window time.Duration) (*AttemptLimiter, error) {
	switch {
	case allowance <= 0:
		return nil, fmt.Errorf("auth: an allowance of %d would refuse every attempt", allowance)
	case window <= 0:
		return nil, fmt.Errorf("auth: a failure window of %s never passes", window)
	}

	return &AttemptLimiter{
		allowance: allowance,
		window:    window,
		failures:  make(map[string]failureRecord),
		now:       time.Now,
	}, nil
}

// Allowed reports whether this identifier may try again. It is asked before the
// credential is checked, so that a blocked identifier costs nothing to refuse.
func (l *AttemptLimiter) Allowed(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	record, tracked := l.failures[id]
	if !tracked || l.expired(record) {
		return true
	}
	return record.count < l.allowance
}

// Failed records one failed attempt.
func (l *AttemptLimiter) Failed(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	record, tracked := l.failures[id]
	if !tracked || l.expired(record) {
		record = failureRecord{startedAt: now}
	}
	record.count++
	l.failures[id] = record

	if len(l.failures) > sweepThreshold {
		l.sweep()
	}
}

// Succeeded clears what was counted, so that a peer that authenticates is not
// carrying failures from before.
func (l *AttemptLimiter) Succeeded(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.failures, id)
}

// Tracked is how many identifiers are currently counted, for a test and for a
// metric — never for a decision.
func (l *AttemptLimiter) Tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.failures)
}

// expired reports whether a record's window has passed. Callers hold the lock.
func (l *AttemptLimiter) expired(record failureRecord) bool {
	return l.now().Sub(record.startedAt) > l.window
}

// sweep drops every record whose window has passed. Callers hold the lock.
func (l *AttemptLimiter) sweep() {
	for id, record := range l.failures {
		if l.expired(record) {
			delete(l.failures, id)
		}
	}
}
