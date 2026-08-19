package registry

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// testRegistry builds a registry with a clock the test moves by hand: peers report
// in at 10s, are suspect past 30s of silence, and gone past 60s.
func testRegistry(t *testing.T, ceiling int) (*Registry, *time.Time) {
	t.Helper()

	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	reg, err := New(Options{
		HeartbeatInterval: 10 * time.Second,
		SuspectAfter:      30 * time.Second,
		OfflineAfter:      60 * time.Second,
		MaxPeers:          ceiling,
		Now:               func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	return reg, &clock
}

func TestRegisterRecordsAPeerAsOnline(t *testing.T) {
	reg, clock := testRegistry(t, 10)

	peer, err := reg.Register("peer-001")
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	if peer.ID != "peer-001" || peer.State != Online {
		t.Errorf("Register() = %+v, want peer-001 online", peer)
	}
	if !peer.LastSeen.Equal(*clock) || !peer.RegisteredAt.Equal(*clock) {
		t.Errorf("timestamps = %s / %s, want both %s", peer.RegisteredAt, peer.LastSeen, clock)
	}

	found, ok := reg.Lookup("peer-001")
	if !ok || found.State != Online {
		t.Errorf("Lookup() = %+v, %t, want the registered peer", found, ok)
	}
}

func TestRegisterAgainReplacesWhatWasHeld(t *testing.T) {
	reg, clock := testRegistry(t, 10)

	first, err := reg.Register("peer-001")
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	*clock = clock.Add(20 * time.Second)

	second, err := reg.Register("peer-001")
	if err != nil {
		t.Fatalf("Register() error = %v on a second registration, want nil — a peer that reconnects cannot know its old record survived", err)
	}
	if !second.RegisteredAt.Equal(first.RegisteredAt) {
		t.Errorf("RegisteredAt = %s, want the original %s — registering again is not arriving for the first time",
			second.RegisteredAt, first.RegisteredAt)
	}
	if !second.LastSeen.After(first.LastSeen) {
		t.Errorf("LastSeen = %s, want it moved past %s", second.LastSeen, first.LastSeen)
	}
}

func TestRegisterRefusesAnEmptyIdentifier(t *testing.T) {
	reg, _ := testRegistry(t, 10)

	if _, err := reg.Register("  "); err == nil {
		t.Error("Register(\"  \") error = nil, want a refusal")
	}
}

func TestDeregisterRemovesThePeer(t *testing.T) {
	reg, _ := testRegistry(t, 10)

	if _, err := reg.Register("peer-001"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	reg.Deregister("peer-001")

	if _, ok := reg.Lookup("peer-001"); ok {
		t.Error("Lookup() found a peer that deregistered")
	}
}

func TestDeregisterAPeerThatIsNotThere(t *testing.T) {
	reg, _ := testRegistry(t, 10)

	// The caller wanted it gone and it is gone; this must not panic or complain.
	reg.Deregister("never-here")
}

func TestLookupAnswersTheSameForNeverHereAndGone(t *testing.T) {
	reg, clock := testRegistry(t, 10)

	if _, err := reg.Register("peer-001"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	*clock = clock.Add(2 * time.Minute)

	gone, goneOK := reg.Lookup("peer-001")
	never, neverOK := reg.Lookup("peer-999")

	if goneOK || neverOK {
		t.Fatalf("Lookup() reported %t for a gone peer and %t for one that never registered, want false for both", goneOK, neverOK)
	}
	if gone != never {
		t.Errorf("a gone peer answers %+v and one that never registered answers %+v; telling them apart says who used to be here",
			gone, never)
	}
}

func TestStateFollowsSilence(t *testing.T) {
	cases := []struct {
		name    string
		silence time.Duration
		want    State
		held    bool
	}{
		{name: "just reported in", silence: 0, want: Online, held: true},
		{name: "inside the suspect window", silence: 29 * time.Second, want: Online, held: true},
		{name: "on the suspect boundary", silence: 30 * time.Second, want: Online, held: true},
		{name: "past the suspect window", silence: 31 * time.Second, want: Suspect, held: true},
		{name: "on the offline boundary", silence: 60 * time.Second, want: Suspect, held: true},
		{name: "past the offline window", silence: 61 * time.Second, held: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg, clock := testRegistry(t, 10)
			if _, err := reg.Register("peer-001"); err != nil {
				t.Fatalf("Register() error = %v", err)
			}

			*clock = clock.Add(c.silence)

			peer, ok := reg.Lookup("peer-001")
			if ok != c.held {
				t.Fatalf("Lookup() held = %t after %s of silence, want %t", ok, c.silence, c.held)
			}
			if c.held && peer.State != c.want {
				t.Errorf("State = %q after %s of silence, want %q", peer.State, c.silence, c.want)
			}
		})
	}
}

func TestHeartbeatReturnsASuspectPeerToOnline(t *testing.T) {
	reg, clock := testRegistry(t, 10)

	if _, err := reg.Register("peer-001"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	*clock = clock.Add(45 * time.Second)

	if peer, _ := reg.Lookup("peer-001"); peer.State != Suspect {
		t.Fatalf("State = %q, want %q before the heartbeat", peer.State, Suspect)
	}

	peer, err := reg.Heartbeat("peer-001")
	if err != nil {
		t.Fatalf("Heartbeat() error = %v, want nil for a suspect peer", err)
	}
	if peer.State != Online {
		t.Errorf("State = %q after a heartbeat, want %q", peer.State, Online)
	}
}

func TestHeartbeatRefusesAPeerThatWasRemoved(t *testing.T) {
	reg, clock := testRegistry(t, 10)

	if _, err := reg.Register("peer-001"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	*clock = clock.Add(2 * time.Minute)

	if _, err := reg.Heartbeat("peer-001"); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("Heartbeat() error = %v, want ErrNotRegistered — the peer has to know to register again", err)
	}
	if _, ok := reg.Lookup("peer-001"); ok {
		t.Error("the expired record survived a refused heartbeat")
	}
}

func TestHeartbeatForAPeerThatNeverRegistered(t *testing.T) {
	reg, _ := testRegistry(t, 10)

	if _, err := reg.Heartbeat("peer-999"); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("Heartbeat() error = %v, want ErrNotRegistered", err)
	}
}

func TestHeartbeatDoesNotMoveTheRegistrationMoment(t *testing.T) {
	reg, clock := testRegistry(t, 10)

	registered, err := reg.Register("peer-001")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	*clock = clock.Add(5 * time.Second)

	beat, err := reg.Heartbeat("peer-001")
	if err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if !beat.RegisteredAt.Equal(registered.RegisteredAt) {
		t.Errorf("RegisteredAt = %s, want the original %s", beat.RegisteredAt, registered.RegisteredAt)
	}
}

func TestRegisterRefusesPastTheCeiling(t *testing.T) {
	reg, _ := testRegistry(t, 2)

	for i := range 2 {
		if _, err := reg.Register("peer-" + strconv.Itoa(i)); err != nil {
			t.Fatalf("Register() error = %v on peer %d, want nil", err, i)
		}
	}

	if _, err := reg.Register("one-too-many"); !errors.Is(err, ErrFull) {
		t.Errorf("Register() error = %v, want ErrFull", err)
	}
	if _, ok := reg.Lookup("peer-0"); !ok {
		t.Error("an online peer was evicted to admit a new one; a working peer outranks a possible one")
	}
}

func TestAFullRegistryAdmitsAgainOnceRecordsExpire(t *testing.T) {
	reg, clock := testRegistry(t, 2)

	for i := range 2 {
		if _, err := reg.Register("peer-" + strconv.Itoa(i)); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}
	*clock = clock.Add(2 * time.Minute)

	if _, err := reg.Register("newcomer"); err != nil {
		t.Errorf("Register() error = %v, want nil — records nobody will hear from again do not hold a place", err)
	}
}

func TestRegisterAgainWhileFull(t *testing.T) {
	reg, _ := testRegistry(t, 1)

	if _, err := reg.Register("peer-001"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if _, err := reg.Register("peer-001"); err != nil {
		t.Errorf("Register() error = %v for a peer already held, want nil — it takes no new room", err)
	}
}

func TestExpireRemovesWithoutBeingAsked(t *testing.T) {
	reg, clock := testRegistry(t, 10)

	for i := range 3 {
		if _, err := reg.Register("peer-" + strconv.Itoa(i)); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}
	*clock = clock.Add(2 * time.Minute)

	if removed := reg.Expire(); removed != 3 {
		t.Errorf("Expire() = %d, want 3", removed)
	}
	if got := reg.Count().Total; got != 0 {
		t.Errorf("Total = %d after expiry, want 0", got)
	}
}

func TestExpireLeavesLivePeersAlone(t *testing.T) {
	reg, clock := testRegistry(t, 10)

	if _, err := reg.Register("stale"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	*clock = clock.Add(2 * time.Minute)
	if _, err := reg.Register("fresh"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if removed := reg.Expire(); removed != 1 {
		t.Errorf("Expire() = %d, want 1", removed)
	}
	if _, ok := reg.Lookup("fresh"); !ok {
		t.Error("Expire() removed a peer that had just reported in")
	}
}

func TestCountReportsEachState(t *testing.T) {
	reg, clock := testRegistry(t, 10)

	if _, err := reg.Register("suspect-soon"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	*clock = clock.Add(40 * time.Second)
	if _, err := reg.Register("fresh"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	counts := reg.Count()
	if counts.Online != 1 || counts.Suspect != 1 || counts.Total != 2 {
		t.Errorf("Count() = %+v, want one online, one suspect, two held", counts)
	}
}

func TestNewRejects(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{name: "no heartbeat interval", opts: Options{SuspectAfter: 30 * time.Second, OfflineAfter: time.Minute}},
		{
			name: "a suspect window inside the heartbeat interval",
			opts: Options{HeartbeatInterval: 30 * time.Second, SuspectAfter: 15 * time.Second, OfflineAfter: time.Minute},
		},
		{
			name: "an offline window inside the suspect window",
			opts: Options{HeartbeatInterval: 10 * time.Second, SuspectAfter: 60 * time.Second, OfflineAfter: 30 * time.Second},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.opts); err == nil {
				t.Error("New() error = nil, want a refusal")
			}
		})
	}
}

func TestDefaultCeilingApplies(t *testing.T) {
	reg, err := New(Options{
		HeartbeatInterval: 10 * time.Second,
		SuspectAfter:      30 * time.Second,
		OfflineAfter:      60 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if reg.ceiling != DefaultMaxPeers {
		t.Errorf("ceiling = %d, want %d", reg.ceiling, DefaultMaxPeers)
	}
	if reg.HeartbeatInterval() != 10*time.Second {
		t.Errorf("HeartbeatInterval() = %s, want 10s", reg.HeartbeatInterval())
	}
}

func TestRegistryIsSafeUnderConcurrentUse(t *testing.T) {
	reg, err := New(Options{
		HeartbeatInterval: 10 * time.Millisecond,
		SuspectAfter:      50 * time.Millisecond,
		OfflineAfter:      100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "peer-" + strconv.Itoa(n%7)

			_, _ = reg.Register(id)
			_, _ = reg.Heartbeat(id)
			reg.Lookup(id)
			reg.Count()
			reg.Expire()
			if n%3 == 0 {
				reg.Deregister(id)
			}
		}(i)
	}
	wg.Wait()
}

func TestSweepExpiresWithoutBeingRead(t *testing.T) {
	reg, err := New(Options{
		HeartbeatInterval: time.Millisecond,
		SuspectAfter:      2 * time.Millisecond,
		OfflineAfter:      5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if _, err := reg.Register("peer-001"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	expired := make(chan int, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reg.Sweep(ctx, time.Millisecond, func(n int) {
		select {
		case expired <- n:
		default:
		}
	})

	select {
	case n := <-expired:
		if n != 1 {
			t.Errorf("Sweep() reported %d expired, want 1", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Sweep() never expired a record nobody read")
	}

	if got := reg.Count().Total; got != 0 {
		t.Errorf("Total = %d, want 0 after the sweep", got)
	}
}

func TestSweepStopsWithItsContext(t *testing.T) {
	reg, _ := testRegistry(t, 10)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reg.Sweep(ctx, time.Millisecond, nil)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Sweep() did not return when its context was cancelled")
	}
}

func TestSweepFallsBackToTheHeartbeatInterval(t *testing.T) {
	reg, _ := testRegistry(t, 10)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// A zero interval must not panic on time.NewTicker; it takes the heartbeat.
	reg.Sweep(ctx, 0, nil)
}
