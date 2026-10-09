package oah

import (
	"math"
	"testing"
	"time"
)

// pactl percentages are cubic in amplitude, so 0 dB is 100% and -6 dB is about
// 79%, not the 50% a linear mapping gives (which is really -18 dB).
func TestDBToPercentUsesCubicVolumeScale(t *testing.T) {
	cases := map[float64]int{0: 100, -6: 79, -20: 46, -60: 10, 6: 126, 30: 150}
	for db, want := range cases {
		if got := dbToPercent(db); got != want {
			t.Errorf("dbToPercent(%v)=%d, want %d", db, got, want)
		}
	}
	// Round trip: the percentage must represent the requested dB within rounding.
	for _, db := range []float64{-30, -12, -6, -1} {
		p := float64(dbToPercent(db)) / 100
		back := 60 * math.Log10(p)
		if math.Abs(back-db) > 0.5 {
			t.Errorf("%v dB -> %d%% -> %.2f dB", db, dbToPercent(db), back)
		}
	}
}

func TestLoginLimiterLocksOutAfterRepeatedFailures(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1_000_000, 0)
	for i := 0; i < loginFreeFailures-1; i++ {
		l.fail("10.0.0.2", now)
		if d := l.retryAfter("10.0.0.2", now); d != 0 {
			t.Fatalf("failure %d already locked out for %v", i+1, d)
		}
	}
	l.fail("10.0.0.2", now)
	if d := l.retryAfter("10.0.0.2", now); d != loginBaseLockout {
		t.Fatalf("lockout %v, want %v", d, loginBaseLockout)
	}
	if d := l.retryAfter("10.0.0.3", now); d != 0 {
		t.Fatalf("another client must not be locked out, got %v", d)
	}
	l.fail("10.0.0.2", now)
	if d := l.retryAfter("10.0.0.2", now); d != 2*loginBaseLockout {
		t.Fatalf("second lockout %v, want doubled", d)
	}
	for i := 0; i < 20; i++ {
		l.fail("10.0.0.2", now)
	}
	if d := l.retryAfter("10.0.0.2", now); d != loginMaxLockout {
		t.Fatalf("lockout %v, want capped at %v", d, loginMaxLockout)
	}
	l.succeed("10.0.0.2")
	if d := l.retryAfter("10.0.0.2", now); d != 0 {
		t.Fatalf("success must clear the lockout, got %v", d)
	}
}

func TestLoginLimiterForgetsOldFailures(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1_000_000, 0)
	for i := 0; i < loginFreeFailures-1; i++ {
		l.fail("a", now)
	}
	later := now.Add(loginForgetAfter + time.Minute)
	l.fail("a", later)
	if d := l.retryAfter("a", later); d != 0 {
		t.Fatalf("stale failures must not count, locked for %v", d)
	}
}

func TestNetworkApplyPending(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	ap := &NetworkApply{State: "verifying", StartedAt: start}
	if !networkApplyPending(ap, start.Add(10*time.Second)) {
		t.Fatal("an unconfirmed apply inside the rollback window is pending")
	}
	if networkApplyPending(ap, start.Add(networkRollbackDelay+time.Minute)) {
		t.Fatal("an apply whose rollback has finished is not pending")
	}
	ap.State = "ok"
	if networkApplyPending(ap, start.Add(time.Second)) {
		t.Fatal("a confirmed apply is not pending")
	}
}

func TestSixGHzBand(t *testing.T) {
	if b := bandForFreq(5975); b != "6 GHz" {
		t.Fatalf("band %q", b)
	}
	if c := channelForFreq(5975); c != 5 {
		t.Fatalf("channel %d, want 5", c)
	}
	if b, c := bandForFreq(5180), channelForFreq(5180); b != "5 GHz" || c != 36 {
		t.Fatalf("5 GHz regression: %q %d", b, c)
	}
}

// The main.conf Name pattern must not consume the lines around the key.
func TestMainConfNamePatternKeepsSurroundingLines(t *testing.T) {
	in := "[General]\n\n# comment\n\n#Name = BlueZ\nClass = 0x200414\n"
	got := setMainConfName(in, "Hub")
	want := "[General]\n\n# comment\n\nName = Hub\nClass = 0x200414\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMainConfNameAddsKeyWhenMissing(t *testing.T) {
	if got := setMainConfName("", "Hub"); got != "[General]\nName = Hub\n" {
		t.Fatalf("got %q", got)
	}
}
