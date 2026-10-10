package oah

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHub scripts the commands the doctor runs. Units start in the states given
// and change when the doctor starts or restarts them, so a fix can be observed
// taking effect.
type fakeHub struct {
	mu        sync.Mutex
	units     map[string]string // unit -> is-active state
	enabled   map[string]bool
	userUnits string // systemctl --user is-active output
	powered   bool
	alias     string
	trusted   bool
	busctl    string
	calls     []string
	// stubborn units stay failed whatever the doctor does.
	stubborn map[string]bool
	// noRealtime: PipeWire runs without realtime priority until RTKit is
	// started and the audio graph restarted. procRoot is the fake /proc.
	noRealtime bool
	procRoot   string
}

// writePipeWireStat writes the fake /proc entry for PipeWire's audio thread.
func (f *fakeHub) writePipeWireStat() {
	fields := make([]string, 50) // fields 3..52
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[41-3] = "1" // SCHED_FIFO
	if f.noRealtime {
		fields[41-3] = "0"
	}
	dir := filepath.Join(f.procRoot, "1234", "task", "1240")
	_ = os.MkdirAll(dir, 0755)
	_ = os.WriteFile(filepath.Join(dir, "stat"), []byte("1240 (data-loop.0) "+strings.Join(fields, " ")+"\n"), 0644)
}

func (f *fakeHub) run(name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)
	switch name {
	case "systemctl":
		switch args[0] {
		case "is-active":
			st := f.units[args[1]]
			if st == "" {
				st = "inactive"
			}
			if st != "active" {
				return st + "\n", errors.New("exit status 3")
			}
			return "active\n", nil
		case "is-enabled":
			if f.enabled[args[1]] {
				return "enabled\n", nil
			}
			return "disabled\n", errors.New("exit status 1")
		case "start", "restart":
			if f.stubborn[args[1]] {
				return "", nil
			}
			f.units[args[1]] = "active"
			if args[1] == "openaudiohub-audio-tuning.service" && f.units["rtkit-daemon.service"] == "active" {
				f.noRealtime = false
			}
			return "", nil
		case "disable":
			for _, u := range args[2:] {
				f.enabled[u] = false
				f.units[u] = "inactive"
			}
			return "", nil
		}
		return "", nil
	case "bluetoothctl":
		if f.units["bluetooth.service"] != "active" {
			return "Waiting to connect to bluetoothd...", errors.New("exit status 1")
		}
		switch args[0] {
		case "show":
			p := "no"
			if f.powered {
				p = "yes"
			}
			return "Controller 11:22:33:44:55:66 (public)\n\tName: hub\n\tAlias: " + f.alias + "\n\tPowered: " + p + "\n", nil
		case "power":
			f.powered = true
		case "system-alias":
			f.alias = args[1]
		case "trust":
			f.trusted = true
		}
		return "", nil
	case "busctl":
		if f.units["bluetooth.service"] != "active" {
			return "", errors.New("unknown name org.bluez")
		}
		out := f.busctl
		if f.trusted {
			out = strings.ReplaceAll(out, `"Trusted" b false`, `"Trusted" b true`)
		}
		return out, nil
	case "runuser":
		return f.userUnits, nil
	case "pgrep":
		f.writePipeWireStat()
		return "1234\n", nil
	case "/usr/sbin/iw":
		return "Connected to 66:55:44:33:22:11 (on wlan0)\n\tSSID: Home-5G\n\tfreq: 5180\n\tsignal: -50 dBm\n", nil
	}
	return "", nil
}

func (f *fakeHub) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func healthyFake(t *testing.T) *fakeHub {
	b, err := os.ReadFile("testdata/busctl-managed-objects.txt")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeHub{
		units: map[string]string{
			"bluetooth.service": "active", "openaudiohub-bt-agent.service": "active",
			"openaudiohub-bluealsa.service": "active", "openaudiohub-bluealsa-bridge.service": "active",
		},
		enabled:   map[string]bool{},
		userUnits: "active\nactive\nactive\n",
		powered:   true,
		alias:     "OpenAudioHub",
		trusted:   true,
		busctl:    string(b),
		stubborn:  map[string]bool{},
	}
}

