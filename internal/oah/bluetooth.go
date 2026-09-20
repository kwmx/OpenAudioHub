package oah

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var macRE = regexp.MustCompile(`(?i)[0-9A-F]{2}(?::[0-9A-F]{2}){5}`)

func parseBluetoothDeviceLines(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		loc := macRE.FindStringIndex(line)
		if loc == nil {
			continue
		}
		addr := strings.ToUpper(line[loc[0]:loc[1]])
		m[addr] = strings.TrimSpace(line[loc[1]:])
	}
	return m
}

func (a *App) bluetoothDeviceSubset(kind string) map[string]bool {
	out, err := a.run.Run(3*time.Second, "bluetoothctl", "devices", kind)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for addr := range parseBluetoothDeviceLines(out) {
		set[addr] = true
	}
	return set
}

func (a *App) listBluetoothDevices(transports []Transport) ([]Device, error) {
	// `bluetoothctl devices` can contain dozens of nearby transient devices.
	// Calling `bluetoothctl info` for every one every few seconds overloaded BlueZ
	// and the small board. Only paired/connected/assigned devices need detailed
	// Device1 inspection; nearby devices can use their discovery name until paired.
	out, err := a.run.Run(4*time.Second, "bluetoothctl", "devices")
	if err != nil {
		return make([]Device, 0), err
	}
	listed := parseBluetoothDeviceLines(out)
	paired := a.bluetoothDeviceSubset("Paired")
	connected := a.bluetoothDeviceSubset("Connected")
	fallbackDetailed := paired == nil || connected == nil
	if paired == nil {
		paired = map[string]bool{}
	}
	if connected == nil {
		connected = map[string]bool{}
	}

	cfg := a.cfg.Get()
	assigned := map[string]bool{}
	for _, addr := range append(append([]string{}, cfg.Slots.Inputs...), cfg.Slots.Outputs...) {
		if addr != "" {
			assigned[strings.ToUpper(addr)] = true
		}
	}

	devices := make([]Device, 0, len(listed)+len(assigned))
	seen := map[string]bool{}
	// Nearby devices used to be described as "unknown" because inspecting every
	// discovered device on every state build overloaded BlueZ. BlueZ does persist
	// Icon and UUIDs for discovered devices, so a bounded, cached lookup gives the
	// UI a real device type without that cost.
	budget := nearbyInfoBudget
	for addr, listedName := range listed {
		seen[addr] = true
		if usableBluetoothName(listedName, addr) {
			a.rememberDeviceName(addr, listedName)
		}
		if fallbackDetailed || paired[addr] || connected[addr] || assigned[addr] {
			devices = append(devices, a.bluetoothInfo(addr, listedName))
			continue
		}
		name := firstUsableBluetoothName(addr, listedName, a.rememberedDeviceName(addr))
		if name == "" {
			name = "Nearby device · " + tailAddr(addr)
		}
		if d, ok := a.cachedDeviceInfo(addr, nearbyInfoTTL); ok {
			d.Name = name
			devices = append(devices, d)
			continue
		}
		if budget > 0 {
			budget--
			d := a.bluetoothInfo(addr, listedName)
			a.rememberDeviceInfo(addr, d)
			devices = append(devices, d)
			continue
		}
		// Over budget for this pass: describe what discovery already told us and
		// refine on a later build rather than blocking the state on BlueZ.
		devices = append(devices, Device{
			ID: strings.ReplaceAll(addr, ":", "_"), Addr: addr, Name: name,
			Kind: "unknown", Caps: []string{}, Status: "disconnected",
		})
	}
	// Assigned devices must remain represented during transient BlueZ resets.
	for addr := range assigned {
		if seen[addr] {
			continue
		}
		devices = append(devices, a.bluetoothInfo(addr, ""))
	}

	inputTransport := map[string]bool{}
	outputTransport := map[string]bool{}
	for _, t := range transports {
		addr := strings.ToUpper(t.Addr)
		if strings.Contains(t.UUID, "Audio Sink") {
			inputTransport[addr] = true
		}
		if strings.Contains(t.UUID, "Audio Source") {
			outputTransport[addr] = true
		}
	}
	for i := range devices {
		for _, t := range transports {
			if strings.EqualFold(t.Addr, devices[i].Addr) {
				devices[i].Codec = t.Codec
				devices[i].Rate = t.Rate
				devices[i].SBCMaxBitpool = t.SBCMaxBitpool
				// Only a transport that actually exposes Delay may set a latency;
				// an absent property must not read as a confirmed 0 ms.
				if t.DelayKnown {
					devices[i].LatencyMS = delayUnitsToMS(t.Delay)
				}
				if t.VolumeKnown {
					devices[i].Volume = int(float64(t.Volume)/127*100 + 0.5)
					devices[i].VolumeKnown = true
				}
			}
		}
		devices[i].Role = a.roleFor(devices[i].Addr)
		devices[i].AutoConnect = a.autoConnectFor(devices[i].Addr)

		// For assigned audio roles, report the A2DP profile state rather than the
		// generic ACL flag. A machine can be Bluetooth-connected while its A2DP
		// profile was refused/busy; calling that state "Connected" made failures
		// look healthy and prevented automatic repair.
		aclConnected := devices[i].Connected
		addr := strings.ToUpper(devices[i].Addr)
		switch {
		case strings.HasPrefix(devices[i].Role, "in"):
			devices[i].Connected = inputTransport[addr]
		case strings.HasPrefix(devices[i].Role, "out"):
			devices[i].Connected = outputTransport[addr]
		}
		// Only one Bluetooth output can hold the engine's single A2DP Source
		// endpoint. Say so plainly for the other assigned output rather than
		// showing "Connecting..." forever.
		if strings.HasPrefix(devices[i].Role, "out") && !devices[i].Connected && !a.isActiveOutput(devices[i].Addr) {
			devices[i].Reason = "Standby. Only one Bluetooth output can play at a time — switch to this output to use it."
			devices[i].Status = "standby"
		}
		if devices[i].Connected {
			devices[i].Status = "connected"
		} else if devices[i].Status == "standby" {
			// keep the explicit reason
		} else if aclConnected && devices[i].Role != "" {
			devices[i].Status = "connecting"
		} else if devices[i].Role != "" {
			// Repeated failures need to look different from "not tried yet", otherwise
			// the UI shows "Connecting…" indefinitely and offers no retry.
			if a.connectFailureCount(strings.ToUpper(devices[i].Addr)) >= 2 {
				devices[i].Status = "error"
			} else {
				devices[i].Status = "disconnected"
			}
		}
	}
	bluealsaStates := a.bluealsaPCMStates()
	for i := range devices {
		if !devices[i].Connected {
			continue
		}
		addr := strings.ToUpper(devices[i].Addr)
		if strings.HasPrefix(devices[i].Role, "out") {
			devices[i].Backend = "pipewire"
		} else if pcm, ok := bluealsaStates[addr]; ok {
			devices[i].Backend = "bluealsa"
			if pcm.VolumeKnown {
				devices[i].Volume = int(float64(pcm.Volume)/127*100 + 0.5)
				devices[i].VolumeKnown = true
			}
		} else if contains(devices[i].Caps, "sends_audio") {
			devices[i].Backend = "pipewire"
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		ri, rj := roleRank(devices[i].Role), roleRank(devices[j].Role)
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(devices[i].Name) < strings.ToLower(devices[j].Name)
	})
	return devices, nil
}

