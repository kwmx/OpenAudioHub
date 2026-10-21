package oah

import (
	"path/filepath"
	"testing"
	"time"
)

func newBackoffApp(t *testing.T) *App {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, defaultConfig()); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A device that is switched off must not be retried on every reconcile: the
// journal floods and, worse, the retry races any connect the user starts by hand.
func TestConnectBackoffGrowsAndBlocks(t *testing.T) {
	a := newBackoffApp(t)
	now := time.Now()
	const addr = "AA:BB:CC:DD:EE:FF"

	if a.connectTooSoon(addr, now) {
		t.Fatal("first attempt must be allowed")
	}
	a.noteConnectAttempt(addr, false, now)
	if !a.connectTooSoon(addr, now) {
		t.Fatal("immediate retry after a failure must be blocked")
	}
	if a.connectTooSoon(addr, now.Add(31*time.Second)) {
		t.Fatal("retry should be allowed once the first backoff elapses")
	}

	// Each consecutive failure must widen the window.
	prev := time.Duration(0)
	for i := 0; i < 6; i++ {
		a.noteConnectAttempt(addr, false, now)
		wait := 0 * time.Second
		for d := time.Second; d < 10*time.Minute; d += time.Second {
			if !a.connectTooSoon(addr, now.Add(d)) {
				wait = d
				break
			}
		}
		if wait < prev {
			t.Fatalf("backoff shrank: %v then %v", prev, wait)
		}
		prev = wait
	}
	if prev > 6*time.Minute {
		t.Fatalf("backoff grew unbounded: %v", prev)
	}
}

func TestConnectSuccessClearsBackoff(t *testing.T) {
	a := newBackoffApp(t)
	now := time.Now()
	const addr = "AA:BB:CC:DD:EE:FF"
	a.noteConnectAttempt(addr, false, now)
	a.noteConnectAttempt(addr, false, now)
	if a.connectFailureCount(addr) != 2 {
		t.Fatalf("failure count %d", a.connectFailureCount(addr))
	}
	a.noteConnectAttempt(addr, true, now)
	if a.connectFailureCount(addr) != 0 {
		t.Fatalf("success did not clear failures: %d", a.connectFailureCount(addr))
	}
	if a.connectTooSoon(addr, now) {
		t.Fatal("a healthy device must not be throttled")
	}
}

// A deliberate action from the UI must not be throttled by earlier auto-connect
// failures, and a role change resets everything.
func TestDeliberateActionClearsBackoff(t *testing.T) {
	a := newBackoffApp(t)
	now := time.Now()
	const a1, a2 = "AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02"
	a.noteConnectAttempt(a1, false, now)
	a.noteConnectAttempt(a2, false, now)
	a.forgetConnectState(a1)
	if a.connectTooSoon(a1, now) {
		t.Fatal("forgetConnectState did not clear the address")
	}
	if !a.connectTooSoon(a2, now) {
		t.Fatal("clearing one address cleared another")
	}
	a.resetConnectState()
	if a.connectTooSoon(a2, now) {
		t.Fatal("resetConnectState did not clear all addresses")
	}
}

// The timeouts are the point of the fix: they must be long enough that Bluetooth
// operations are not killed mid-flight.
func TestBluetoothTimeoutsAreGenerous(t *testing.T) {
	if pairTimeout < 45*time.Second {
		t.Fatalf("pairTimeout %v is short enough to abort pairing", pairTimeout)
	}
	if connectTimeout < 30*time.Second {
		t.Fatalf("connectTimeout %v is short enough to abort a connect", connectTimeout)
	}
	if connectTimeout > pairTimeout {
		t.Fatalf("connect timeout %v should not exceed pair timeout %v", connectTimeout, pairTimeout)
	}
}

// Paging an absent device stalls the links that are playing, so while audio is
// heard a device that failed to answer is retried only every streamingRetry.
func TestAbsentDevicesAreRetriedRarelyWhileAudioPlays(t *testing.T) {
	a := &App{}
	const addr = "AA:BB:CC:DD:EE:01"
	now := time.Now()
	a.noteConnectAttempt(addr, false, now)
	playing := []Transport{
		{UUID: "Audio Sink (0000110b-0000-1000-8000-00805f9b34fb)", State: "active"},
		{UUID: "Audio Source (0000110a-0000-1000-8000-00805f9b34fb)", State: "active"},
	}
	a.noteTransports(playing)
	if !a.connectTooSoon(addr, now.Add(time.Minute)) {
		t.Fatal("while audio plays the 30 s backoff must not apply")
	}
	if a.connectTooSoon(addr, now.Add(streamingRetry+time.Second)) {
		t.Fatal("the device must still be retried eventually")
	}

	// A source playing with no output connected is heard by nobody: keep
	// looking for the output at the normal pace.
	a.noteTransports(playing[:1])
	if a.connectTooSoon(addr, now.Add(time.Minute)) {
		t.Fatal("with no output connected the normal backoff applies")
	}
	// Paused source: the output's silent stream does not count as playing.
	a.noteTransports([]Transport{{UUID: playing[0].UUID, State: "idle"}, playing[1]})
	if a.connectTooSoon(addr, now.Add(time.Minute)) {
		t.Fatal("a paused source must not slow reconnects")
	}

	// A device that has not failed is tried straight away even while playing.
	a.noteTransports(playing)
	if a.connectTooSoon("AA:BB:CC:DD:EE:02", now) {
		t.Fatal("a first attempt is never delayed")
	}
}
