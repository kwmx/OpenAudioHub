package oah

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The doctor checks the parts of the appliance that most often go wrong and,
// where a fix is safe, applies it on request. No fix restarts bluetooth.service:
// that drops every audio link, so settings that need it are saved and the user is
// told they apply after the next reboot.

// DoctorCheck is one line of a doctor report.
type DoctorCheck struct {
	ID     string `json:"id"`
	Area   string `json:"area"`
	Title  string `json:"title"`
	Status string `json:"status"` // ok, warning, problem or skipped
	Detail string `json:"detail"`
	// FixLabel names the available fix; empty when there is nothing to apply.
	FixLabel string `json:"fixLabel,omitempty"`
	Fixed    bool   `json:"fixed,omitempty"`
	FixError string `json:"fixError,omitempty"`
	FixNote  string `json:"fixNote,omitempty"`
}

// DoctorReport is the result of one doctor run.
type DoctorReport struct {
	RanAt    time.Time     `json:"ranAt"`
	Checks   []DoctorCheck `json:"checks"`
	Problems int           `json:"problems"`
	Warnings int           `json:"warnings"`
	Fixable  int           `json:"fixable"`
	Fixed    int           `json:"fixed"`
}

const (
	doctorOK      = "ok"
	doctorWarning = "warning"
	doctorProblem = "problem"
	doctorSkipped = "skipped"
)

// finding is a check's verdict. fix, when set, returns an optional note for the
// user (for example that a change applies after a reboot).
type finding struct {
	status, detail, fixLabel string
	fix                      func() (string, error)
}

type doctorCheckDef struct {
	id, area, title string
	// daemonOnly checks read state that only the running daemon holds, such as
	// pairing mode or reconnect backoff, and are skipped by the command line.
	daemonOnly bool
	run        func(a *App) finding
}

// Paths the doctor inspects. Variables so tests can redirect them.
var (
	bluealsaReceiverBin = "/usr/local/lib/openaudiohub/bluealsa-4.3.1-oah"
	rfkillRoot          = "/sys/class/rfkill"
	pairingFlagPath     = "/run/openaudiohub/pairing-enabled"
	diskCheckPath       = "/"
)

// doctorFixRounds bounds how often "fix all" re-checks for newly exposed problems.
const doctorFixRounds = 3

// recommendedBluezConf is what the installer writes to main.conf.
func recommendedBluezConf(c Config) []struct{ section, key, val string } {
	return []struct{ section, key, val string }{
		{"General", "Name", c.BluetoothName},
		{"General", "Class", "0x200414"},
		{"General", "FastConnectable", "true"},
		{"General", "JustWorksRepairing", "confirm"},
		{"General", "TemporaryTimeout", "300"},
		{"Policy", "AutoEnable", "true"},
	}
}

func doctorChecks() []doctorCheckDef {
	return []doctorCheckDef{
		{id: "bluetooth-service", area: "bluetooth", title: "Bluetooth service", run: (*App).checkBluetoothService},
		{id: "bluetooth-adapter", area: "bluetooth", title: "Bluetooth adapter", run: (*App).checkBluetoothAdapter},
		{id: "bluetooth-name", area: "bluetooth", title: "Bluetooth name", run: (*App).checkBluetoothName},
		{id: "pairing-agent", area: "bluetooth", title: "Pairing helper", run: (*App).checkPairingAgent},
		{id: "bluez-config", area: "bluetooth", title: "Bluetooth settings", run: (*App).checkBluezConfig},
		{id: "device-list", area: "bluetooth", title: "Device list", run: (*App).checkDeviceList},
		{id: "pairing-mode", area: "bluetooth", title: "Pairing mode", daemonOnly: true, run: (*App).checkPairingMode},
		{id: "assigned-devices", area: "bluetooth", title: "Assigned devices", run: (*App).checkAssignedDevices},
		{id: "connect-retries", area: "bluetooth", title: "Automatic reconnects", daemonOnly: true, run: (*App).checkConnectRetries},
		{id: "device-names", area: "bluetooth", title: "Saved device names", run: (*App).checkDeviceNames},
		{id: "audio-session", area: "audio", title: "PipeWire audio", run: (*App).checkAudioSession},
		{id: "bluealsa-conflict", area: "audio", title: "Conflicting audio services", run: (*App).checkBlueALSAConflict},
		{id: "secondary-receiver", area: "audio", title: "Extra inputs (BlueALSA)", run: (*App).checkSecondaryReceiver},
		{id: "audio-projection", area: "audio", title: "Receiver settings file", run: (*App).checkAudioProjection},
		{id: "disk-space", area: "system", title: "Storage", run: (*App).checkDiskSpace},
		{id: "wifi-band", area: "network", title: "Wi-Fi", run: (*App).checkWiFiBand},
	}
}