func roleRank(s string) int {
	switch s {
	case "in1":
		return 0
	case "in2":
		return 1
	case "out1":
		return 2
	default:
		return 9
	}
}

func (a *App) bluetoothInfo(addr, listedName string) Device {
	d := Device{ID: strings.ReplaceAll(addr, ":", "_"), Addr: strings.ToUpper(addr), Name: "Unnamed device · " + tailAddr(addr), Status: "disconnected", Caps: make([]string, 0)}
	out, _ := a.run.Run(4*time.Second, "bluetoothctl", "info", addr)
	var deviceName, alias string
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		if k, v, ok := cutKV(s); ok {
			switch k {
			case "Name":
				deviceName = v
			case "Alias":
				alias = v
			case "Icon":
				d.Icon = v
			case "Paired":
				d.Paired = parseBool(v)
			case "Trusted":
				d.Trusted = parseBool(v)
			case "Connected":
				d.Connected = parseBool(v)
			case "RSSI":
				if fs := strings.Fields(v); len(fs) > 0 {
					d.RSSI = atoiLoose(fs[0])
				}
			case "UUID":
				if strings.Contains(v, "Audio Source") {
					d.Caps = appendUnique(d.Caps, "sends_audio")
				}
				if strings.Contains(v, "Audio Sink") {
					d.Caps = appendUnique(d.Caps, "plays_audio")
				}
			}
		}
	}
	// Name resolution intentionally uses several sources. BlueZ Alias may become
	// a formatted MAC after pairing on some devices, so never let a MAC-like Alias
	// overwrite a real discovery name. The persisted OpenAudioHub cache keeps the
	// last useful name across disconnects, pairing changes and reboots.
	stored := bluezStoredNames(addr)
	candidates := []string{alias, deviceName, listedName}
	candidates = append(candidates, stored...)
	candidates = append(candidates, a.rememberedDeviceName(addr))
	if best := firstUsableBluetoothName(addr, candidates...); best != "" {
		d.Name = best
		a.rememberDeviceName(addr, best)
	}
	if d.Connected {
		d.Status = "connected"
	}
	switch {
	case contains(d.Caps, "sends_audio") && contains(d.Caps, "plays_audio"):
		d.Kind = "audio-bidirectional"
	case contains(d.Caps, "sends_audio"):
		d.Kind = "source"
	case contains(d.Caps, "plays_audio"):
		d.Kind = "output"
	default:
		d.Kind = "unknown"
	}
	return d
}

