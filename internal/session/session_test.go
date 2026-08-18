package session

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakePeers is a registry the test controls.
type fakePeers struct {
	present map[string]bool
}

func (f fakePeers) Lookup(peerID string) bool { return f.present[peerID] }

// denyAll refuses every pair, to test the authorizer boundary.
type denyAll struct{}

func (denyAll) Allows(_, _ string) bool { return false }

// testStore builds a store with a clock the test moves by hand: sessions fail
// after 2 minutes of negotiating and are forgotten 10 minutes after they end.
func testStore(t *testing.T, opts Options) (*Store, *time.Time) {
	t.Helper()

	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if opts.NegotiationTimeout == 0 {
		opts.NegotiationTimeout = 2 * time.Minute
	}
	if opts.Retention == 0 {
		opts.Retention = 10 * time.Minute
	}
	opts.Now = func() time.Time { return clock }

	store, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	return store, &clock
}

func TestOpenCreatesANegotiatingSession(t *testing.T) {
	store, clock := testStore(t, Options{})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v, want nil", err)
	}

	if opened.ID == "" {
		t.Error("ID is empty, want an identifier a third peer cannot guess")
	}
	if opened.Caller != "peer-001" || opened.Callee != "peer-002" {
		t.Errorf("participants = %q and %q, want peer-001 calling peer-002", opened.Caller, opened.Callee)
	}
	if opened.State != Negotiating {
		t.Errorf("State = %q, want %q", opened.State, Negotiating)
	}
	if opened.Path != PathUnknown {
		t.Errorf("Path = %q, want it unknown until a peer reports one", opened.Path)
	}
	if !opened.OpenedAt.Equal(*clock) {
		t.Errorf("OpenedAt = %s, want %s", opened.OpenedAt, clock)
	}
}

func TestOpenRefuses(t *testing.T) {
	cases := []struct {
		name   string
		opts   Options
		caller string
		callee string
		want   error
	}{
		{name: "a peer reaching itself", caller: "peer-001", callee: "peer-001", want: ErrSelf},
		{name: "a caller the policy refuses", opts: Options{Authorizer: denyAll{}}, caller: "peer-001", callee: "peer-002", want: ErrNotAllowed},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, _ := testStore(t, c.opts)

			if _, err := store.Open(c.caller, c.callee); !errors.Is(err, c.want) {
				t.Errorf("Open() error = %v, want %v", err, c.want)
			}
		})
	}
}

func TestOpenRefusesEmptyParticipants(t *testing.T) {
	store, _ := testStore(t, Options{})

	if _, err := store.Open("", "peer-002"); err == nil {
		t.Error("Open() error = nil with no caller, want a refusal")
	}
	if _, err := store.Open("peer-001", "  "); err == nil {
		t.Error("Open() error = nil with no callee, want a refusal")
	}
}

func TestOpenAnswersTheSessionThatAlreadyExists(t *testing.T) {
	store, _ := testStore(t, Options{})

	first, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// The same pair, asked the other way round: still one session.
	second, err := store.Open("peer-002", "peer-001")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("a second request made session %q beside %q; one pair has one session", second.ID, first.ID)
	}
	if store.Count() != 1 {
		t.Errorf("Count() = %d, want 1", store.Count())
	}
}

func TestOpenAfterASessionEnded(t *testing.T) {
	store, _ := testStore(t, Options{})

	first, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Report(first.ID, "peer-001", Closed, PathUnknown); err != nil {
		t.Fatalf("Report() error = %v", err)
	}

	second, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v, want nil — the pair may call again", err)
	}
	if second.ID == first.ID {
		t.Error("the closed session was handed back; a new call is a new session")
	}
}

func TestStateMoves(t *testing.T) {
	cases := []struct {
		name    string
		to      State
		path    Path
		allowed bool
	}{
		{name: "negotiating to connected", to: Connected, path: Direct, allowed: true},
		{name: "negotiating to failed", to: Failed, allowed: true},
		{name: "negotiating to closed", to: Closed, allowed: true},
		{name: "negotiating to negotiating", to: Negotiating, allowed: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, _ := testStore(t, Options{})
			opened, err := store.Open("peer-001", "peer-002")
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}

			moved, err := store.Report(opened.ID, "peer-001", c.to, c.path)
			if c.allowed {
				if err != nil {
					t.Fatalf("Report() error = %v, want nil", err)
				}
				if moved.State != c.to {
					t.Errorf("State = %q, want %q", moved.State, c.to)
				}
				return
			}

			if !errors.Is(err, ErrBadTransition) {
				t.Errorf("Report() error = %v, want ErrBadTransition", err)
			}
			held, err := store.Get(opened.ID, "peer-001")
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if held.State != Negotiating {
				t.Errorf("State = %q after a refused move, want it unchanged at %q", held.State, Negotiating)
			}
		})
	}
}

