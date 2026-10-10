package oah

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const bondAddr = "AA:BB:CC:DD:EE:01"

// withBluezStore points the BlueZ device store at a temporary directory and
// returns a function that writes addr's info file there.
func withBluezStore(t *testing.T) func(addr, info string) {
	t.Helper()
	root := t.TempDir()
	old := bluezStorageRoot
	bluezStorageRoot = root
	oldWait := pairKeyWait
	pairKeyWait = time.Millisecond
	t.Cleanup(func() { bluezStorageRoot, pairKeyWait = old, oldWait })
	return func(addr, info string) {
		dir := filepath.Join(root, "11:22:33:44:55:66", addr)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "info"), []byte(info), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBluezBondHasKey(t *testing.T) {
	cases := []struct {
		name, info string
		want       bool
	}{
		{"BR/EDR link key", "[General]\nName=Headphones\nTrusted=true\n\n[LinkKey]\nKey=00112233445566778899AABBCCDDEEFF\nType=4\nPINLength=0\n", true},
		{"LE long-term key", "[General]\nName=Buds\n\n[LongTermKey]\nKey=00112233445566778899AABBCCDDEEFF\nAuthenticated=0\n", true},
		{"legacy slave LTK", "[General]\n[SlaveLongTermKey]\nKey=0011\n", true},
		{"record without a key", "[General]\nName=Headphones\nTrusted=true\nBlocked=false\n", false},
		{"Key= outside a key section", "[General]\nName=Headphones\n\n[IdentityResolvingKey]\nKey=0011\n\n[ServiceRecords]\nKey=0011\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			write := withBluezStore(t)
			write(bondAddr, c.info)
			if got := bluezBondHasKey(bondAddr); got != c.want {
				t.Fatalf("bluezBondHasKey = %v, want %v", got, c.want)
			}
		})
	}
	withBluezStore(t)
	if bluezBondHasKey(bondAddr) {
		t.Fatal("a device with no record has no key")
	}
}

// The "paired before, key gone" message is for a device BlueZ remembers without a
// key. The original draft showed it when a key was present, the opposite case.
func TestLostBondReason(t *testing.T) {
	write := withBluezStore(t)
	if r := lostBondReason(bondAddr, false); r != "" {
		t.Fatalf("a device only seen in a scan must not be called previously paired: %q", r)
	}
	write(bondAddr, "[General]\nName=Headphones\nTrusted=true\n")
	if r := lostBondReason(bondAddr, false); !strings.Contains(r, "key is gone") {
		t.Fatalf("a remembered device without a key needs the explanation, got %q", r)
	}
	if r := lostBondReason(bondAddr, true); r != "" {
		t.Fatalf("a paired device needs no explanation, got %q", r)
	}
	write(bondAddr, "[General]\nName=Headphones\n[LinkKey]\nKey=0011\n")
	if r := lostBondReason(bondAddr, false); r != "" {
		t.Fatalf("a device with a stored key has not lost it, got %q", r)
	}
}

type recordedRunner struct {
	mu    sync.Mutex
	calls []string
	reply func(call string) (string, error)
}

func (r *recordedRunner) run(name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	if r.reply != nil {
		return r.reply(call)
	}
	return "", nil
}

func (r *recordedRunner) has(call string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c == call {
			return true
		}
	}
	return false
}

func TestMakeBondable(t *testing.T) {
	rec := &recordedRunner{}
	a := &App{run: runner{fake: rec.run}}
	restore, err := a.makeBondable()
	if err != nil {
		t.Fatal(err)
	}
	if !rec.has("bluetoothctl pairable on") {
		t.Fatal("the adapter must be pairable while pairing")
	}
	restore()
	if !rec.has("bluetoothctl pairable off") {
		t.Fatal("pairable must be switched back off afterwards")
	}

	// Pairing mode switched on during the attempt now owns the adapter state.
	rec = &recordedRunner{}
	a = &App{run: runner{fake: rec.run}}
	restore, _ = a.makeBondable()
	a.pairing = PairingState{Active: true, Until: time.Now().Add(time.Minute)}
	restore()
	if rec.has("bluetoothctl pairable off") {
		t.Fatal("restoring must not close a pairing window the user just opened")
	}

	// Pairing mode already on: nothing to change, nothing to restore.
	rec = &recordedRunner{}
	a = &App{run: runner{fake: rec.run}, pairing: PairingState{Active: true, Until: time.Now().Add(time.Minute)}}
	restore, _ = a.makeBondable()
	restore()
	if len(rec.calls) != 0 {
		t.Fatalf("no adapter changes expected during pairing mode, got %v", rec.calls)
	}

	rec = &recordedRunner{reply: func(string) (string, error) { return "", errors.New("org.bluez.Error.Failed") }}
	a = &App{run: runner{fake: rec.run}}
	if _, err := a.makeBondable(); err == nil {
		t.Fatal("a failure to make the adapter pairable must stop the pairing")
	}
}