func cutKV(s string) (string, string, bool) {
	i := strings.Index(s, ":")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
}
func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}
func appendUnique(a []string, s string) []string {
	if contains(a, s) {
		return a
	}
	return append(a, s)
}
func tailAddr(addr string) string {
	p := strings.Split(addr, ":")
	if len(p) >= 2 {
		return strings.Join(p[len(p)-2:], ":")
	}
	return addr
}

// activeOutputHolder returns the assigned output that currently holds the engine's
// single A2DP source transport, ignoring except. Connecting another output means
// taking the endpoint from this one.
// isActiveOutput reports whether addr is the selected output slot.
func (a *App) isActiveOutput(addr string) bool {
	c := a.cfg.Get()
	addr = strings.ToUpper(cleanAddr(addr))
	if addr == "" || len(c.Slots.Outputs) == 0 {
		return false
	}
	idx := c.Slots.ActiveOutput
	if idx < 0 || idx >= len(c.Slots.Outputs) {
		idx = 0
	}
	return strings.EqualFold(c.Slots.Outputs[idx], addr)
}

func (a *App) activeOutputHolder(except string) string {
	except = strings.ToUpper(cleanAddr(except))
	assigned := map[string]bool{}
	for _, raw := range a.cfg.Get().Slots.Outputs {
		if raw != "" {
			assigned[strings.ToUpper(raw)] = true
		}
	}
	for _, t := range a.listTransports() {
		addr := strings.ToUpper(t.Addr)
		if strings.Contains(t.UUID, "Audio Source") && addr != except && assigned[addr] {
			return addr
		}
	}
	return ""
}

func (a *App) roleFor(addr string) string {
	cfg := a.cfg.Get()
	addr = strings.ToUpper(addr)
	for i, x := range cfg.Slots.Inputs {
		if strings.EqualFold(x, addr) {
			return "in" + strconv.Itoa(i+1)
		}
	}
	for i, x := range cfg.Slots.Outputs {
		if strings.EqualFold(x, addr) {
			return "out" + strconv.Itoa(i+1)
		}
	}
	return ""
}