func TestAConnectedSessionCannotNegotiateAgain(t *testing.T) {
	store, _ := testStore(t, Options{})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Report(opened.ID, "peer-001", Connected, Relayed); err != nil {
		t.Fatalf("Report() error = %v", err)
	}

	if _, err := store.Report(opened.ID, "peer-002", Negotiating, PathUnknown); !errors.Is(err, ErrBadTransition) {
		t.Errorf("Report() error = %v, want ErrBadTransition — a renegotiation is this session carrying on", err)
	}
}

func TestAnEndedSessionStaysEnded(t *testing.T) {
	for _, end := range []State{Failed, Closed} {
		t.Run(string(end), func(t *testing.T) {
			store, _ := testStore(t, Options{})
			opened, err := store.Open("peer-001", "peer-002")
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			if _, err := store.Report(opened.ID, "peer-001", end, PathUnknown); err != nil {
				t.Fatalf("Report() error = %v", err)
			}

			for _, to := range []State{Negotiating, Connected, Closed, Failed} {
				if _, err := store.Report(opened.ID, "peer-001", to, PathUnknown); !errors.Is(err, ErrBadTransition) {
					t.Errorf("Report(%q) error = %v, want ErrBadTransition from %q", to, err, end)
				}
			}
		})
	}
}

func TestConnectingRecordsThePath(t *testing.T) {
	for _, path := range []Path{Direct, Relayed} {
		t.Run(string(path), func(t *testing.T) {
			store, _ := testStore(t, Options{})
			opened, err := store.Open("peer-001", "peer-002")
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}

			connected, err := store.Report(opened.ID, "peer-002", Connected, path)
			if err != nil {
				t.Fatalf("Report() error = %v", err)
			}
			if connected.Path != path {
				t.Errorf("Path = %q, want %q", connected.Path, path)
			}
		})
	}
}

func TestGetIsOnlyForAPeerInTheSession(t *testing.T) {
	store, _ := testStore(t, Options{})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	for _, peerID := range []string{"peer-001", "peer-002"} {
		if _, err := store.Get(opened.ID, peerID); err != nil {
			t.Errorf("Get() error = %v for %q, want nil", err, peerID)
		}
	}

	if _, err := store.Get(opened.ID, "peer-003"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() error = %v for a peer outside the session, want ErrNotFound", err)
	}
	if _, err := store.Get("no-such-session", "peer-001"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() error = %v for a session that never existed, want ErrNotFound", err)
	}
}

func TestReportIsOnlyForAPeerInTheSession(t *testing.T) {
	store, _ := testStore(t, Options{})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if _, err := store.Report(opened.ID, "peer-003", Closed, PathUnknown); !errors.Is(err, ErrNotFound) {
		t.Errorf("Report() error = %v from a peer outside the session, want ErrNotFound", err)
	}

	held, err := store.Get(opened.ID, "peer-001")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if held.State != Negotiating {
		t.Errorf("State = %q, want it untouched by a stranger", held.State)
	}
}

func TestASessionNegotiatingPastTheTimeoutIsFailed(t *testing.T) {
	store, clock := testStore(t, Options{NegotiationTimeout: time.Minute})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	*clock = clock.Add(90 * time.Second)

	held, err := store.Get(opened.ID, "peer-001")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if held.State != Failed {
		t.Errorf("State = %q past the timeout, want %q whether or not anything swept it", held.State, Failed)
	}
	if store.Count() != 0 {
		t.Errorf("Count() = %d, want a timed-out session not to count as live", store.Count())
	}
}

func TestATimedOutSessionIsNotResurrected(t *testing.T) {
	store, clock := testStore(t, Options{NegotiationTimeout: time.Minute})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	*clock = clock.Add(90 * time.Second)

	if _, err := store.Report(opened.ID, "peer-001", Connected, Direct); !errors.Is(err, ErrBadTransition) {
		t.Errorf("Report() error = %v, want ErrBadTransition — the session had already failed", err)
	}
}