// RunDoctor runs the doctor from the command line, where no daemon state exists.
// With fix it applies every available fix.
func (a *App) RunDoctor(fix bool) DoctorReport {
	a.standalone = true
	var ids map[string]bool
	if fix {
		ids = map[string]bool{"*": true}
	}
	return a.runDoctor(ids)
}

// runDoctor runs every check. fixIDs selects fixes to apply: nil applies none,
// and the key "*" applies all of them. After fixing, every check runs again, so
// the report shows the state the hub is actually in.
//
// Applying all fixes repeats for a few rounds, because one fix can expose the
// next: while Bluetooth is down the adapter and device checks cannot run, so
// their problems only appear once the first round has started it.
func (a *App) runDoctor(fixIDs map[string]bool) DoctorReport {
	a.doctorMu.Lock()
	defer a.doctorMu.Unlock()
	defs := doctorChecks()
	findings := a.evaluateChecks(defs)
	type outcome struct {
		err  error
		note string
	}
	attempted := map[string]outcome{}
	if len(fixIDs) > 0 {
		rounds := 1
		if fixIDs["*"] {
			rounds = doctorFixRounds
		}
		for round := 0; round < rounds; round++ {
			applied := 0
			for i, d := range defs {
				f := findings[i]
				if f.fix == nil || f.status == doctorOK || f.status == doctorSkipped {
					continue
				}
				if !fixIDs["*"] && !fixIDs[d.id] {
					continue
				}
				if _, done := attempted[d.id]; done {
					continue // each fix runs at most once per request
				}
				note, err := f.fix()
				if err != nil {
					a.logf("doctor: fix %s failed: %v", d.id, err)
				} else {
					a.logf("doctor: applied fix %s", d.id)
				}
				attempted[d.id] = outcome{err: err, note: note}
				applied++
			}
			if applied == 0 {
				break
			}
			findings = a.evaluateChecks(defs)
		}
		if len(attempted) > 0 {
			a.signalRefresh()
		}
	}
	rep := DoctorReport{RanAt: time.Now(), Checks: make([]DoctorCheck, 0, len(defs))}
	for i, d := range defs {
		f := findings[i]
		c := DoctorCheck{ID: d.id, Area: d.area, Title: d.title, Status: f.status, Detail: f.detail}
		if f.fix != nil && f.status != doctorOK && f.status != doctorSkipped {
			c.FixLabel = f.fixLabel
			rep.Fixable++
		}
		if o, ok := attempted[d.id]; ok {
			switch {
			case o.err != nil:
				c.FixError = "The fix did not complete. See the hub log for details."
			case f.status == doctorOK:
				c.Fixed = true
				c.FixNote = o.note
				rep.Fixed++
			default:
				c.FixError = "The fix ran, but the check still fails."
			}
		}
		switch f.status {
		case doctorProblem:
			rep.Problems++
		case doctorWarning:
			rep.Warnings++
		}
		rep.Checks = append(rep.Checks, c)
	}
	return rep
}

func (a *App) evaluateChecks(defs []doctorCheckDef) []finding {
	out := make([]finding, len(defs))
	for i, d := range defs {
		if d.daemonOnly && a.standalone {
			out[i] = finding{status: doctorSkipped, detail: "Checked by the running hub; open Diagnostics in the web interface."}
			continue
		}
		out[i] = a.safeCheck(d)
	}
	return out
}