func (a *App) listTransports() []Transport {
	out, err := a.run.Run(3*time.Second, "bluetoothctl", "transport.list")
	if err != nil {
		return make([]Transport, 0)
	}
	ts := make([]Transport, 0)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Transport ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "Transport "))
		show, _ := a.run.Run(3*time.Second, "bluetoothctl", "transport.show", path)
		t := Transport{Path: path, Addr: addrFromBluezPath(path)}
		for _, l := range strings.Split(show, "\n") {
			k, v, ok := cutKV(strings.TrimSpace(l))
			if !ok {
				continue
			}
			switch k {
			case "UUID":
				t.UUID = v
			case "Media Codec":
				t.Codec = v
			case "Frequencies":
				if fs := strings.Fields(v); len(fs) > 0 {
					t.Rate = parseRate(fs[0])
				}
			case "Bitpool Range":
				parts := strings.Split(v, "-")
				if len(parts) == 2 {
					t.SBCMaxBitpool = atoiLoose(parts[1])
				}
			case "State":
				t.State = v
			case "Volume":
				if fs := strings.Fields(v); len(fs) > 0 {
					n, _ := strconv.ParseInt(strings.TrimPrefix(fs[0], "0x"), 16, 64)
					t.Volume = int(n)
					t.VolumeKnown = true
				}
			case "Delay":
				if fs := strings.Fields(v); len(fs) > 0 {
					n, _ := strconv.ParseInt(strings.TrimPrefix(fs[0], "0x"), 16, 64)
					t.Delay = int(n)
					t.DelayKnown = true
				}
			}
		}
		ts = append(ts, t)
	}
	return ts
}
func addrFromBluezPath(p string) string {
	r := regexp.MustCompile(`dev_([0-9A-Fa-f_]{17})`)
	m := r.FindStringSubmatch(p)
	if len(m) != 2 {
		return ""
	}
	return strings.ToUpper(strings.ReplaceAll(m[1], "_", ":"))
}
func parseRate(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "khz")
	f, _ := strconv.ParseFloat(s, 64)
	return int(f*1000 + 0.5)
}

func (a *App) autoConnectFor(addr string) bool {
	addr = strings.ToUpper(cleanAddr(addr))
	if addr == "" {
		return false
	}
	c := a.cfg.Get()
	p, ok := c.DevicePrefs[addr]
	return ok && p.AutoConnect
}

func (a *App) setAutoConnect(addr string, enabled bool) error {
	addr = cleanAddr(addr)
	if addr == "" {
		return fmtErr("invalid Bluetooth address")
	}
	return a.cfg.Update(func(c *Config) error {
		if c.DevicePrefs == nil {
			c.DevicePrefs = map[string]DevicePrefs{}
		}
		p := c.DevicePrefs[addr]
		p.AutoConnect = enabled
		c.DevicePrefs[addr] = p
		return nil
	})
}

type bluealsaPCMState struct {
	Path        string
	Volume      int
	VolumeKnown bool
}

func (a *App) bluealsaPCMStates() map[string]bluealsaPCMState {
	res := map[string]bluealsaPCMState{}
	out, err := a.run.Run(3*time.Second, "bluealsa-cli", "list-pcms")
	if err != nil {
		return res
	}
	volRE := regexp.MustCompile(`(?i)Volume:\s*L:\s*(\d+)(?:\s+R:\s*(\d+))?`)
	for _, raw := range strings.Split(out, "\n") {
		path := strings.TrimSpace(raw)
		if !strings.HasPrefix(path, "/org/bluealsa/") {
			continue
		}
		addr := addrFromBluezPath(path)
		if addr == "" {
			continue
		}
		info, infoErr := a.run.Run(2*time.Second, "bluealsa-cli", "info", path)
		if infoErr != nil || !strings.Contains(strings.ToUpper(info), "A2DP") {
			continue
		}
		st := bluealsaPCMState{Path: path}
		if m := volRE.FindStringSubmatch(info); len(m) >= 2 {
			l, _ := strconv.Atoi(m[1])
			r := l
			if len(m) >= 3 && m[2] != "" {
				r, _ = strconv.Atoi(m[2])
			}
			st.Volume = (l + r + 1) / 2
			st.VolumeKnown = true
		}
		// Prefer the first A2DP PCM for an address. The current OpenAudioHub
		// BlueALSA daemon only exposes the receive-side A2DP profile, so there is
		// normally exactly one.
		if _, exists := res[addr]; !exists {
			res[addr] = st
		}
	}
	return res
}

func (a *App) bluealsaPCMAddresses() map[string]bool {
	set := map[string]bool{}
	out, err := a.run.Run(3*time.Second, "bluealsa-aplay", "-L")
	if err != nil {
		return set
	}
	for _, m := range macRE.FindAllString(out, -1) {
		set[strings.ToUpper(m)] = true
	}
	return set
}

