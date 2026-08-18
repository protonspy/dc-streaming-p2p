package auth

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// testLimiter builds a limiter with a clock the test moves by hand.
func testLimiter(t *testing.T, allowance int, window time.Duration) (*AttemptLimiter, *time.Time) {
	t.Helper()

	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	limiter, err := NewAttemptLimiter(allowance, window)
	if err != nil {
		t.Fatalf("NewAttemptLimiter() error = %v, want nil", err)
	}
	limiter.now = func() time.Time { return clock }

	return limiter, &clock
}

func TestAllowedUntilTheAllowanceIsSpent(t *testing.T) {
	limiter, _ := testLimiter(t, 3, time.Minute)

	for i := range 3 {
		if !limiter.Allowed("sdk-web") {
			t.Fatalf("Allowed() = false on failure %d, want true until the allowance is spent", i+1)
		}
		limiter.Failed("sdk-web")
	}

	if limiter.Allowed("sdk-web") {
		t.Error("Allowed() = true after the allowance was spent, want false")
	}
}

func TestRefusesEvenTheRightSecretWhileBlocked(t *testing.T) {
	limiter, _ := testLimiter(t, 1, time.Minute)

	limiter.Failed("sdk-web")

	if limiter.Allowed("sdk-web") {
		t.Error("Allowed() = true while blocked; the limit must not become a hint that a secret was close")
	}
}

func TestAllowedAgainOnceTheWindowPasses(t *testing.T) {
	limiter, clock := testLimiter(t, 1, time.Minute)

	limiter.Failed("sdk-web")
	if limiter.Allowed("sdk-web") {
		t.Fatal("Allowed() = true immediately after the allowance was spent, want false")
	}

	*clock = clock.Add(time.Minute + time.Second)

	if !limiter.Allowed("sdk-web") {
		t.Error("Allowed() = false after the window passed, want true")
	}
}

func TestSucceededClearsTheCount(t *testing.T) {
	limiter, _ := testLimiter(t, 3, time.Minute)

	limiter.Failed("sdk-web")
	limiter.Failed("sdk-web")
	limiter.Succeeded("sdk-web")

	for i := range 3 {
		if !limiter.Allowed("sdk-web") {
			t.Fatalf("Allowed() = false on failure %d after a success cleared the count, want true", i+1)
		}
		limiter.Failed("sdk-web")
	}
}

func TestOneIdentifierDoesNotBlockAnother(t *testing.T) {
	limiter, _ := testLimiter(t, 1, time.Minute)

	limiter.Failed("sdk-web")

	if !limiter.Allowed("sdk-mobile") {
		t.Error("Allowed() = false for an identifier that has not failed, want true")
	}
}

func TestFailuresOutsideTheWindowDoNotAccumulate(t *testing.T) {
	limiter, clock := testLimiter(t, 2, time.Minute)

	limiter.Failed("sdk-web")
	*clock = clock.Add(2 * time.Minute)
	limiter.Failed("sdk-web")

	if !limiter.Allowed("sdk-web") {
		t.Error("Allowed() = false, want true — a failure older than the window is not part of this one")
	}
}

func TestForgetsIdentifiersItNoLongerCountsFor(t *testing.T) {
	limiter, clock := testLimiter(t, 2, time.Minute)

	for i := range sweepThreshold + 1 {
		limiter.Failed("client-" + strconv.Itoa(i))
	}
	*clock = clock.Add(2 * time.Minute)

	// One more failure past the window is what triggers the sweep.
	limiter.Failed("late-arrival")

	if got := limiter.Tracked(); got > 1 {
		t.Errorf("Tracked() = %d, want the expired entries dropped — a limiter that never forgets is a memory leak", got)
	}
}

func TestNewAttemptLimiterRejects(t *testing.T) {
	cases := []struct {
		name      string
		allowance int
		window    time.Duration
	}{
		{name: "an allowance of zero, which would refuse everyone", allowance: 0, window: time.Minute},
		{name: "a negative allowance", allowance: -1, window: time.Minute},
		{name: "a window of zero", allowance: 3, window: 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewAttemptLimiter(c.allowance, c.window); err == nil {
				t.Error("NewAttemptLimiter() error = nil, want a refusal")
			}
		})
	}
}

func TestLimiterIsSafeUnderConcurrentUse(t *testing.T) {
	limiter, err := NewAttemptLimiter(100, time.Minute)
	if err != nil {
		t.Fatalf("NewAttemptLimiter() error = %v, want nil", err)
	}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "client-" + strconv.Itoa(n%5)
			limiter.Allowed(id)
			limiter.Failed(id)
			limiter.Succeeded(id)
		}(i)
	}
	wg.Wait()
}