// safeCheck keeps one broken check from taking down the whole report.
func (a *App) safeCheck(d doctorCheckDef) (f finding) {
	defer func() {
		if v := recover(); v != nil {
			a.logf("doctor: check %s panicked: %v", d.id, v)
			f = finding{status: doctorSkipped, detail: "This check could not run."}
		}
	}()
	return d.run(a)
}

// unitStates are the words systemctl is-active prints for a unit.
var unitStates = map[string]bool{"active": true, "inactive": true, "failed": true, "activating": true, "deactivating": true, "reloading": true, "maintenance": true, "refreshing": true}

// parseUnitStates reads one state per line from systemctl is-active output.
// Anything else (an error such as "System has not been booted with systemd")
// yields "unknown" rather than a word of the error text.
func parseUnitStates(out string, n int) []string {
	res := make([]string, n)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := range res {
		res[i] = "unknown"
		if i < len(lines) && unitStates[strings.TrimSpace(lines[i])] {
			res[i] = strings.TrimSpace(lines[i])
		}
	}
	return res
}

// unitState returns systemctl's state for a unit, or "unknown" when systemd
// cannot be asked. is-active exits non-zero for anything but active, so the
// output is read regardless of the error.
func (a *App) unitState(unit string) string {
	out, _ := a.run.Run(3*time.Second, "systemctl", "is-active", unit)
	return parseUnitStates(out, 1)[0]
}

const systemdUnavailable = "Could not ask systemd about this service."

// restartUnits clears a failed state, which otherwise blocks a start once the
// unit hit its start limit, and then starts or restarts each unit in order.
func (a *App) restartUnits(verb string, units ...string) error {
	for _, u := range units {
		_, _ = a.run.Run(5*time.Second, "systemctl", "reset-failed", u)
		if _, err := a.run.Run(15*time.Second, "systemctl", verb, u); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) checkBluetoothService() finding {
	st := a.unitState("bluetooth.service")
	switch st {
	case "active":
		return finding{status: doctorOK, detail: "Running."}
	case "unknown":
		return finding{status: doctorSkipped, detail: systemdUnavailable}
	}
	return finding{
		status:   doctorProblem,
		detail:   fmt.Sprintf("bluetooth.service is %s, so no device can connect.", st),
		fixLabel: "Start Bluetooth",
		fix: func() (string, error) {
			return "", a.restartUnits("start", "bluetooth.service")
		},
	}
}

// rfkillBluetooth reports whether any Bluetooth radio is soft or hard blocked.
func rfkillBluetooth(root string) (soft, hard bool) {
	entries, _ := filepath.Glob(filepath.Join(root, "rfkill*"))
	for _, e := range entries {
		t, _ := os.ReadFile(filepath.Join(e, "type"))
		if strings.TrimSpace(string(t)) != "bluetooth" {
			continue
		}
		if b, _ := os.ReadFile(filepath.Join(e, "soft")); strings.TrimSpace(string(b)) == "1" {
			soft = true
		}
		if b, _ := os.ReadFile(filepath.Join(e, "hard")); strings.TrimSpace(string(b)) == "1" {
			hard = true
		}
	}
	return soft, hard
}

// adapterShow parses `bluetoothctl show`. ok is false when there is no adapter.
func unblockBluetoothSysfs(root string) {
	entries, _ := filepath.Glob(filepath.Join(root, "rfkill*"))
	for _, e := range entries {
		if t, _ := os.ReadFile(filepath.Join(e, "type")); strings.TrimSpace(string(t)) == "bluetooth" {
			_ = os.WriteFile(filepath.Join(e, "soft"), []byte("0"), 0644)
		}
	}
}

func (a *App) adapterShow() (props map[string]string, ok bool) {
	out, err := a.run.Run(4*time.Second, "bluetoothctl", "show")
	if err != nil || strings.Contains(out, "No default controller") {
		return nil, false
	}
	props = map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "Controller ") {
			if f := strings.Fields(t); len(f) > 1 {
				props["Address"] = f[1]
			}
			continue
		}
		if k, v, found := cutKV(t); found {
			if _, seen := props[k]; !seen {
				props[k] = v
			}
		}
	}
	return props, props["Address"] != ""
}