func (a *App) setTransportVolume(addr string, percent int) error {
	addr = cleanAddr(addr)
	if addr == "" {
		return fmtErr("invalid Bluetooth address")
	}
	if percent < 0 || percent > 100 {
		return fmtErr("Bluetooth volume must be between 0 and 100 percent")
	}
	value := int(float64(percent)*127.0/100.0 + 0.5)

	// BlueALSA uses soft-volume by design in the secondary receive path. In
	// that mode its PCM volume is intentionally independent of BlueZ AVRCP /
	// MediaTransport volume, so control it through BlueALSA's own D-Bus client.
	// This keeps the device-card volume domain separate from PipeWire mixer gain.
	if pcm, ok := a.bluealsaPCMStates()[strings.ToUpper(addr)]; ok && pcm.Path != "" {
		if _, err := a.run.Run(4*time.Second, "bluealsa-cli", "volume", pcm.Path, strconv.Itoa(value)); err != nil {
			return err
		}
		a.signalRefresh()
		return nil
	}

	matched := false
	for _, t := range a.listTransports() {
		if !strings.EqualFold(t.Addr, addr) {
			continue
		}
		matched = true
		if _, err := a.run.Run(4*time.Second, "bluetoothctl", "transport.volume", t.Path, strconv.Itoa(value)); err != nil {
			return err
		}
	}
	if !matched {
		return fmtErr("the device has no active Bluetooth audio transport")
	}
	a.signalRefresh()
	return nil
}

func (a *App) btAction(addr, action string) error {
	addr = cleanAddr(addr)
	if addr == "" {
		return fmtErr("invalid Bluetooth address")
	}

	a.btOpsMu.Lock()
	defer a.btOpsMu.Unlock()

	if action == "forget" {
		hadRole := a.roleFor(addr) != ""
		if hadRole {
			if err := a.setRole(addr, ""); err != nil {
				return err
			}
		}
		_ = a.cfg.Update(func(c *Config) error { delete(c.DevicePrefs, addr); return nil })
		_, _ = a.run.Run(5*time.Second, "bluetoothctl", "disconnect", addr)
		out, err := a.run.Run(10*time.Second, "bluetoothctl", "remove", addr)
		if err != nil {
			lower := strings.ToLower(out + " " + err.Error())
			if !strings.Contains(lower, "not available") && !strings.Contains(lower, "does not exist") && !strings.Contains(lower, "not found") {
				return err
			}
		}
		a.logf("Bluetooth device forgotten: %s", addr)
		a.signalRefresh()
		return nil
	}

	if action == "connect" || action == "disconnect" {
		// A deliberate user action must not be throttled by earlier auto-connect
		// failures, and must clear a previous failure so the UI can recover.
		a.forgetConnectState(addr)
	}
	switch action {
	case "pair":
		if out, e := a.run.Run(3*time.Second, "bluetoothctl", "devices"); e == nil {
			if name := parseBluetoothDeviceLines(out)[addr]; usableBluetoothName(name, addr) {
				a.rememberDeviceName(addr, name)
			}
		}
		// Pairing a headset routinely takes longer than a controller round trip: the
		// peer may first have to establish a link, and some devices wait for the user
		// to confirm. A short deadline killed bluetoothctl mid-pairing, which left the
		// device trusted but unbonded and made the UI ask to pair again.
		if _, err := a.run.Run(pairTimeout, "bluetoothctl", "pair", addr); err != nil {
			return err
		}
		// Do not trust the exit status alone. A raced or interrupted attempt can exit
		// without storing a bond, and trusting an unbonded device is what produced the
		// "Paired: no / Trusted: yes" state that kept the UI asking to pair.
		if d := a.bluetoothInfo(addr, ""); !d.Paired {
			return userErrorf("the device did not complete pairing — put it in pairing mode and try again")
		}
		// Audio reconnects should not block on authorization prompts after pairing.
		_, _ = a.run.Run(5*time.Second, "bluetoothctl", "trust", addr)
		return nil
	case "connect":
		role := a.roleFor(addr)
		// A device with no role has no route, so connecting it cannot produce audio.
		// Never fall through to a raw BlueZ error: whether the capability read
		// succeeds or not, the actionable answer is the same.
		if role == "" {
			caps := a.bluetoothInfo(addr, "").Caps
			switch {
			case contains(caps, "sends_audio") && contains(caps, "plays_audio"):
				return userErrorf("assign this device to an input or an output before connecting it")
			case contains(caps, "sends_audio"):
				return userErrorf("assign this device to Input 1 or Input 2 before connecting it")
			case contains(caps, "plays_audio"):
				return userErrorf("assign this device to an output before connecting it")
			default:
				return userErrorf("assign this device to a slot before connecting it")
			}
		}
		args := []string{"connect", addr}
		if strings.HasPrefix(role, "in") {
			// There are exactly two local A2DP sink SEPs in the current engine:
			// PipeWire + BlueALSA. A third source failing with EBUSY is capacity,
			// not a controller crash. Surface that clearly for UI-initiated connects.
			occupied := 0
			for _, t := range a.listTransports() {
				if strings.Contains(t.UUID, "Audio Sink") && !strings.EqualFold(t.Addr, addr) {
					occupied++
				}
			}
			if occupied >= 2 {
				return userErrorf("both Bluetooth input endpoints are already occupied")
			}
			args = append(args, "a2dp-source")
		} else if strings.HasPrefix(role, "out") {
			// The engine has a single A2DP source endpoint, so connecting a second
			// output is a switch, not an addition. Release the output that currently
			// holds the endpoint first; otherwise BlueZ answers with
			// "Unable to select SEP" / br-connection-create-socket and the attempt can
			// never succeed.
			if holder := a.activeOutputHolder(addr); holder != "" {
				a.logf("switching output: releasing %s first", holder)
				_, _ = a.run.Run(10*time.Second, "bluetoothctl", "disconnect", holder)
				time.Sleep(1200 * time.Millisecond)
			}
			args = append(args, "a2dp-sink")
		}
		_, err := a.run.Run(connectTimeout, "bluetoothctl", args...)
		if err == nil && strings.HasPrefix(role, "out") {
			a.setDefaultOutput(addr)
		}
		return err
	case "disconnect":
		_, err := a.run.Run(10*time.Second, "bluetoothctl", "disconnect", addr)
		return err
	case "trust":
		_, err := a.run.Run(8*time.Second, "bluetoothctl", "trust", addr)
		return err
	case "untrust":
		_, err := a.run.Run(8*time.Second, "bluetoothctl", "untrust", addr)
		return err
	default:
		return fmtErr("unknown bluetooth action %q", action)
	}
}