// doctorApp builds an App whose files live in a temporary directory and whose
// commands go to fake. It returns a function that restores the package paths.
func doctorApp(t *testing.T, fake *fakeHub) *App {
	t.Helper()
	dir := t.TempDir()
	oldConf, oldBin, oldRfkill, oldFlag, oldDisk, oldProc := bluetoothMainConf, bluealsaReceiverBin, rfkillRoot, pairingFlagPath, diskCheckPath, procRoot
	t.Cleanup(func() {
		bluetoothMainConf, bluealsaReceiverBin, rfkillRoot, pairingFlagPath, diskCheckPath, procRoot = oldConf, oldBin, oldRfkill, oldFlag, oldDisk, oldProc
	})
	procRoot = filepath.Join(dir, "proc")
	fake.procRoot = procRoot
	bluetoothMainConf = filepath.Join(dir, "main.conf")
	bluealsaReceiverBin = filepath.Join(dir, "bluealsa-receiver")
	rfkillRoot = filepath.Join(dir, "rfkill")
	pairingFlagPath = filepath.Join(dir, "run", "pairing-enabled")
	diskCheckPath = dir

	cfg := defaultConfig()
	cfg.BluetoothName = "OpenAudioHub"
	cfgPath := filepath.Join(dir, "config.json")
	if err := saveConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := newConfigStore(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{
		cfg: store, run: runner{fake: fake.run},
		deviceNames: map[string]string{}, deviceNamesPath: filepath.Join(dir, "device-names.json"),
		deviceInfo: map[string]deviceInfoEntry{}, connectState: map[string]*connectState{},
		policed: map[string]time.Time{}, refresh: make(chan struct{}, 1), reconcileReq: make(chan string, 1),
	}
	// A healthy installation: recommended main.conf and a current audio.json.
	conf := ""
	for _, r := range recommendedBluezConf(cfg) {
		conf = setConfKey(conf, r.section, r.key, r.val)
	}
	if err := os.WriteFile(bluetoothMainConf, []byte(conf), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.writeAudioProjection(store.Get().Audio); err != nil {
		t.Fatal(err)
	}
	return a
}

func checkByID(t *testing.T, rep DoctorReport, id string) DoctorCheck {
	t.Helper()
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in report", id)
	return DoctorCheck{}
}

func TestDoctorHealthyHub(t *testing.T) {
	a := doctorApp(t, healthyFake(t))
	rep := a.runDoctor(nil)
	for _, c := range rep.Checks {
		if c.ID == "disk-space" {
			continue // depends on the machine running the test
		}
		if c.Status != doctorOK {
			t.Errorf("%s: %s: %s", c.ID, c.Status, c.Detail)
		}
		if c.FixLabel != "" {
			t.Errorf("%s offers a fix on a healthy hub", c.ID)
		}
	}
}

func TestDoctorFindsAndFixesProblems(t *testing.T) {
	fake := healthyFake(t)
	fake.units["bluetooth.service"] = "failed"
	fake.units["openaudiohub-bt-agent.service"] = "inactive"
	fake.enabled["bluealsa.service"] = true
	fake.powered = false
	fake.alias = "orangepizero2w"
	a := doctorApp(t, fake)
	// Stale settings and leftovers from older releases.
	// main.conf does not exist yet, nor does its directory.
	bluetoothMainConf = filepath.Join(t.TempDir(), "bluetooth", "main.conf")
	if err := os.Remove(filepath.Join(filepath.Dir(a.cfg.path), "audio.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.deviceNamesPath, []byte(`{"AA:BB:CC:DD:EE:01":"RSSI: 0xffffffc4 (-60)","AA:BB:CC:DD:EE:02":"Kitchen Speaker"}`), 0644); err != nil {
		t.Fatal(err)
	}
	a.deviceNames = loadDeviceNameCache(a.deviceNamesPath)
	// The PC from the fixture is assigned but not trusted.
	fake.trusted = false
	if err := a.cfg.Update(func(c *Config) error { c.Slots.Inputs[0] = "AA:BB:CC:DD:EE:01"; return nil }); err != nil {
		t.Fatal(err)
	}
	// Pairing mode is off, but the helper's flag was left behind.
	if err := os.MkdirAll(filepath.Dir(pairingFlagPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pairingFlagPath, []byte("1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	before := a.runDoctor(nil)
	visible := map[string]string{
		"bluetooth-service": doctorProblem, "pairing-agent": doctorProblem, "bluez-config": doctorWarning,
		"pairing-mode": doctorProblem, "device-names": doctorWarning, "bluealsa-conflict": doctorProblem,
		"audio-projection": doctorProblem,
	}
	// With Bluetooth down these cannot be checked yet; "fix all" must still
	// reach them once its first round has started Bluetooth.
	hidden := []string{"bluetooth-adapter", "bluetooth-name", "assigned-devices"}
	for _, id := range hidden {
		if c := checkByID(t, before, id); c.Status != doctorSkipped {
			t.Errorf("before: %s = %s, want skipped while Bluetooth is down", id, c.Status)
		}
	}
	want := map[string]string{}
	for id, st := range visible {
		want[id] = st
	}
	for _, id := range hidden {
		want[id] = ""
	}
	for id, st := range visible {
		c := checkByID(t, before, id)
		if c.Status != st || c.FixLabel == "" {
			t.Errorf("before: %s = %s (fix %q), want %s with a fix: %s", id, c.Status, c.FixLabel, st, c.Detail)
		}
	}
	if fake.called("systemctl start") || fake.called("bluetoothctl trust") {
		t.Fatal("checking alone must not change anything")
	}

	after := a.runDoctor(map[string]bool{"*": true})
	for id := range want {
		c := checkByID(t, after, id)
		if c.Status != doctorOK || !c.Fixed {
			t.Errorf("after: %s = %s, fixed=%v, error %q: %s", id, c.Status, c.Fixed, c.FixError, c.Detail)
		}
	}
	if after.Fixed != len(want) {
		t.Errorf("fixed %d, want %d", after.Fixed, len(want))
	}
	if !fake.called("systemctl reset-failed bluetooth.service") {
		t.Error("a failed unit must be reset before it is started")
	}
	if fake.called("systemctl restart bluetooth.service") {
		t.Fatal("the doctor must never restart Bluetooth; that drops every audio link")
	}
	if n := checkByID(t, after, "bluez-config").FixNote; !strings.Contains(n, "reboot") {
		t.Errorf("main.conf fix should say when it applies, got %q", n)
	}
	conf, _ := os.ReadFile(bluetoothMainConf)
	if v := mainConfValues(string(conf)); v["General"]["FastConnectable"] != "true" || v["Policy"]["AutoEnable"] != "true" {
		t.Errorf("main.conf not updated:\n%s", conf)
	}
	if _, err := os.Stat(pairingFlagPath); !os.IsNotExist(err) {
		t.Error("stale pairing flag should be removed")
	}
	var names map[string]string
	b, _ := os.ReadFile(a.deviceNamesPath)
	if err := json.Unmarshal(b, &names); err != nil || len(names) != 1 || names["AA:BB:CC:DD:EE:02"] != "Kitchen Speaker" {
		t.Errorf("name cache after fix: %s", b)
	}
}

func TestDoctorReportsFixThatDoesNotHelp(t *testing.T) {
	fake := healthyFake(t)
	fake.units["openaudiohub-bt-agent.service"] = "failed"
	fake.stubborn["openaudiohub-bt-agent.service"] = true
	a := doctorApp(t, fake)
	rep := a.runDoctor(map[string]bool{"pairing-agent": true})
	c := checkByID(t, rep, "pairing-agent")
	if c.Fixed || c.FixError == "" || c.Status != doctorProblem {
		t.Fatalf("a fix that leaves the problem must say so: %+v", c)
	}
}

func TestDoctorFixesOnlySelectedChecks(t *testing.T) {
	fake := healthyFake(t)
	fake.units["openaudiohub-bt-agent.service"] = "inactive"
	fake.alias = "wrong"
	a := doctorApp(t, fake)
	rep := a.runDoctor(map[string]bool{"bluetooth-name": true})
	if fake.called("systemctl restart openaudiohub-bt-agent.service") {
		t.Fatal("a fix that was not selected ran")
	}
	if c := checkByID(t, rep, "pairing-agent"); c.Status != doctorProblem || c.Fixed {
		t.Fatalf("unselected check changed: %+v", c)
	}
	if c := checkByID(t, rep, "bluetooth-name"); !c.Fixed {
		t.Fatalf("selected fix did not apply: %+v", c)
	}
}

func TestDoctorStandaloneSkipsDaemonState(t *testing.T) {
	a := doctorApp(t, healthyFake(t))
	if err := os.MkdirAll(filepath.Dir(pairingFlagPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pairingFlagPath, []byte("1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The command line cannot know whether the daemon has pairing on, so it must
	// not call a live flag stale and offer to remove it.
	rep := a.RunDoctor(true)
	if c := checkByID(t, rep, "pairing-mode"); c.Status != doctorSkipped || c.Fixed {
		t.Fatalf("pairing-mode from the command line: %+v", c)
	}
	if _, err := os.Stat(pairingFlagPath); err != nil {
		t.Fatal("the command line removed the daemon's pairing flag")
	}
}

func TestDoctorConnectRetries(t *testing.T) {
	a := doctorApp(t, healthyFake(t))
	if err := a.cfg.Update(func(c *Config) error { c.Slots.Outputs[0] = "AA:BB:CC:DD:EE:03"; return nil }); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a.noteConnectAttempt("AA:BB:CC:DD:EE:03", false, now)
	a.noteConnectAttempt("AA:BB:CC:DD:EE:03", false, now)
	rep := a.runDoctor(map[string]bool{"connect-retries": true})
	if c := checkByID(t, rep, "connect-retries"); !c.Fixed {
		t.Fatalf("retry fix: %+v", c)
	}
	if a.connectFailureCount("AA:BB:CC:DD:EE:03") != 0 {
		t.Fatal("backoff should be cleared")
	}
	select {
	case <-a.reconcileReq:
	default:
		t.Fatal("a reconnect should be queued")
	}
}

func TestParseUnitStatesIgnoresErrors(t *testing.T) {
	got := parseUnitStates("System has not been booted with systemd as init system (PID 1). Can't operate.\n", 2)
	if got[0] != "unknown" || got[1] != "unknown" {
		t.Fatalf("got %v", got)
	}
	got = parseUnitStates("active\nfailed\n", 3)
	if got[0] != "active" || got[1] != "failed" || got[2] != "unknown" {
		t.Fatalf("got %v", got)
	}
}

func TestSetConfKeyIsSectionAware(t *testing.T) {
	in := "[General]\n#Name = BlueZ\n\n[Policy]\n#AutoEnable=true\n# AutoEnable is also mentioned here\n"
	out := setConfKey(in, "Policy", "AutoEnable", "true")
	out = setConfKey(out, "General", "FastConnectable", "true")
	out = setConfKey(out, "LE", "MinConnectionInterval", "7")
	want := "[General]\nFastConnectable = true\n#Name = BlueZ\n\n[Policy]\nAutoEnable = true\n# AutoEnable is also mentioned here\n\n[LE]\nMinConnectionInterval = 7\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if v := mainConfValues(out); v["Policy"]["AutoEnable"] != "true" || v["General"]["Name"] != "" {
		t.Fatalf("values %v", v)
	}
}

func TestDoctorFixEndpointNeedsSelection(t *testing.T) {
	a := doctorApp(t, healthyFake(t))
	rec := httptest.NewRecorder()
	a.handleDoctorFix(rec, httptest.NewRequest("POST", "/api/doctor/fix", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	a.handleDoctorFix(rec, httptest.NewRequest("POST", "/api/doctor/fix", strings.NewReader(`{"ids":["bluetooth-name"]}`)))
	var rep DoctorReport
	if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&rep) != nil || len(rep.Checks) == 0 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestDoctorFixAllRunsEachFixOnce(t *testing.T) {
	fake := healthyFake(t)
	fake.units["openaudiohub-bt-agent.service"] = "failed"
	fake.stubborn["openaudiohub-bt-agent.service"] = true
	a := doctorApp(t, fake)
	a.runDoctor(map[string]bool{"*": true})
	n := 0
	for _, c := range fake.calls {
		if c == "systemctl restart openaudiohub-bt-agent.service" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a fix that does not help ran %d times, want once", n)
	}
}

// Debian's main.conf has more sections after [General], and older installs or a
// rename leave an active value to replace, not add to.
func TestSetConfKeyReplacesActiveKeyBeforeLaterSections(t *testing.T) {
	in := "[General]\n#Name = BlueZ\nName = Old Hub\nClass = 0x000100\n\n[BR]\n#PageScanType=\n\n[Policy]\nAutoEnable=false\n"
	out := setConfKey(in, "General", "Name", "Hub")
	out = setConfKey(out, "General", "Class", "0x200414")
	out = setConfKey(out, "Policy", "AutoEnable", "true")
	out = setConfKey(out, "General", "FastConnectable", "true")
	want := "[General]\nFastConnectable = true\n#Name = BlueZ\nName = Hub\nClass = 0x200414\n\n[BR]\n#PageScanType=\n\n[Policy]\nAutoEnable = true\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if again := setConfKey(out, "General", "Name", "Hub"); again != out {
		t.Fatal("setting the same value twice must not add a line")
	}
}

func TestDoctorFixesStaleActiveBluezSettings(t *testing.T) {
	fake := healthyFake(t)
	a := doctorApp(t, fake)
	stale := "[General]\nName = orangepizero2w\nClass = 0x000100\n\n[BR]\n\n[LE]\n\n[Policy]\nAutoEnable=true\n"
	if err := os.WriteFile(bluetoothMainConf, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	rep := a.runDoctor(map[string]bool{"bluez-config": true})
	if c := checkByID(t, rep, "bluez-config"); !c.Fixed {
		t.Fatalf("stale active settings were not fixed: %+v", c)
	}
	b, _ := os.ReadFile(bluetoothMainConf)
	if strings.Count(string(b), "Name =") != 1 || strings.Count(string(b), "Class =") != 1 {
		t.Fatalf("duplicate keys written:\n%s", b)
	}
}

// Without realtime priority PipeWire's audio thread competes with everything
// else on the board. The doctor must notice and restart audio once RTKit runs.
func TestDoctorRestoresRealtimeAudio(t *testing.T) {
	fake := healthyFake(t)
	fake.noRealtime = true
	a := doctorApp(t, fake)
	c := checkByID(t, a.runDoctor(nil), "audio-realtime")
	if c.Status != doctorWarning || !strings.Contains(c.Detail, "RTKit") {
		t.Fatalf("before: %+v", c)
	}
	c = checkByID(t, a.runDoctor(map[string]bool{"audio-realtime": true}), "audio-realtime")
	if !c.Fixed || c.Status != doctorOK {
		t.Fatalf("after: %+v", c)
	}
	if !fake.called("systemctl start rtkit-daemon.service") {
		t.Fatal("the fix must start RTKit before restarting audio")
	}
}

func TestHasRealtimeThreadHandlesOddCommandNames(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "7", "task", "8")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	fields := strings.Repeat("0 ", 37) + "2 0 0" // fields 4..40, then 41
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte("8 (pw (data) loop) S "+fields+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !hasRealtimeThread(root, "7") {
		t.Fatal("SCHED_RR in field 41 is realtime")
	}
	if hasRealtimeThread(root, "9") {
		t.Fatal("a missing process has no realtime thread")
	}
}