func (a *App) checkBluetoothAdapter() finding {
	soft, hard := rfkillBluetooth(rfkillRoot)
	if hard {
		return finding{status: doctorProblem, detail: "The Bluetooth radio is disabled by a hardware switch or firmware (rfkill hard block). The hub cannot turn it on."}
	}
	powerOn := func() (string, error) {
		// Unblock through sysfs as well: the rfkill tool is not always installed.
		unblockBluetoothSysfs(rfkillRoot)
		_, _ = a.run.Run(4*time.Second, "rfkill", "unblock", "bluetooth")
		time.Sleep(500 * time.Millisecond)
		_, err := a.run.Run(6*time.Second, "bluetoothctl", "power", "on")
		return "", err
	}
	if soft {
		return finding{status: doctorProblem, detail: "The Bluetooth radio is switched off (rfkill soft block).", fixLabel: "Turn the radio on", fix: powerOn}
	}
	props, ok := a.adapterShow()
	if !ok {
		if a.unitState("bluetooth.service") != "active" {
			return finding{status: doctorSkipped, detail: "Waiting for the Bluetooth service."}
		}
		return finding{status: doctorProblem, detail: "Bluetooth is running but reports no adapter. Check that the board's Bluetooth firmware loaded (dmesg | grep -i blue)."}
	}
	if !parseBool(props["Powered"]) {
		return finding{status: doctorProblem, detail: "The adapter is powered off.", fixLabel: "Power on", fix: powerOn}
	}
	return finding{status: doctorOK, detail: "Powered on (" + props["Address"] + ")."}
}

func (a *App) checkBluetoothName() finding {
	want := strings.TrimSpace(a.cfg.Get().BluetoothName)
	props, ok := a.adapterShow()
	if !ok {
		return finding{status: doctorSkipped, detail: "No adapter to check."}
	}
	if props["Alias"] == want {
		return finding{status: doctorOK, detail: fmt.Sprintf("Other devices see %q.", want)}
	}
	return finding{
		status:   doctorWarning,
		detail:   fmt.Sprintf("Other devices see %q instead of %q.", props["Alias"], want),
		fixLabel: "Apply the hub name",
		fix:      func() (string, error) { return "", a.setBluetoothAlias(want) },
	}
}

func (a *App) checkPairingAgent() finding {
	st := a.unitState("openaudiohub-bt-agent.service")
	switch st {
	case "active":
		return finding{status: doctorOK, detail: "Running."}
	case "unknown":
		return finding{status: doctorSkipped, detail: systemdUnavailable}
	}
	return finding{
		status:   doctorProblem,
		detail:   fmt.Sprintf("The pairing helper is %s. PCs and phones cannot pair with the hub, and devices that are not trusted cannot reconnect.", st),
		fixLabel: "Restart the pairing helper",
		fix: func() (string, error) {
			return "", a.restartUnits("restart", "openaudiohub-bt-agent.service")
		},
	}
}

func (a *App) checkBluezConfig() finding {
	b, err := os.ReadFile(bluetoothMainConf)
	if err != nil && !os.IsNotExist(err) {
		return finding{status: doctorSkipped, detail: "Could not read " + bluetoothMainConf + "."}
	}
	have := mainConfValues(string(b))
	var diffs []string
	for _, r := range recommendedBluezConf(a.cfg.Get()) {
		if r.val == "" {
			continue
		}
		cur, ok := have[r.section][r.key]
		if ok && strings.EqualFold(cur, r.val) {
			continue
		}
		if !ok {
			cur = "not set"
		}
		diffs = append(diffs, fmt.Sprintf("%s is %s (recommended %s)", r.key, cur, r.val))
	}
	if len(diffs) == 0 {
		return finding{status: doctorOK, detail: "main.conf has the recommended settings."}
	}
	return finding{
		status:   doctorWarning,
		detail:   strings.Join(diffs, "; ") + ".",
		fixLabel: "Update main.conf",
		fix: func() (string, error) {
			cur, _ := os.ReadFile(bluetoothMainConf)
			s := string(cur)
			for _, r := range recommendedBluezConf(a.cfg.Get()) {
				if r.val != "" {
					s = setConfKey(s, r.section, r.key, r.val)
				}
			}
			if err := os.MkdirAll(filepath.Dir(bluetoothMainConf), 0755); err != nil {
				return "", err
			}
			if err := os.WriteFile(bluetoothMainConf, []byte(s), 0644); err != nil {
				return "", err
			}
			return "Saved. Bluetooth reads it at start, so it applies after the next reboot.", nil
		},
	}
}