func TestReapForgetsWhatIsPastRetention(t *testing.T) {
	store, clock := testStore(t, Options{NegotiationTimeout: time.Minute, Retention: 5 * time.Minute})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Report(opened.ID, "peer-001", Closed, PathUnknown); err != nil {
		t.Fatalf("Report() error = %v", err)
	}

	// Inside the retention, the peer can still read why the call ended.
	*clock = clock.Add(4 * time.Minute)
	store.Reap()
	if _, err := store.Get(opened.ID, "peer-001"); err != nil {
		t.Errorf("Get() error = %v inside the retention, want the ended session still readable", err)
	}

	*clock = clock.Add(2 * time.Minute)
	_, forgotten := store.Reap()
	if forgotten != 1 {
		t.Errorf("Reap() forgot %d, want 1", forgotten)
	}
	if _, err := store.Get(opened.ID, "peer-001"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() error = %v past the retention, want ErrNotFound", err)
	}
}

func TestReapCountsWhatItFailed(t *testing.T) {
	store, clock := testStore(t, Options{NegotiationTimeout: time.Minute})

	for i := range 3 {
		if _, err := store.Open("caller-"+strconv.Itoa(i), "callee-"+strconv.Itoa(i)); err != nil {
			t.Fatalf("Open() error = %v", err)
		}
	}
	*clock = clock.Add(90 * time.Second)

	failed, _ := store.Reap()
	if failed != 3 {
		t.Errorf("Reap() failed %d, want 3", failed)
	}
}

func TestClosePeerEndsEverySessionItWasIn(t *testing.T) {
	store, _ := testStore(t, Options{})

	first, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	second, err := store.Open("peer-003", "peer-001")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	untouched, err := store.Open("peer-004", "peer-005")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if closed := store.ClosePeer("peer-001"); closed != 2 {
		t.Errorf("ClosePeer() closed %d, want 2", closed)
	}

	for _, id := range []string{first.ID, second.ID} {
		held, err := store.Get(id, "peer-001")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if held.State != Closed {
			t.Errorf("State = %q, want %q once the peer left the registry", held.State, Closed)
		}
	}

	held, err := store.Get(untouched.ID, "peer-004")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if held.State != Negotiating {
		t.Errorf("an unrelated session moved to %q", held.State)
	}
}

func TestClosePeerLeavesEndedSessionsAlone(t *testing.T) {
	store, _ := testStore(t, Options{})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Report(opened.ID, "peer-001", Failed, PathUnknown); err != nil {
		t.Fatalf("Report() error = %v", err)
	}

	if closed := store.ClosePeer("peer-001"); closed != 0 {
		t.Errorf("ClosePeer() closed %d, want 0 — a failed session is not reopened to be closed", closed)
	}
	held, err := store.Get(opened.ID, "peer-001")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if held.State != Failed {
		t.Errorf("State = %q, want it left at %q", held.State, Failed)
	}
}

func TestOpenRefusesPastTheCeiling(t *testing.T) {
	store, _ := testStore(t, Options{MaxSessions: 2})

	for i := range 2 {
		if _, err := store.Open("caller-"+strconv.Itoa(i), "callee-"+strconv.Itoa(i)); err != nil {
			t.Fatalf("Open() error = %v", err)
		}
	}

	if _, err := store.Open("caller-x", "callee-x"); !errors.Is(err, ErrFull) {
		t.Errorf("Open() error = %v, want ErrFull", err)
	}
}

func TestAnEndedSessionDoesNotHoldAPlace(t *testing.T) {
	store, _ := testStore(t, Options{MaxSessions: 1})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Report(opened.ID, "peer-001", Closed, PathUnknown); err != nil {
		t.Fatalf("Report() error = %v", err)
	}

	if _, err := store.Open("peer-003", "peer-004"); err != nil {
		t.Errorf("Open() error = %v, want nil — a closed session does not count against the ceiling", err)
	}
}

func TestReachableAsksTheRegistry(t *testing.T) {
	store, _ := testStore(t, Options{Peers: fakePeers{present: map[string]bool{"peer-002": true}}})

	if !store.Reachable("peer-002") {
		t.Error("Reachable() = false for a registered peer")
	}
	if store.Reachable("peer-404") {
		t.Error("Reachable() = true for a peer nobody registered")
	}
}

