package oah

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func newDelayApp(t *testing.T, input2 string, requestedMS int) *App {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	c := defaultConfig()
	c.Slots.Inputs = []string{"AA:AA:AA:AA:AA:01", input2}
	c.Audio.SecondaryAdvertisedDelayMS = requestedMS
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// fakeReceiver installs a stand-in for the isolated receiver binary. The patched
// daemon reads this variable name, so its presence is the capability marker.
func fakeReceiver(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bluealsa-oah")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAUDIOHUB_RECEIVER_BIN", p)
}

func writeDelayState(t *testing.T, requested int, result, detail string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "delay-report.state")
	body := "transport=/org/bluez/hci0/dev_00_00_5E_00_53_01/a2dpsnk/source\n"
	body += fmt.Sprintf("requested_ms=%d\nresult=%s\n", requested, result)
	if detail != "" {
		body += "detail=" + detail + "\n"
	}
	body += "epoch=1700000000\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAUDIOHUB_DELAY_STATE_FILE", p)
}

// The UI and configuration use whole milliseconds; BlueZ MediaTransport1.Delay is
// in 0.1 ms units, matching g_variant_new_uint16(ms * 10) in the C patch.
func TestDelayUnitConversion(t *testing.T) {
	if delayMSToUnits(150) != 1500 {
		t.Fatalf("150 ms must be 1500 BlueZ units, got %d", delayMSToUnits(150))
	}
	if delayUnitsToMS(1500) != 150 {
		t.Fatalf("1500 BlueZ units must be 150 ms, got %d", delayUnitsToMS(1500))
	}
	for _, ms := range []int{0, 1, 10, 35, 150, 999, 2000} {
		if got := delayUnitsToMS(delayMSToUnits(ms)); got != ms {
			t.Fatalf("%d ms round-tripped to %d", ms, got)
		}
	}
}