func (a *App) checkDeviceList() finding {
	if a.unitState("bluetooth.service") != "active" {
		return finding{status: doctorSkipped, detail: "Waiting for the Bluetooth service."}
	}
	snap, err := a.bluezSnapshot()
	if err != nil {
		return finding{status: doctorWarning, detail: "Reading devices from BlueZ in one call failed, so the list uses the slower bluetoothctl method and shows fewer names: " + err.Error()}
	}
	return finding{status: doctorOK, detail: fmt.Sprintf("BlueZ reports %d devices.", len(snap))}
}

func (a *App) checkPairingMode() finding {
	_, err := os.Stat(pairingFlagPath)
	flag := err == nil
	active := a.pairingActive()
	switch {
	case flag && !active:
		return finding{
			status:   doctorProblem,
			detail:   "Pairing mode is off, but the pairing helper still accepts new devices.",
			fixLabel: "Turn pairing off",
			fix: func() (string, error) {
				// The user may have turned pairing on since the check ran.
				if a.pairingActive() {
					return "", nil
				}
				return "", a.setPairing(false)
			},
		}
	case active && !flag:
		return finding{
			status:   doctorProblem,
			detail:   "Pairing mode is on, but the pairing helper will refuse new devices.",
			fixLabel: "Repair pairing mode",
			fix: func() (string, error) {
				// Hold a.mu so the pairing timer cannot turn pairing off between
				// this check and the write, which would leave pairing open.
				a.mu.Lock()
				defer a.mu.Unlock()
				if !(a.pairing.Active && time.Now().Before(a.pairing.Until)) {
					return "", nil
				}
				if err := os.MkdirAll(filepath.Dir(pairingFlagPath), 0755); err != nil {
					return "", err
				}
				return "", os.WriteFile(pairingFlagPath, []byte("1\n"), 0644)
			},
		}
	}
	// The adapter's own state is what a PC or phone sees. Pairing mode can be on
	// in the hub while the adapter is not discoverable (for example after
	// bluetoothd restarted), and Windows then reports the hub as not responding.
	if props, ok := a.adapterShow(); ok {
		visible := parseBool(props["Discoverable"]) && parseBool(props["Pairable"])
		switch {
		case active && !visible:
			return finding{
				status:   doctorProblem,
				detail:   fmt.Sprintf("Pairing mode is on, but the adapter reports Discoverable: %s and Pairable: %s, so PCs and phones cannot find or pair with the hub.", props["Discoverable"], props["Pairable"]),
				fixLabel: "Re-apply pairing mode",
				fix:      func() (string, error) { return "", a.setPairing(true) },
			}
		case !active && parseBool(props["Discoverable"]):
			return finding{
				status:   doctorWarning,
				detail:   "Pairing mode is off, but the adapter is still discoverable.",
				fixLabel: "Turn pairing off",
				fix: func() (string, error) {
					if a.pairingActive() {
						return "", nil
					}
					return "", a.setPairing(false)
				},
			}
		}
	}
	if active {
		return finding{status: doctorOK, detail: "On; the hub is discoverable and new devices can pair."}
	}
	return finding{status: doctorOK, detail: "Off; only known devices can connect."}
}

type assignedSlot struct{ label, addr string }

func (a *App) assignedSlots() []assignedSlot {
	c := a.cfg.Get()
	var out []assignedSlot
	for i, x := range c.Slots.Inputs {
		if x != "" {
			out = append(out, assignedSlot{fmt.Sprintf("Input %d", i+1), strings.ToUpper(x)})
		}
	}
	for i, x := range c.Slots.Outputs {
		if x != "" {
			out = append(out, assignedSlot{fmt.Sprintf("Output %d", i+1), strings.ToUpper(x)})
		}
	}
	return out
}