func TestReachableWithoutARegistry(t *testing.T) {
	store, _ := testStore(t, Options{})

	if !store.Reachable("anybody") {
		t.Error("Reachable() = false with no registry wired up, want true")
	}
}

func TestCountIsLiveSessionsOnly(t *testing.T) {
	store, _ := testStore(t, Options{})

	live, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Report(live.ID, "peer-001", Connected, Direct); err != nil {
		t.Fatalf("Report() error = %v", err)
	}

	ended, err := store.Open("peer-003", "peer-004")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Report(ended.ID, "peer-003", Closed, PathUnknown); err != nil {
		t.Fatalf("Report() error = %v", err)
	}

	if got := store.Count(); got != 1 {
		t.Errorf("Count() = %d, want only the connected session", got)
	}
}

func TestSessionHelpers(t *testing.T) {
	held := Session{Caller: "peer-001", Callee: "peer-002", State: Connected}

	if !held.Has("peer-001") || !held.Has("peer-002") || held.Has("peer-003") {
		t.Error("Has() does not report the participants")
	}
	if held.Other("peer-001") != "peer-002" || held.Other("peer-002") != "peer-001" {
		t.Error("Other() does not report the far side")
	}
	if !held.Live() {
		t.Error("Live() = false for a connected session")
	}
	if (Session{State: Closed}).Live() || (Session{State: Failed}).Live() {
		t.Error("Live() = true for a session that ended")
	}
}

func TestNewRejects(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{name: "no negotiation timeout", opts: Options{}},
		{name: "a negative negotiation timeout", opts: Options{NegotiationTimeout: -time.Minute}},
		{name: "a negative retention", opts: Options{NegotiationTimeout: time.Minute, Retention: -time.Minute}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.opts); err == nil {
				t.Error("New() error = nil, want a refusal")
			}
		})
	}
}

func TestPairKeyDoesNotDependOnWhoAsked(t *testing.T) {
	if pairKey("peer-001", "peer-002") != pairKey("peer-002", "peer-001") {
		t.Error("the pair key depends on the order, so two peers calling each other would make two sessions")
	}
	if pairKey("peer-001", "peer-002") == pairKey("peer-001", "peer-003") {
		t.Error("two different pairs share a key")
	}
}

func TestStoreIsSafeUnderConcurrentUse(t *testing.T) {
	store, err := New(Options{NegotiationTimeout: time.Minute, Retention: time.Minute})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			caller := "caller-" + strconv.Itoa(n%5)
			callee := "callee-" + strconv.Itoa(n%5)

			opened, err := store.Open(caller, callee)
			if err != nil {
				return
			}
			store.Get(opened.ID, caller)
			store.Report(opened.ID, caller, Connected, Direct)
			store.Count()
			store.Reap()
			if n%4 == 0 {
				store.ClosePeer(caller)
			}
		}(i)
	}
	wg.Wait()
}

func TestOpeningAndClosingInALoopDoesNotGrowWithoutBound(t *testing.T) {
	store, _ := testStore(t, Options{MaxSessions: 4, Retention: time.Hour})

	// The ceiling is on live sessions and an ended one frees its place at once,
	// so this loop is refused by nothing. What it must not do is grow the map for
	// as long as the retention lasts.
	for i := range 500 {
		opened, err := store.Open("attacker", "callee-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("Open() error = %v on call %d", err, i)
		}
		if _, err := store.Report(opened.ID, "attacker", Closed, PathUnknown); err != nil {
			t.Fatalf("Report() error = %v", err)
		}
	}

	if held := store.Held(); held > 4*retainedPerLive {
		t.Errorf("Held() = %d after 500 open-and-close rounds, want no more than %d",
			held, 4*retainedPerLive)
	}
	if store.Count() != 0 {
		t.Errorf("Count() = %d, want 0 — every session was closed", store.Count())
	}
}