func pairingApp(rec *recordedRunner) *App {
	return &App{run: runner{fake: rec.run}, deviceNames: map[string]string{}, deviceInfo: map[string]deviceInfoEntry{}, connectState: map[string]*connectState{}, policed: map[string]time.Time{}}
}

func pairingReply(call string) (string, error) {
	if call == "bluetoothctl info "+bondAddr {
		// BlueZ reports Paired: yes even for a pairing that stored no key.
		return "Device " + bondAddr + " (public)\n\tName: Headphones\n\tPaired: yes\n\tConnected: no\n", nil
	}
	return "", nil
}

func TestPairRequiresStoredKey(t *testing.T) {
	withBluezStore(t)
	rec := &recordedRunner{reply: pairingReply}
	a := pairingApp(rec)
	err := a.btAction(bondAddr, "pair")
	if err == nil || !isUserErr(err) {
		t.Fatalf("a pairing with no stored key must be reported as failed, got %v", err)
	}
	if rec.has("bluetoothctl trust " + bondAddr) {
		t.Fatal("a device whose pairing did not store a key must not be trusted")
	}
	if !rec.has("bluetoothctl pairable on") || !rec.has("bluetoothctl pairable off") {
		t.Fatalf("pairable must be held on for the attempt and restored, calls: %v", rec.calls)
	}
}

func TestPairSucceedsWithStoredKey(t *testing.T) {
	write := withBluezStore(t)
	write(bondAddr, "[General]\nName=Headphones\n\n[LinkKey]\nKey=00112233445566778899AABBCCDDEEFF\n")
	rec := &recordedRunner{reply: pairingReply}
	a := pairingApp(rec)
	if err := a.btAction(bondAddr, "pair"); err != nil {
		t.Fatal(err)
	}
	if !rec.has("bluetoothctl trust " + bondAddr) {
		t.Fatal("a bonded device should be trusted")
	}
	on, pair := -1, -1
	for i, c := range rec.calls {
		switch c {
		case "bluetoothctl pairable on":
			on = i
		case "bluetoothctl pair " + bondAddr:
			pair = i
		}
	}
	if on < 0 || pair < 0 || on > pair {
		t.Fatalf("the adapter must be pairable before pairing starts, calls: %v", rec.calls)
	}
}

func TestSessionOnlyPairingIsFlagged(t *testing.T) {
	withBluezStore(t)
	a := &App{deviceNames: map[string]string{}, deviceInfo: map[string]deviceInfoEntry{}}
	d := a.deviceFromBluez(bluezDevice{Addr: bondAddr, Name: "Headphones", Paired: true, HasBonded: true, Bonded: false})
	if !strings.Contains(d.Reason, "session only") {
		t.Fatalf("Paired without Bonded must be explained, got %q", d.Reason)
	}
	d = a.deviceFromBluez(bluezDevice{Addr: bondAddr, Name: "Headphones", Paired: true, HasBonded: true, Bonded: true})
	if d.Reason != "" {
		t.Fatalf("a bonded device needs no explanation, got %q", d.Reason)
	}
	// Older BlueZ has no Bonded property; never guess.
	d = a.deviceFromBluez(bluezDevice{Addr: bondAddr, Name: "Headphones", Paired: true})
	if d.Reason != "" {
		t.Fatalf("without a Bonded property nothing can be concluded, got %q", d.Reason)
	}
}

func TestDoctorFlagsSessionOnlyPairing(t *testing.T) {
	fake := healthyFake(t)
	fake.busctl = strings.Replace(fake.busctl, `"Bonded":{"type":"b","data":true}`, `"Bonded":{"type":"b","data":false}`, 1)
	a := doctorApp(t, fake)
	if err := a.cfg.Update(func(c *Config) error { c.Slots.Inputs[0] = bondAddr; return nil }); err != nil {
		t.Fatal(err)
	}
	c := checkByID(t, a.runDoctor(nil), "assigned-devices")
	if c.Status != doctorProblem || !strings.Contains(c.Detail, "session only") {
		t.Fatalf("assigned session-only pairing: %+v", c)
	}
}