func (a *App) checkAssignedDevices() finding {
	slots := a.assignedSlots()
	if len(slots) == 0 {
		return finding{status: doctorOK, detail: "No devices are assigned yet."}
	}
	snap, err := a.bluezSnapshot()
	if err != nil {
		return finding{status: doctorSkipped, detail: "Bluetooth is not answering."}
	}
	var missing, sessionOnly, untrustedNames []string
	var untrusted []string
	for _, s := range slots {
		bd, ok := snap[s.addr]
		name := firstUsableBluetoothName(s.addr, bd.Alias, bd.Name, a.rememberedDeviceName(s.addr))
		if name == "" {
			name = s.addr
		}
		switch {
		case !ok || !(bd.Paired || bd.Bonded):
			missing = append(missing, fmt.Sprintf("%s (%s)", s.label, name))
		case sessionOnlyReason(bd) != "":
			sessionOnly = append(sessionOnly, fmt.Sprintf("%s (%s)", s.label, name))
		case !bd.Trusted:
			untrusted = append(untrusted, s.addr)
			untrustedNames = append(untrustedNames, name)
		}
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, strings.Join(missing, ", ")+" is no longer paired. Pair it again, or unassign it.")
	}
	if len(sessionOnly) > 0 {
		parts = append(parts, strings.Join(sessionOnly, ", ")+" is paired for this session only: no key was stored, so it is forgotten at the next disconnect. Forget it on the Devices page, then pair it again.")
	}
	if len(untrusted) > 0 {
		parts = append(parts, strings.Join(untrustedNames, ", ")+" is not trusted, so its reconnects depend on the pairing helper.")
	}
	if len(parts) == 0 {
		return finding{status: doctorOK, detail: fmt.Sprintf("%d assigned, all paired and trusted.", len(slots))}
	}
	f := finding{status: doctorWarning, detail: strings.Join(parts, " ")}
	if len(missing) > 0 || len(sessionOnly) > 0 {
		f.status = doctorProblem
	}
	if len(untrusted) > 0 {
		f.fixLabel = "Trust them"
		f.fix = func() (string, error) {
			for _, addr := range untrusted {
				if _, err := a.run.Run(5*time.Second, "bluetoothctl", "trust", addr); err != nil {
					return "", err
				}
			}
			return "", nil
		}
	}
	return f
}

func (a *App) checkConnectRetries() finding {
	var failing []string
	var addrs []string
	for _, s := range a.assignedSlots() {
		if n := a.connectFailureCount(s.addr); n >= 2 {
			name := firstUsableBluetoothName(s.addr, a.rememberedDeviceName(s.addr))
			if name == "" {
				name = s.addr
			}
			failing = append(failing, fmt.Sprintf("%s (%s, %d failed tries)", s.label, name, n))
			addrs = append(addrs, s.addr)
		}
	}
	if len(failing) == 0 {
		return finding{status: doctorOK, detail: "No assigned device is failing to reconnect."}
	}
	return finding{
		status:   doctorWarning,
		detail:   strings.Join(failing, ", ") + ". The hub now waits up to 5 minutes between tries. Check the device is on and in range.",
		fixLabel: "Retry now",
		fix: func() (string, error) {
			for _, addr := range addrs {
				a.forgetConnectState(addr)
			}
			a.requestReconcile("doctor: retry connections")
			return "Reconnecting in the background.", nil
		},
	}
}

func (a *App) checkDeviceNames() finding {
	if a.deviceNamesPath == "" {
		return finding{status: doctorSkipped, detail: "No name cache."}
	}
	raw := map[string]string{}
	b, err := os.ReadFile(a.deviceNamesPath)
	if err != nil {
		return finding{status: doctorOK, detail: "No saved names yet."}
	}
	if json.Unmarshal(b, &raw) != nil {
		return finding{status: doctorWarning, detail: "The saved name file is unreadable.", fixLabel: "Rebuild it", fix: a.rewriteDeviceNameCache}
	}
	var bad []string
	for addr, name := range raw {
		if cleanAddr(addr) == "" || !usableBluetoothName(name, addr) {
			bad = append(bad, name)
		}
	}
	if len(bad) == 0 {
		return finding{status: doctorOK, detail: plural(len(raw), "saved name", "saved names") + "."}
	}
	sort.Strings(bad)
	return finding{
		status:   doctorWarning,
		detail:   fmt.Sprintf("%s not real, for example %q. Older releases saved scan output as names.", plural(len(bad), "saved name is", "saved names are"), bad[0]),
		fixLabel: "Remove them",
		fix:      a.rewriteDeviceNameCache,
	}
}