func TestOnePeerCannotHoldEverySession(t *testing.T) {
	store, _ := testStore(t, Options{MaxSessions: 100, MaxSessionsPerPeer: 3})

	for i := range 3 {
		if _, err := store.Open("greedy", "callee-"+strconv.Itoa(i)); err != nil {
			t.Fatalf("Open() error = %v on session %d", err, i)
		}
	}

	if _, err := store.Open("greedy", "callee-4"); !errors.Is(err, ErrTooManyForPeer) {
		t.Errorf("Open() error = %v, want ErrTooManyForPeer", err)
	}

	// Another peer is unaffected: the cap is per peer, not a share of the whole.
	if _, err := store.Open("somebody-else", "callee-9"); err != nil {
		t.Errorf("Open() error = %v for an unrelated peer, want nil", err)
	}
}

func TestTheCalleeCapCountsToo(t *testing.T) {
	store, _ := testStore(t, Options{MaxSessions: 100, MaxSessionsPerPeer: 2})

	for i := range 2 {
		if _, err := store.Open("caller-"+strconv.Itoa(i), "popular"); err != nil {
			t.Fatalf("Open() error = %v", err)
		}
	}

	if _, err := store.Open("caller-9", "popular"); !errors.Is(err, ErrTooManyForPeer) {
		t.Errorf("Open() error = %v, want ErrTooManyForPeer — a callee has a cap as well", err)
	}
}

func TestEndingASessionFreesThePeerCap(t *testing.T) {
	store, _ := testStore(t, Options{MaxSessions: 100, MaxSessionsPerPeer: 1})

	opened, err := store.Open("peer-001", "peer-002")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Open("peer-001", "peer-003"); !errors.Is(err, ErrTooManyForPeer) {
		t.Fatalf("Open() error = %v, want ErrTooManyForPeer", err)
	}

	if _, err := store.Report(opened.ID, "peer-001", Closed, PathUnknown); err != nil {
		t.Fatalf("Report() error = %v", err)
	}

	if _, err := store.Open("peer-001", "peer-003"); err != nil {
		t.Errorf("Open() error = %v after the first session closed, want nil", err)
	}
}

func TestATimedOutSessionFreesItsPlace(t *testing.T) {
	store, clock := testStore(t, Options{MaxSessions: 100, MaxSessionsPerPeer: 1, NegotiationTimeout: time.Minute})

	if _, err := store.Open("peer-001", "peer-002"); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	*clock = clock.Add(90 * time.Second)

	if _, err := store.Open("peer-001", "peer-003"); err != nil {
		t.Errorf("Open() error = %v, want nil — the timed-out session no longer counts against the peer", err)
	}
	if store.Count() != 1 {
		t.Errorf("Count() = %d, want only the new session", store.Count())
	}
}

func TestCountsFollowEveryWayASessionCanEnd(t *testing.T) {
	ends := map[string]func(*Store, Session) error{
		"reported closed": func(s *Store, held Session) error {
			_, err := s.Report(held.ID, held.Caller, Closed, PathUnknown)
			return err
		},
		"reported failed": func(s *Store, held Session) error {
			_, err := s.Report(held.ID, held.Caller, Failed, PathUnknown)
			return err
		},
		"the peer left the registry": func(s *Store, held Session) error {
			s.ClosePeer(held.Caller)
			return nil
		},
	}

	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			store, _ := testStore(t, Options{MaxSessionsPerPeer: 1})

			opened, err := store.Open("peer-001", "peer-002")
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			if err := end(store, opened); err != nil {
				t.Fatalf("ending the session: %v", err)
			}

			if store.Count() != 0 {
				t.Errorf("Count() = %d, want 0", store.Count())
			}
			// The per-peer count came down too, or the peer could never call again.
			if _, err := store.Open("peer-001", "peer-003"); err != nil {
				t.Errorf("Open() error = %v, want nil once the earlier session ended", err)
			}
		})
	}
}

func TestReapDoesNotLeaveTheEndedListGrowing(t *testing.T) {
	store, clock := testStore(t, Options{Retention: time.Minute})

	for i := range 20 {
		opened, err := store.Open("peer-"+strconv.Itoa(i), "callee-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		if _, err := store.Report(opened.ID, opened.Caller, Closed, PathUnknown); err != nil {
			t.Fatalf("Report() error = %v", err)
		}
	}

	*clock = clock.Add(2 * time.Minute)
	store.Reap()

	if store.Held() != 0 {
		t.Errorf("Held() = %d, want 0 past the retention", store.Held())
	}
	if len(store.ended) != 0 {
		t.Errorf("the ended list holds %d identifiers the store no longer has", len(store.ended))
	}
}
