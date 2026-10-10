package oah

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reconcileApp builds an App whose commands go to rec, with one output and,
// optionally, inputs assigned and set to auto-connect.
func reconcileApp(t *testing.T, rec *recordedRunner, output string, inputs ...string) *App {
	t.Helper()
	cfg := defaultConfig()
	cfg.Slots.Outputs[0] = output
	cfg.DevicePrefs[output] = DevicePrefs{AutoConnect: true}
	for i, in := range inputs {
		cfg.Slots.Inputs[i] = in
		cfg.DevicePrefs[in] = DevicePrefs{AutoConnect: true}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := newConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return &App{cfg: store, run: runner{fake: rec.run}, deviceNames: map[string]string{}, deviceInfo: map[string]deviceInfoEntry{},
		connectState: map[string]*connectState{}, policed: map[string]time.Time{}, refresh: make(chan struct{}, 1), reconcileReq: make(chan string, 1)}
}

func (r *recordedRunner) count(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

const absentOutput = "70:F9:4A:86:51:5F"

// While pairing mode is on, the radio must stay free for the PC or phone being
// added: paging an absent output is what made Windows see the hub as not
// responding.
func TestReconcileDoesNotPageDuringPairingMode(t *testing.T) {
	rec := &recordedRunner{}
	a := reconcileApp(t, rec, absentOutput, "AA:BB:CC:DD:EE:01")
	a.pairing = PairingState{Active: true, Until: time.Now().Add(time.Minute)}
	a.reconcileRoutes("auto-connect supervisor")
	if n := rec.count("bluetoothctl connect"); n != 0 {
		t.Fatalf("%d connection attempts during pairing mode, calls: %v", n, rec.calls)
	}
	if n := rec.count("systemctl stop openaudiohub-bluealsa"); n != 0 {
		t.Fatal("pairing mode must not restage the inputs")
	}

	a.pairing = PairingState{}
	a.reconcileRoutes("auto-connect supervisor")
	if !rec.has("bluetoothctl connect "+absentOutput+" a2dp-sink") || !rec.has("bluetoothctl connect AA:BB:CC:DD:EE:01 a2dp-source") {
		t.Fatalf("outside pairing mode assigned devices are reconnected, calls: %v", rec.calls)
	}
}

func TestTimedOutConnectIsCancelled(t *testing.T) {
	rec := &recordedRunner{reply: func(call string) (string, error) {
		if strings.HasPrefix(call, "bluetoothctl connect") {
			return "", errors.New("bluetoothctl timed out")
		}
		return "", nil
	}}
	a := reconcileApp(t, rec, absentOutput)
	a.reconcileRoutes("auto-connect supervisor")
	if !rec.has("bluetoothctl disconnect " + absentOutput) {
		t.Fatalf("a timed-out Connect must be cancelled, calls: %v", rec.calls)
	}

	// A page timeout is a finished attempt; there is nothing to cancel.
	rec = &recordedRunner{reply: func(call string) (string, error) {
		if strings.HasPrefix(call, "bluetoothctl connect") {
			return "", errors.New("exit status 1: hci0 " + absentOutput + " type BR/EDR connect failed (status 0x04, Connect Failed)")
		}
		return "", nil
	}}
	a = reconcileApp(t, rec, absentOutput)
	a.reconcileRoutes("auto-connect supervisor")
	if rec.has("bluetoothctl disconnect " + absentOutput) {
		t.Fatal("a completed failure needs no cancel")
	}
	if a.connectFailureCount(absentOutput) != 1 {
		t.Fatal("the failure must count towards backoff")
	}
}

func TestReconcileDoesNotRestageWhileBackingOff(t *testing.T) {
	rec := &recordedRunner{}
	a := reconcileApp(t, rec, "", "AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02")
	now := time.Now()
	for _, in := range []string{"AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02"} {
		a.noteConnectAttempt(in, false, now)
		a.noteConnectAttempt(in, false, now)
	}
	a.reconcileRoutes("auto-connect supervisor")
	if rec.count("systemctl stop openaudiohub-bluealsa") != 0 || rec.count("bluetoothctl connect") != 0 {
		t.Fatalf("nothing is due, so BlueALSA must not be cycled, calls: %v", rec.calls)
	}
	// A role change still restages unconditionally.
	a.reconcileRoutes("input roles changed")
	if rec.count("systemctl stop openaudiohub-bluealsa") == 0 || rec.count("bluetoothctl connect") == 0 {
		t.Fatalf("a role change restages, calls: %v", rec.calls)
	}
}

func TestBluezErrorHint(t *testing.T) {
	cases := []struct {
		action, err, want string
	}{
		{"pair", "bluetoothctl pair 00:A6:55:12:4E:00: exit status 1: hci0 00:A6:55:12:4E:00 type BR/EDR connect failed (status 0x04, Connect Failed)\nFailed to pair: org.bluez.Error.ConnectionAttemptFailed", "add the hub from the PC"},
		{"connect", "Failed to connect: org.bluez.Error.InProgress br-connection-busy", "still busy"},
		{"connect", "org.bluez.Error.Failed br-connection-page-timeout", "did not answer"},
		{"pair", "Failed to pair: org.bluez.Error.AuthenticationRejected", "refused the pairing"},
		{"pair", "Failed to pair: org.bluez.Error.AlreadyExists", "already paired"},
		{"connect", "something new", ""},
	}
	for _, c := range cases {
		if got := bluezErrorHint(c.action, errors.New(c.err)); (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s %q: got %q, want it to contain %q", c.action, c.err, got, c.want)
		}
	}
}

func TestForgetClearsLocalTraces(t *testing.T) {
	write := withBluezStore(t)
	write(bondAddr, "[General]\nName=PC\n")
	cache := filepath.Join(bluezStorageRoot, "11:22:33:44:55:66", "cache", bondAddr)
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("[General]\nName=DESKTOP-4QO0QL9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	names := filepath.Join(t.TempDir(), "device-names.json")
	a := &App{deviceNames: map[string]string{bondAddr: "DESKTOP-4QO0QL9", "AA:BB:CC:DD:EE:02": "Speaker"}, deviceNamesPath: names,
		deviceInfo: map[string]deviceInfoEntry{bondAddr: {}}, connectState: map[string]*connectState{bondAddr: {fails: 3}}, policed: map[string]time.Time{bondAddr: time.Now()}}
	a.forgetLocally(bondAddr)
	if _, ok := a.deviceNames[bondAddr]; ok {
		t.Fatal("saved name kept")
	}
	if a.deviceNames["AA:BB:CC:DD:EE:02"] != "Speaker" {
		t.Fatal("other devices' names must stay")
	}
	if b, _ := os.ReadFile(names); strings.Contains(string(b), "DESKTOP") || !strings.Contains(string(b), "Speaker") {
		t.Fatalf("name file not rewritten: %s", b)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("BlueZ name cache entry kept")
	}
	if len(a.deviceInfo) != 0 || a.connectFailureCount(bondAddr) != 0 || a.recentlyPoliced(bondAddr, time.Now()) {
		t.Fatal("cached state kept")
	}
}

func TestDoctorChecksAdapterVisibilityInPairingMode(t *testing.T) {
	fake := healthyFake(t)
	a := doctorApp(t, fake)
	a.pairing = PairingState{Active: true, Until: time.Now().Add(time.Minute)}
	if err := os.MkdirAll(filepath.Dir(pairingFlagPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pairingFlagPath, []byte("1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The fake adapter reports neither Discoverable nor Pairable.
	rep := a.runDoctor(nil)
	c := checkByID(t, rep, "pairing-mode")
	if c.Status != doctorProblem || !strings.Contains(c.Detail, "cannot find") {
		t.Fatalf("pairing mode on but adapter hidden: %+v", c)
	}
	a.runDoctor(map[string]bool{"pairing-mode": true})
	if !fake.called("bluetoothctl discoverable on") || !fake.called("bluetoothctl pairable on") {
		t.Fatal("the fix must re-apply pairing mode on the adapter")
	}
}