func (a *App) setPairing(enable bool) error {
	val := "off"
	if enable {
		val = "on"
	}
	if _, err := a.run.Run(4*time.Second, "bluetoothctl", "pairable", val); err != nil {
		return err
	}
	if _, err := a.run.Run(4*time.Second, "bluetoothctl", "discoverable", val); err != nil {
		return err
	}
	if enable {
		_ = os.MkdirAll("/run/openaudiohub", 0755)
		_ = os.WriteFile("/run/openaudiohub/pairing-enabled", []byte("1\n"), 0644)
	} else {
		_ = os.Remove("/run/openaudiohub/pairing-enabled")
	}
	a.mu.Lock()
	a.pairing.Active = enable
	if enable {
		a.pairing.Until = time.Now().Add(time.Duration(a.cfg.Get().UI.PairingTimeoutSec) * time.Second)
	} else {
		a.pairing.Until = time.Time{}
	}
	a.mu.Unlock()
	if enable {
		go func(until time.Time) {
			time.Sleep(time.Until(until))
			a.mu.RLock()
			same := a.pairing.Active && a.pairing.Until.Equal(until)
			a.mu.RUnlock()
			if same {
				_ = a.setPairing(false)
				a.signalRefresh()
			}
		}(a.pairing.Until)
	}
	return nil
}

func (a *App) scanBluetooth() {
	a.mu.Lock()
	if a.pairing.Scanning {
		a.mu.Unlock()
		return
	}
	a.pairing.Scanning = true
	a.mu.Unlock()
	a.signalRefresh()
	go func() {
		out, _ := a.run.Run(8*time.Second, "bluetoothctl", "--timeout", "6", "scan", "on")
		// Scan output is often the only moment a device exposes a useful friendly
		// name. Cache it before pairing can replace Alias/Name with a MAC-like value.
		for addr, name := range parseBluetoothDeviceLines(out) {
			if usableBluetoothName(name, addr) {
				a.rememberDeviceName(addr, name)
			}
		}
		a.mu.Lock()
		a.pairing.Scanning = false
		a.mu.Unlock()
		a.signalRefresh()
	}()
}