// rewriteDeviceNameCache writes the in-memory cache, which only ever holds
// usable names, back over the file.
func (a *App) rewriteDeviceNameCache() (string, error) {
	a.deviceNamesMu.Lock()
	clean := map[string]string{}
	for addr, name := range a.deviceNames {
		if usableBluetoothName(name, addr) {
			clean[addr] = name
		}
	}
	a.deviceNames = clean
	path := a.deviceNamesPath
	a.deviceNamesMu.Unlock()
	return "", writeDeviceNameFile(path, clean)
}

func (a *App) checkAudioSession() finding {
	out, _ := a.audioUserCommand(4*time.Second, "systemctl", "--user", "is-active", "pipewire.service", "pipewire-pulse.service", "wireplumber.service")
	names := []string{"pipewire", "pipewire-pulse", "wireplumber"}
	states := parseUnitStates(out, len(names))
	var down []string
	for i, n := range names {
		if st := states[i]; st == "unknown" {
			down = append(down, n+" is not answering")
		} else if st != "active" {
			down = append(down, n+" is "+st)
		}
	}
	if len(down) == 0 {
		return finding{status: doctorOK, detail: "PipeWire, its Pulse server and WirePlumber are running."}
	}
	return finding{
		status:   doctorProblem,
		detail:   strings.Join(down, ", ") + ". Nothing can be mixed or played until they run.",
		fixLabel: "Start audio services",
		fix: func() (string, error) {
			// Bypass the self-heal rate limit: this is an explicit request.
			a.audioHealMu.Lock()
			a.audioLastHeal = time.Time{}
			a.audioHealMu.Unlock()
			if err := a.ensureAudioSession(); err != nil {
				return "", err
			}
			a.requestReconcile("doctor: audio services started")
			return "", nil
		},
	}
}

func (a *App) checkBlueALSAConflict() finding {
	units := []string{"bluealsa.service", "bluealsa-aplay.service"}
	var running []string
	for _, u := range units {
		st := a.unitState(u)
		if st == "unknown" {
			return finding{status: doctorSkipped, detail: systemdUnavailable}
		}
		active := st == "active"
		out, _ := a.run.Run(3*time.Second, "systemctl", "is-enabled", u)
		enabled := strings.TrimSpace(out) == "enabled"
		if active || enabled {
			running = append(running, u)
		}
	}
	if len(running) == 0 {
		return finding{status: doctorOK, detail: "Debian's own BlueALSA services are off."}
	}
	return finding{
		status:   doctorProblem,
		detail:   strings.Join(running, " and ") + " from Debian is running or starts at boot. It takes the Bluetooth endpoints OpenAudioHub manages.",
		fixLabel: "Disable them",
		fix: func() (string, error) {
			args := append([]string{"disable", "--now"}, running...)
			_, err := a.run.Run(15*time.Second, "systemctl", args...)
			if err == nil {
				a.requestReconcile("doctor: conflicting BlueALSA disabled")
			}
			return "", err
		},
	}
}

func (a *App) checkSecondaryReceiver() finding {
	inputs := 0
	for _, x := range a.cfg.Get().Slots.Inputs {
		if x != "" {
			inputs++
		}
	}
	if inputs < 2 {
		return finding{status: doctorOK, detail: "Not needed while fewer than two inputs are assigned."}
	}
	if !fileExists(bluealsaReceiverBin) {
		return finding{status: doctorProblem, detail: "The BlueALSA receiver for inputs 2 and up is not built. Run: sudo scripts/build-bluealsa.sh"}
	}
	units := []string{"openaudiohub-bluealsa.service", "openaudiohub-bluealsa-bridge.service"}
	var down []string
	for _, u := range units {
		switch st := a.unitState(u); st {
		case "active":
		case "unknown":
			return finding{status: doctorSkipped, detail: systemdUnavailable}
		default:
			down = append(down, strings.TrimSuffix(u, ".service")+" is "+st)
		}
	}
	if len(down) == 0 {
		return finding{status: doctorOK, detail: "Receiver and player are running."}
	}
	return finding{
		status:   doctorProblem,
		detail:   strings.Join(down, ", ") + ". Inputs 2 and up cannot connect or play.",
		fixLabel: "Start the receiver",
		fix: func() (string, error) {
			if err := a.restartUnits("start", units...); err != nil {
				return "", err
			}
			a.requestReconcile("doctor: receiver started")
			return "", nil
		},
	}
}