func TestDelayReportStates(t *testing.T) {
	const in2 = "00:00:5E:00:53:01"
	sink := func(units int, known bool) []Transport {
		return []Transport{{Path: "/org/bluez/hci0/dev_00_00_5E_00_53_01/a2dpsnk/source",
			Addr: in2, UUID: "Audio Sink", Delay: units, DelayKnown: known}}
	}

	t.Run("zero keeps the engine default", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 0)
		rep := a.delayReport(sink(1500, true))
		if rep.State != delayDefault {
			t.Fatalf("state %q", rep.State)
		}
	})

	t.Run("unsupported when the receiver has no patch", func(t *testing.T) {
		a := newDelayApp(t, in2, 150)
		t.Setenv("OPENAUDIOHUB_RECEIVER_BIN", filepath.Join(t.TempDir(), "missing"))
		rep := a.delayReport(sink(0, false))
		if rep.State != delayUnsupported || rep.Capable {
			t.Fatalf("state %q capable %v", rep.State, rep.Capable)
		}
	})

	t.Run("pending when no secondary transport is acquired", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 150)
		rep := a.delayReport(nil)
		if rep.State != delayPending {
			t.Fatalf("state %q", rep.State)
		}
	})

	t.Run("reported only when the acquired transport shows the value", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 150)
		writeDelayState(t, 150, "reported", "")
		rep := a.delayReport(sink(1500, true))
		if rep.State != delayReported {
			t.Fatalf("state %q detail %q", rep.State, rep.Detail)
		}
		if rep.ActualMS != 150 || !rep.ActualKnown {
			t.Fatalf("actual %d known %v", rep.ActualMS, rep.ActualKnown)
		}
		if rep.Attempt != "reported" || rep.AttemptedAt == "" {
			t.Fatalf("attempt %q at %q", rep.Attempt, rep.AttemptedAt)
		}
	})

	t.Run("rejected when the owning write was refused", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 150)
		writeDelayState(t, 150, "rejected", "org.bluez.Error.NotAuthorized")
		rep := a.delayReport(sink(0, true))
		if rep.State != delayRejected || rep.Attempt != "rejected" {
			t.Fatalf("state %q attempt %q", rep.State, rep.Attempt)
		}
		if rep.Detail != "org.bluez.Error.NotAuthorized" {
			t.Fatalf("detail %q", rep.Detail)
		}
	})

	t.Run("mismatch when accepted but the transport does not show it", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 150)
		writeDelayState(t, 150, "reported", "")
		if got := a.delayReport(sink(0, true)).State; got != delayMismatch {
			t.Fatalf("state %q", got)
		}
	})

	t.Run("pending when BlueZ exposes no delay property", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 150)
		writeDelayState(t, 150, "reported", "")
		rep := a.delayReport(sink(0, false))
		if rep.State != delayPending || rep.ActualKnown {
			t.Fatalf("state %q actualKnown %v", rep.State, rep.ActualKnown)
		}
	})

	t.Run("a record for an older value never describes the current request", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 300)
		writeDelayState(t, 150, "rejected", "old failure")
		rep := a.delayReport(sink(0, true))
		if rep.Attempt != "" || rep.Detail == "old failure" {
			t.Fatalf("stale record leaked: attempt %q detail %q", rep.Attempt, rep.Detail)
		}
		if rep.State != delayPending {
			t.Fatalf("state %q", rep.State)
		}
	})

	t.Run("only the secondary input is considered", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 150)
		writeDelayState(t, 150, "reported", "")
		other := []Transport{{Path: "x", Addr: "11:22:33:44:55:66", UUID: "Audio Sink", Delay: 1500, DelayKnown: true}}
		if got := a.delayReport(other).State; got != delayPending {
			t.Fatalf("a different device was treated as the secondary input: %q", got)
		}
	})

	t.Run("the output source transport is not the reported sink", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 150)
		writeDelayState(t, 150, "reported", "")
		asSource := []Transport{{Path: "y", Addr: in2, UUID: "Audio Source", Delay: 1500, DelayKnown: true}}
		if got := a.delayReport(asSource).State; got != delayPending {
			t.Fatalf("state %q", got)
		}
	})

	t.Run("the report does not depend on the selected output", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 150)
		writeDelayState(t, 150, "reported", "")
		before := a.delayReport(sink(1500, true))
		if before.State != delayReported {
			t.Fatalf("baseline state %q", before.State)
		}
		// Changing or clearing Output 1 must not disturb the secondary input report.
		if err := a.cfg.Update(func(c *Config) error { c.Slots.Outputs = []string{""}; return nil }); err != nil {
			t.Fatal(err)
		}
		after := a.delayReport(sink(1500, true))
		if after.State != before.State || after.ActualMS != before.ActualMS || after.RequestedMS != before.RequestedMS {
			t.Fatalf("output change altered the report: %q/%d -> %q/%d", before.State, before.ActualMS, after.State, after.ActualMS)
		}
	})

	t.Run("an unparsable state file is ignored", func(t *testing.T) {
		fakeReceiver(t, "receiver with OAH_ADVERTISED_DELAY_MS")
		a := newDelayApp(t, in2, 150)
		p := filepath.Join(t.TempDir(), "delay-report.state")
		if err := os.WriteFile(p, []byte("garbage without keys\n\x00\x01"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OPENAUDIOHUB_DELAY_STATE_FILE", p)
		if got := a.delayReport(sink(0, true)).State; got != delayPending {
			t.Fatalf("state %q", got)
		}
	})
}

func TestDelayBounds(t *testing.T) {
	for _, n := range []int{-1, 2001, 65535} {
		c := defaultConfig().Audio
		c.SecondaryAdvertisedDelayMS = n
		if validateReceiverOptions(c) == nil {
			t.Fatalf("accepted %d ms", n)
		}
	}
	for _, n := range []int{0, 1, 150, 2000} {
		c := defaultConfig().Audio
		c.SecondaryAdvertisedDelayMS = n
		if err := validateReceiverOptions(c); err != nil {
			t.Fatalf("%d ms rejected: %v", n, err)
		}
	}
}

// Reset must persist as an explicit 0 and survive normalization, otherwise the
// engine default would be silently replaced on the next load.
func TestDelayPersistsAndResetsToZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, defaultConfig()); err != nil {
		t.Fatal(err)
	}
	store, err := newConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *Config) error { c.Audio.SecondaryAdvertisedDelayMS = 150; return nil }); err != nil {
		t.Fatal(err)
	}
	back, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Audio.SecondaryAdvertisedDelayMS != 150 {
		t.Fatalf("persisted %d", back.Audio.SecondaryAdvertisedDelayMS)
	}
	if err := store.Update(func(c *Config) error { c.Audio.SecondaryAdvertisedDelayMS = 0; return nil }); err != nil {
		t.Fatal(err)
	}
	back, err = loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Audio.SecondaryAdvertisedDelayMS != 0 {
		t.Fatalf("reset persisted %d", back.Audio.SecondaryAdvertisedDelayMS)
	}
	normalizeConfig(&back)
	if back.Audio.SecondaryAdvertisedDelayMS != 0 {
		t.Fatalf("normalization resurrected %d", back.Audio.SecondaryAdvertisedDelayMS)
	}
}

// The unprivileged bridge reads audio.json, so the requested total has to survive
// the projection or the receiver never sees it.
func TestDelayReachesAudioProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := defaultConfig()
	c.Audio.SecondaryAdvertisedDelayMS = 175
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.writeAudioProjection(c.Audio); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "audio.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" || !json.Valid(b) {
		t.Fatal("projection is not valid JSON")
	}
	var out AudioConfig
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.SecondaryAdvertisedDelayMS != 175 {
		t.Fatalf("projection lost the delay: %d", out.SecondaryAdvertisedDelayMS)
	}
	if out.SecondarySBCMaxBitpool != c.Audio.SecondarySBCMaxBitpool {
		t.Fatal("projection disturbed the SBC cap")
	}
}

// A shared receiver binary is the capability source for every call; make sure a
// stale cache entry cannot report a different binary's capability.
func TestReceiverCapabilityTracksTheFile(t *testing.T) {
	dir := t.TempDir()
	withPatch := filepath.Join(dir, "with")
	without := filepath.Join(dir, "without")
	if err := os.WriteFile(withPatch, []byte("...OAH_ADVERTISED_DELAY_MS..."), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(without, []byte("plain upstream binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAUDIOHUB_RECEIVER_BIN", withPatch)
	if !receiverSupportsDelayReport() {
		t.Fatal("patched receiver not detected")
	}
	t.Setenv("OPENAUDIOHUB_RECEIVER_BIN", without)
	if receiverSupportsDelayReport() {
		t.Fatal("unpatched receiver reported as capable")
	}
	t.Setenv("OPENAUDIOHUB_RECEIVER_BIN", filepath.Join(dir, "absent"))
	if receiverSupportsDelayReport() {
		t.Fatal("absent receiver reported as capable")
	}
}