func (a *App) checkAudioProjection() finding {
	want := a.cfg.Get().Audio
	path := filepath.Join(filepath.Dir(a.cfg.path), "audio.json")
	rewrite := func() (string, error) {
		a.audioConfigMu.Lock()
		defer a.audioConfigMu.Unlock()
		if err := a.writeAudioProjection(a.cfg.Get().Audio); err != nil {
			return "", err
		}
		return "Reconnect the source on Input 2 to use these settings.", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return finding{status: doctorProblem, detail: "audio.json is missing, so the receiver falls back to built-in defaults.", fixLabel: "Rewrite it", fix: rewrite}
	}
	var have AudioConfig
	if json.Unmarshal(b, &have) != nil {
		return finding{status: doctorProblem, detail: "audio.json is unreadable, so the receiver cannot start.", fixLabel: "Rewrite it", fix: rewrite}
	}
	if mustJSON(have) != mustJSON(want) {
		return finding{status: doctorWarning, detail: "audio.json does not match the saved audio settings.", fixLabel: "Rewrite it", fix: rewrite}
	}
	return finding{status: doctorOK, detail: "Matches the saved audio settings."}
}

func (a *App) checkDiskSpace() finding {
	var st syscall.Statfs_t
	if err := syscall.Statfs(diskCheckPath, &st); err != nil {
		return finding{status: doctorSkipped, detail: "Could not read free space."}
	}
	total := float64(st.Blocks) * float64(st.Bsize)
	free := float64(st.Bavail) * float64(st.Bsize)
	if total <= 0 {
		return finding{status: doctorSkipped, detail: "Could not read free space."}
	}
	pct := free / total * 100
	detail := fmt.Sprintf("%.0f MB free (%.0f%%).", free/1e6, pct)
	status := doctorOK
	switch {
	case free < 100e6 || pct < 3:
		status = doctorProblem
		detail += " Settings and logs may fail to save."
	case free < 300e6 || pct < 8:
		status = doctorWarning
	}
	if status == doctorOK {
		return finding{status: status, detail: detail}
	}
	return finding{
		status:   status,
		detail:   detail,
		fixLabel: "Shrink system logs",
		fix: func() (string, error) {
			_, err := a.run.Run(30*time.Second, "journalctl", "--vacuum-size=50M")
			return "", err
		},
	}
}

func (a *App) checkWiFiBand() finding {
	w := a.wifiState()
	switch {
	case w.SSID == "" && wiredConnection(w.Interface):
		return finding{status: doctorOK, detail: "Using a wired connection; Wi-Fi is not connected."}
	case w.SSID == "":
		return finding{status: doctorWarning, detail: "Not connected to Wi-Fi on " + w.Interface + ", and no wired connection was found."}
	case w.Band == "2.4 GHz":
		return finding{status: doctorWarning, detail: fmt.Sprintf("Connected to %q on 2.4 GHz, which shares the radio with Bluetooth audio. Join a 5 GHz network from the Network page if you hear dropouts.", w.SSID)}
	}
	return finding{status: doctorOK, detail: fmt.Sprintf("Connected to %q on %s.", w.SSID, w.Band)}
}

// wiredConnection reports whether an interface other than loopback and the
// Wi-Fi interface is up with an IPv4 address.
func wiredConnection(wifi string) bool {
	ifs, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Name == wifi {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, ad := range addrs {
			if ip, _, err := net.ParseCIDR(ad.String()); err == nil && ip.To4() != nil {
				return true
			}
		}
	}
	return false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
