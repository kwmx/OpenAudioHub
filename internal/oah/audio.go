package oah

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (a *App) applyAudioConfig(cfg AudioConfig) error {
	a.audioConfigMu.Lock()
	defer a.audioConfigMu.Unlock()
	if err := validateReceiverOptions(cfg); err != nil {
		return err
	}
	if cfg.Quantum < 256 || cfg.Quantum > 8192 || cfg.Quantum%128 != 0 {
		return fmtErr("quantum must be 256..8192 in 128-sample steps")
	}
	if cfg.PreferredRate != 44100 && cfg.PreferredRate != 48000 && cfg.PreferredRate != 96000 {
		return fmtErr("unsupported preferred rate")
	}
	if len(cfg.AllowedRates) == 0 || len(cfg.AllowedRates) > 3 {
		return fmtErr("select at least one allowed sample rate")
	}
	seenRates := map[int]bool{}
	for _, r := range cfg.AllowedRates {
		if r != 44100 && r != 48000 && r != 96000 {
			return fmtErr("unsupported allowed sample rate")
		}
		seenRates[r] = true
	}
	if !seenRates[cfg.PreferredRate] {
		return fmtErr("preferred sample rate must also be allowed")
	}
	if cfg.BlueALSAPeriodUS < 10000 || cfg.BlueALSAPeriodUS > 250000 {
		return fmtErr("BlueALSA period must be 10–250 ms")
	}
	// Input 2 currently uses Debian's BlueALSA 4.3.x player, which does not
	// provide adaptive resampling. Keep the period on an exact 10 ms boundary:
	// at both 44.1 kHz and 48 kHz that produces an integer frame count and
	// avoids the slow clock drift / periodic blips caused by rounded periods.
	if cfg.BlueALSAPeriodUS%10000 != 0 {
		return fmtErr("BlueALSA period must be a multiple of 10 ms for stable 44.1/48 kHz bridging")
	}
	if cfg.BlueALSABufferUS < cfg.BlueALSAPeriodUS*3 || cfg.BlueALSABufferUS > 1000000 {
		return fmtErr("BlueALSA buffer must be at least 3 periods and no more than 1 second")
	}
	if cfg.BlueALSABufferUS%cfg.BlueALSAPeriodUS != 0 {
		return fmtErr("BlueALSA buffer must be an exact multiple of the period")
	}
	if cfg.CodecPolicy != "compatibility" && cfg.CodecPolicy != "quality" {
		return fmtErr("unsupported codec policy")
	}
	if cfg.Resampler != "auto" {
		return fmtErr("this BlueALSA build does not expose an adaptive resampler; use Automatic")
	}
	cfg.LiveMeters = false
	switch cfg.Preset {
	case "low", "balanced", "stable", "custom":
	default:
		cfg.Preset = "custom"
	}
	old := a.cfg.Get().Audio
	if err := a.cfg.Update(func(c *Config) error { c.Audio = cfg; return nil }); err != nil {
		return err
	}
	if err := a.writeAudioProjection(cfg); err != nil {
		_ = a.cfg.Update(func(c *Config) error { c.Audio = old; return nil })
		return fmtErr("could not write non-secret audio configuration: %v", err)
	}
	graphChanged := old.PreferredRate != cfg.PreferredRate || old.Quantum != cfg.Quantum || old.CodecPolicy != cfg.CodecPolicy || mustJSON(old.AllowedRates) != mustJSON(cfg.AllowedRates)
	bridgeChanged := old.BlueALSAPeriodUS != cfg.BlueALSAPeriodUS || old.BlueALSABufferUS != cfg.BlueALSABufferUS
	receiverChanged := old.SecondarySBCMaxBitpool != cfg.SecondarySBCMaxBitpool || old.SecondaryAdvertisedDelayMS != cfg.SecondaryAdvertisedDelayMS
	// Never start an idle player merely because settings were saved. Receiver
	// capabilities take effect only after the BlueALSA peer reconnects.
	if graphChanged {
		if _, err := a.run.Run(28*time.Second, "systemctl", "restart", "openaudiohub-audio-tuning.service"); err != nil {
			return fmtErr("settings saved, but the audio graph could not be restarted; inspect Diagnostics")
		}
	}
	if receiverChanged {
		if err := a.restartSecondaryReceiverLocked(); err != nil {
			return err
		}
	} else if bridgeChanged {
		if _, err := a.run.Run(10*time.Second, "systemctl", "try-restart", "openaudiohub-bluealsa-bridge.service"); err != nil {
			return fmtErr("settings saved, but the secondary bridge did not restart; inspect Diagnostics")
		}
	}
	if graphChanged || receiverChanged {
		a.requestReconcile("audio settings changed")
	}
	a.signalRefresh()
	return nil
}

// restartSecondaryReceiverLocked cycles the secondary receiver so it re-reads
// audio.json at startup. The caller must hold audioConfigMu.
//
// Requested capabilities only reach the source after the peer reconnects, so this
// does not report success by itself: the outcome is observable through
// DelayReport, which stays pending until the acquired transport shows the value.
func (a *App) restartSecondaryReceiverLocked() error {
	a.btOpsMu.Lock()
	// Restart only the secondary receiver; the primary and output remain intact.
	for addr := range a.bluealsaPCMAddresses() {
		_, _ = a.run.Run(6*time.Second, "bluetoothctl", "disconnect", addr)
	}
	_, err := a.run.Run(12*time.Second, "systemctl", "try-restart", "openaudiohub-bluealsa.service")
	a.btOpsMu.Unlock()
	if err != nil {
		return fmtErr("settings saved, but the secondary receiver did not restart; reconnect after checking Diagnostics")
	}
	return nil
}

// applyReceiverDelay changes only the secondary receiver's advertised delay and
// leaves every other audio setting, and playback, untouched. ms == 0 restores the
// engine default. It waits for nothing: report the resulting DelayReport instead
// of claiming success here.
func (a *App) applyReceiverDelay(ms int) error {
	a.audioConfigMu.Lock()
	defer a.audioConfigMu.Unlock()
	if ms < 0 || ms > 2000 {
		return fmtErr("advertised delay must be 0\u20132000 ms; 0 uses the engine default")
	}
	old := a.cfg.Get().Audio
	if old.SecondaryAdvertisedDelayMS == ms {
		return nil
	}
	next := old
	next.SecondaryAdvertisedDelayMS = ms
	if err := a.cfg.Update(func(c *Config) error { c.Audio.SecondaryAdvertisedDelayMS = ms; return nil }); err != nil {
		return err
	}
	if err := a.writeAudioProjection(next); err != nil {
		_ = a.cfg.Update(func(c *Config) error { c.Audio.SecondaryAdvertisedDelayMS = old.SecondaryAdvertisedDelayMS; return nil })
		return fmtErr("could not write non-secret audio configuration: %v", err)
	}
	if err := a.restartSecondaryReceiverLocked(); err != nil {
		return err
	}
	a.requestReconcile("advertised delay changed")
	a.signalRefresh()
	return nil
}

func (a *App) setRole(addr, role string) error {
	addr = cleanAddr(addr)
	if addr == "" {
		return fmtErr("invalid Bluetooth address")
	}
	if role != "" {
		d := a.bluetoothInfo(addr, "")
		if !d.Paired {
			return fmtErr("pair the device before assigning a role")
		}
		switch role {
		case "in1", "in2":
			if !contains(d.Caps, "sends_audio") {
				return fmtErr("this device does not advertise Bluetooth audio output capability")
			}
		case "out1", "out2":
			if !contains(d.Caps, "plays_audio") {
				return fmtErr("this device does not advertise Bluetooth audio playback capability")
			}
		default:
			return fmtErr("invalid role")
		}
	}
	return a.cfg.Update(func(c *Config) error {
		for i, x := range c.Slots.Inputs {
			if strings.EqualFold(x, addr) {
				c.Slots.Inputs[i] = ""
			}
		}
		for i, x := range c.Slots.Outputs {
			if strings.EqualFold(x, addr) {
				c.Slots.Outputs[i] = ""
			}
		}
		switch role {
		case "":
		case "in1", "in2":
			idx := atoiLoose(strings.TrimPrefix(role, "in")) - 1
			if idx < 0 || idx >= len(c.Slots.Inputs) {
				return fmtErr("invalid input role")
			}
			c.Slots.Inputs[idx] = addr
		case "out1", "out2":
			idx := atoiLoose(strings.TrimPrefix(role, "out")) - 1
			if idx < 0 || idx >= len(c.Slots.Outputs) {
				return fmtErr("invalid output role")
			}
			c.Slots.Outputs[idx] = addr
		default:
			return fmtErr("invalid role")
		}
		if role != "" {
			if c.DevicePrefs == nil {
				c.DevicePrefs = map[string]DevicePrefs{}
			}
			if _, ok := c.DevicePrefs[addr]; !ok {
				c.DevicePrefs[addr] = DevicePrefs{AutoConnect: true}
			}
		}
		return nil
	})
}

func (a *App) reconcileRoutes(reason string) {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	a.btOpsMu.Lock()
	defer a.btOpsMu.Unlock()
	a.logf("reconcile: %s", reason)
	now := time.Now()

	c := a.cfg.Get()
	connected := a.bluetoothDeviceSubset("Connected")
	if connected == nil {
		connected = map[string]bool{}
	}
	// BlueZ's generic Device1 Connected flag only means that an ACL link exists.
	// A source can remain ACL-connected after its A2DP profile failed, which made
	// older releases believe a broken route was healthy forever. Route health is
	// therefore based on actual MediaTransport objects. Idle transports still
	// count: they represent a configured A2DP profile waiting for audio.
	transports := a.listTransports()
	inputMedia := map[string]bool{}
	outputMedia := map[string]bool{}
	for _, t := range transports {
		addr := strings.ToUpper(t.Addr)
		if strings.Contains(t.UUID, "Audio Sink") {
			inputMedia[addr] = true
		}
		if strings.Contains(t.UUID, "Audio Source") {
			outputMedia[addr] = true
		}
	}

	// A paired-but-unassigned audio source must not silently consume one of the
	// two local sink SEPs. This can otherwise make an assigned source fail with
	// BlueZ EBUSY even though the UI appears to have a free slot. Only police
	// actual A2DP media transports; unrelated Bluetooth devices are untouched.
	assignedInputs := map[string]bool{}
	for _, raw := range c.Slots.Inputs {
		if raw != "" {
			assignedInputs[strings.ToUpper(raw)] = true
		}
	}
	assignedOutputs := map[string]bool{}
	outputOrder := make([]string, 0, len(c.Slots.Outputs))
	for _, raw := range c.Slots.Outputs {
		if raw == "" {
			continue
		}
		addr := strings.ToUpper(raw)
		if !assignedOutputs[addr] {
			assignedOutputs[addr] = true
			outputOrder = append(outputOrder, addr)
		}
	}
	for _, t := range transports {
		addr := strings.ToUpper(t.Addr)
		switch {
		case strings.Contains(t.UUID, "Audio Sink") && addr != "" && !assignedInputs[addr]:
			a.logf("disconnecting unassigned A2DP source %s to free input SEP", addr)
			_, _ = a.run.Run(6*time.Second, "bluetoothctl", "disconnect", addr)
			delete(connected, addr)
			delete(inputMedia, addr)
		case strings.Contains(t.UUID, "Audio Source") && addr != "" && !assignedOutputs[addr]:
			a.logf("disconnecting unassigned A2DP output %s", addr)
			_, _ = a.run.Run(6*time.Second, "bluetoothctl", "disconnect", addr)
			delete(connected, addr)
			delete(outputMedia, addr)
		}
	}

	// Keep every assigned output connected and audible. Only initiate a connection
	// when Auto is enabled; manual Connect bypasses this policy.
	// The engine registers a single A2DP Source endpoint, so only one Bluetooth
	// output can hold it at a time. Attempting a second produces
	// "Unable to select SEP" from BlueZ and a connection that never settles, so skip
	// it and let the state explain why instead of retrying forever.
	sourceHolder := ""
	for _, addr := range outputOrder {
		if outputMedia[addr] {
			sourceHolder = addr
			break
		}
	}
	for _, addr := range outputOrder {
		if outputMedia[addr] {
			continue
		}
		if sourceHolder != "" {
			a.logf("output %s waiting: %s already holds the single A2DP source endpoint", addr, sourceHolder)
			continue
		}
		if !a.autoConnectFor(addr) {
			continue
		}
		if a.connectTooSoon(addr, now) {
			continue
		}
		// Even if the generic Bluetooth ACL is already connected, explicitly request
		// A2DP when its media transport is missing.
		_, err := a.run.Run(connectTimeout, "bluetoothctl", "connect", addr, "a2dp-sink")
		a.noteConnectAttempt(addr, err == nil, now)
		if err != nil {
			a.logf("auto-connect output %s: %v", addr, err)
		} else {
			connected[addr] = true
			outputMedia[addr] = true
		}
	}
	// Route after connecting, so both sink nodes exist before the fan-out is built.
	a.ensureOutputRouting(outputOrder)

	inputs := make([]string, 0, len(c.Slots.Inputs))
	for _, raw := range c.Slots.Inputs {
		if raw != "" {
			inputs = append(inputs, strings.ToUpper(raw))
		}
	}
	connectedInputs := 0
	for _, addr := range inputs {
		if inputMedia[addr] {
			connectedInputs++
		}
	}

	// The proven manual staging sequence was:
	//   PipeWire sink only -> first source -> start BlueALSA second SEP -> second source.
	// Reproduce that sequence at cold establishment (or an explicit input-role
	// change), but never tear down healthy sessions during ordinary health checks.
	forceRebind := reason == "input roles changed"
	if len(inputs) > 0 && (forceRebind || connectedInputs == 0) {
		if forceRebind {
			for _, addr := range inputs {
				_, _ = a.run.Run(5*time.Second, "bluetoothctl", "disconnect", addr)
				delete(connected, addr)
				delete(inputMedia, addr)
			}
		}
		_, _ = a.run.Run(8*time.Second, "systemctl", "stop", "openaudiohub-bluealsa-bridge.service")
		_, _ = a.run.Run(8*time.Second, "systemctl", "stop", "openaudiohub-bluealsa.service")

		// Input 1 (or the only configured input) gets the primary PipeWire SEP.
		first := inputs[0]
		if a.autoConnectFor(first) || forceRebind {
			if _, err := a.run.Run(12*time.Second, "bluetoothctl", "connect", first, "a2dp-source"); err != nil {
				a.logf("primary input connect %s: %v", first, err)
			} else {
				connected[first] = true
				inputMedia[first] = true
			}
		}

		if len(inputs) > 1 {
			_, _ = a.run.Run(8*time.Second, "systemctl", "start", "openaudiohub-bluealsa.service")
			time.Sleep(700 * time.Millisecond) // allow the second SBC SEP to register
			second := inputs[1]
			if a.autoConnectFor(second) || forceRebind {
				if _, err := a.run.Run(12*time.Second, "bluetoothctl", "connect", second, "a2dp-source"); err != nil {
					a.logf("secondary input connect %s: %v", second, err)
				} else {
					connected[second] = true
					inputMedia[second] = true
				}
			}
			// Match the hand-tested staging exactly: establish the second A2DP
			// transport first, then attach the long-lived BlueALSA->PipeWire
			// player. Starting the player before the remote transport was not
			// required by the successful prototype and added an avoidable race.
			time.Sleep(250 * time.Millisecond)
			_, _ = a.run.Run(8*time.Second, "systemctl", "start", "openaudiohub-bluealsa-bridge.service")
		}
		a.applyMixer()
		a.signalRefresh()
		return
	}

	// Once a session is healthy, endpoints are long-lived. This is the crucial
	// difference from 0.1.3: periodic reconcile never disconnects a source or
	// restarts BlueALSA just to maintain logical slot order.
	if len(inputs) > 1 {
		_, _ = a.run.Run(8*time.Second, "systemctl", "start", "openaudiohub-bluealsa.service")
		_, _ = a.run.Run(8*time.Second, "systemctl", "start", "openaudiohub-bluealsa-bridge.service")
	}
	for _, addr := range inputs {
		if inputMedia[addr] || !a.autoConnectFor(addr) {
			continue
		}
		if a.connectTooSoon(addr, now) {
			continue
		}
		_, err := a.run.Run(connectTimeout, "bluetoothctl", "connect", addr, "a2dp-source")
		a.noteConnectAttempt(addr, err == nil, now)
		if err != nil {
			a.logf("auto-connect input %s: %v", addr, err)
		} else {
			connected[addr] = true
			inputMedia[addr] = true
		}
		time.Sleep(350 * time.Millisecond)
	}

	a.applyMixer()
	a.signalRefresh()
}

func (a *App) routeSupervisor() {
	// Low-rate best-effort reconnect loop. It never tears down working AVDTP
	// sessions; it only asks reconcile to fill missing Auto-enabled slots.
	t := time.NewTicker(45 * time.Second)
	defer t.Stop()
	for range t.C {
		a.requestReconcile("auto-connect supervisor")
	}
}

// sinkNameFor resolves a Bluetooth device address to its current PipeWire sink
// name, or "" when that device has no sink node in the graph yet.
func (a *App) sinkNameFor(addr string) string {
	normalized := strings.ReplaceAll(strings.ToUpper(cleanAddr(addr)), ":", "_")
	if normalized == "" {
		return ""
	}
	out, _ := a.userPactl("list", "short", "sinks")
	for _, l := range strings.Split(out, "\n") {
		fs := strings.Fields(l)
		if len(fs) > 1 && strings.Contains(strings.ToUpper(fs[1]), normalized) {
			return fs[1]
		}
	}
	return ""
}

func (a *App) setDefaultOutput(addr string) {
	if name := a.sinkNameFor(addr); name != "" {
		_, _ = a.userPactl("set-default-sink", name)
	}
}

// combineSinkName is the virtual sink used to feed two outputs at once.
const combineSinkName = "openaudiohub_multi"

// ensureOutputRouting makes the mix reach every assigned output.
//
// One output behaves exactly as before: that device's sink becomes the default.
// Two outputs need an explicit fan-out, because WirePlumber only routes a stream
// to one default sink; a combine sink is created with both as slaves. The module
// is only reloaded when the participating sinks actually change, so the 45s
// reconcile loop does not interrupt audio.
func (a *App) ensureOutputRouting(addrs []string) {
	a.outputRouteMu.Lock()
	defer a.outputRouteMu.Unlock()

	sinks := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if name := a.sinkNameFor(addr); name != "" {
			sinks = append(sinks, name)
		}
	}

	// Fewer than two usable sinks: no fan-out. This is the original path.
	if len(sinks) < 2 {
		a.unloadCombineSinkLocked()
		if len(sinks) == 1 {
			_, _ = a.userPactl("set-default-sink", sinks[0])
		}
		return
	}

	want := strings.Join(sinks, ",")
	if a.combineModule != "" && a.combineSlaves == want {
		// Already correct. Re-assert the default in case another client moved it.
		_, _ = a.userPactl("set-default-sink", combineSinkName)
		return
	}
	if a.combineModule != "" {
		a.unloadCombineSinkLocked()
	}
	out, err := a.userPactl("load-module", "module-combine-sink", "sink_name="+combineSinkName, "slaves="+want)
	if err != nil {
		// Never leave the appliance silent: fall back to the first output.
		a.logf("multi-output fan-out failed (%v); using %s only", err, sinks[0])
		_, _ = a.userPactl("set-default-sink", sinks[0])
		return
	}
	a.combineModule = strings.TrimSpace(out)
	a.combineSlaves = want
	_, _ = a.userPactl("set-default-sink", combineSinkName)
	a.logf("multi-output fan-out active: %s -> %s", combineSinkName, want)
}

func (a *App) unloadCombineSinkLocked() {
	if a.combineModule == "" {
		return
	}
	_, _ = a.userPactl("unload-module", a.combineModule)
	a.logf("multi-output fan-out removed")
	a.combineModule = ""
	a.combineSlaves = ""
}

func (a *App) applyMixer() {
	a.mixerApplyMu.Lock()
	defer a.mixerApplyMu.Unlock()
	c := a.cfg.Get()
	blocks := a.sinkInputBlocks()
	bluealsaAddrs := a.bluealsaPCMAddresses()
	for i, addr := range c.Slots.Inputs {
		if addr == "" || i >= len(c.Mixer.Gains) {
			continue
		}
		id := findSinkInputFor(blocks, addr, bluealsaAddrs)
		if id == "" {
			continue
		}

		// Input gain and Master are internal digital mixer stages. They are
		// deliberately separate from Bluetooth transport volume on the device cards.
		effectiveDB := c.Mixer.Gains[i] + c.Mixer.HeadroomDB + c.Mixer.Master
		vol := dbToPercent(effectiveDB)
		placement := "stereo"
		if i < len(c.Mixer.Placement) {
			placement = c.Mixer.Placement[i]
		}
		switch placement {
		case "left":
			_, _ = a.userPactl("set-sink-input-volume", id, fmt.Sprintf("%d%%", vol), "0%")
		case "right":
			_, _ = a.userPactl("set-sink-input-volume", id, "0%", fmt.Sprintf("%d%%", vol))
		default:
			_, _ = a.userPactl("set-sink-input-volume", id, fmt.Sprintf("%d%%", vol), fmt.Sprintf("%d%%", vol))
		}
		mute := "0"
		if c.Mixer.MasterMute || (i < len(c.Mixer.Mutes) && c.Mixer.Mutes[i]) {
			mute = "1"
		}
		_, _ = a.userPactl("set-sink-input-mute", id, mute)
	}
}

func dbToPercent(db float64) int {
	p := int(100*math.Pow(10, db/20) + 0.5)
	if p < 0 {
		p = 0
	}
	if p > 150 {
		p = 150
	}
	return p
}

func (a *App) audioUserCommand(timeout time.Duration, name string, args ...string) (string, error) {
	c := a.cfg.Get()
	home := "/home/" + c.AudioUser
	if u, err := user.Lookup(c.AudioUser); err == nil && u.HomeDir != "" {
		home = u.HomeDir
	}
	runtime := fmt.Sprintf("/run/user/%d", c.AudioUID)
	base := []string{
		"-u", c.AudioUser, "--", "env",
		"HOME=" + home,
		"XDG_RUNTIME_DIR=" + runtime,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtime + "/bus",
		"PULSE_SERVER=unix:" + runtime + "/pulse/native",
		name,
	}
	return a.run.Run(timeout, "runuser", append(base, args...)...)
}

func (a *App) userPactl(args ...string) (string, error) {
	return a.audioUserCommand(5*time.Second, "pactl", args...)
}
func (a *App) sinkInputBlocks() map[string]string {
	out, _ := a.userPactl("list", "sink-inputs")
	res := map[string]string{}
	re := regexp.MustCompile(`Sink Input #(\d+)`)
	var id string
	var b strings.Builder
	flush := func() {
		if id != "" {
			res[id] = b.String()
		}
		b.Reset()
	}
	for _, l := range strings.Split(out, "\n") {
		if m := re.FindStringSubmatch(l); len(m) == 2 {
			flush()
			id = m[1]
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	flush()
	return res
}
func findSinkInputFor(blocks map[string]string, addr string, bluealsaAddrs map[string]bool) string {
	needle := strings.ToUpper(strings.ReplaceAll(addr, ":", "_"))
	for id, b := range blocks {
		if strings.Contains(strings.ToUpper(b), needle) {
			return id
		}
	}
	if bluealsaAddrs[strings.ToUpper(addr)] {
		for id, b := range blocks {
			low := strings.ToLower(b)
			if strings.Contains(low, "openaudiohub.bluealsa") || strings.Contains(low, "openaudiohub-bluealsa") || strings.Contains(low, "bluealsa-aplay") {
				return id
			}
		}
	}
	return ""
}

func (a *App) updateMixer(next MixerConfig) error {
	inputs := len(a.cfg.Get().Slots.Inputs)
	if len(next.Gains) != inputs || len(next.Mutes) != inputs || len(next.Placement) != inputs {
		return fmtErr("mixer channel configuration is incomplete")
	}
	for _, g := range next.Gains {
		if math.IsNaN(g) || math.IsInf(g, 0) || g < -30 || g > 6 {
			return fmtErr("input gain must be between -30 and +6 dB")
		}
	}
	for _, p := range next.Placement {
		if p != "stereo" && p != "left" && p != "right" {
			return fmtErr("invalid channel placement")
		}
	}
	if math.IsNaN(next.Master) || math.IsInf(next.Master, 0) || next.Master < -30 || next.Master > 6 {
		return fmtErr("master gain must be between -30 and +6 dB")
	}
	if math.IsNaN(next.HeadroomDB) || math.IsInf(next.HeadroomDB, 0) || next.HeadroomDB < -18 || next.HeadroomDB > 0 {
		return fmtErr("headroom must be between -18 and 0 dB")
	}
	if err := a.cfg.Update(func(c *Config) error { c.Mixer = next; return nil }); err != nil {
		return err
	}
	a.applyMixer()
	return nil
}

func (a *App) audioReady() error {
	// Service state is a more reliable appliance health signal than a single CLI
	// client probe. wpctl/pactl can transiently fail while the graph is still
	// streaming, which previously produced false "Audio graph unavailable" UI.
	out, err := a.audioUserCommand(3*time.Second, "systemctl", "--user", "is-active", "pipewire.service", "wireplumber.service")
	if err == nil && strings.Count(strings.TrimSpace(out), "active") >= 2 {
		return nil
	}
	if _, wpErr := a.audioUserCommand(3*time.Second, "wpctl", "status", "-n"); wpErr == nil {
		return nil
	}
	go func() { _ = a.ensureAudioSession() }()
	if err != nil {
		return err
	}
	return fmtErr("PipeWire user services are not active")
}

func (a *App) ensureAudioSession() error {
	a.audioHealMu.Lock()
	defer a.audioHealMu.Unlock()
	// Avoid restart storms if BlueZ/PipeWire is genuinely unavailable.
	if !a.audioLastHeal.IsZero() && time.Since(a.audioLastHeal) < 20*time.Second {
		_, err := a.audioUserCommand(2*time.Second, "wpctl", "status", "-n")
		return err
	}
	a.audioLastHeal = time.Now()
	c := a.cfg.Get()
	_, _ = a.run.Run(5*time.Second, "systemctl", "start", fmt.Sprintf("user@%d.service", c.AudioUID))
	_, _ = a.audioUserCommand(8*time.Second, "systemctl", "--user", "start",
		"pipewire.socket", "pipewire-pulse.socket", "pipewire.service", "pipewire-pulse.service", "wireplumber.service")
	var last error
	for i := 0; i < 8; i++ {
		if _, err := a.audioUserCommand(2*time.Second, "wpctl", "status", "-n"); err == nil {
			// Best effort: wake the Pulse compatibility socket too because mixer and
			// default-sink controls use pactl.
			_, _ = a.userPactl("info")
			return nil
		} else {
			last = err
		}
		time.Sleep(350 * time.Millisecond)
	}
	return last
}

func (a *App) mixerEventSupervisor() {
	// Mixer gain belongs to PipeWire, while Bluetooth device volume belongs to
	// the transport. A BlueALSA ALSA stream is created only when a remote source
	// actually starts sending audio, so apply the saved internal gain when a new
	// sink-input appears instead of conflating it with transport volume.
	for {
		c := a.cfg.Get()
		home := "/home/" + c.AudioUser
		if u, err := user.Lookup(c.AudioUser); err == nil && u.HomeDir != "" {
			home = u.HomeDir
		}
		runtime := fmt.Sprintf("/run/user/%d", c.AudioUID)
		args := []string{
			"-u", c.AudioUser, "--", "env",
			"HOME=" + home,
			"XDG_RUNTIME_DIR=" + runtime,
			"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtime + "/bus",
			"PULSE_SERVER=unix:" + runtime + "/pulse/native",
			"pactl", "subscribe",
		}
		cmd := exec.Command("runuser", args...)
		stdout, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "Event 'new' on sink-input") {
				time.Sleep(80 * time.Millisecond)
				a.applyMixer()
				a.signalRefresh()
			}
		}
		_ = cmd.Wait()
		time.Sleep(2 * time.Second)
	}
}

func (a *App) audioSupervisor() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for range t.C {
		if _, err := a.audioUserCommand(2*time.Second, "wpctl", "status", "-n"); err != nil {
			a.logf("audio supervisor: PipeWire unavailable, attempting recovery: %v", err)
			_ = a.ensureAudioSession()
			a.signalRefresh()
		}
	}
}

func (a *App) audioXRuns() (int, error) {
	out, err := a.audioUserCommand(3*time.Second, "pw-top", "-b", "-n", "1")
	if err != nil {
		return 0, err
	}
	sum := 0
	for _, l := range strings.Split(out, "\n") {
		fs := strings.Fields(l)
		if len(fs) < 9 {
			continue
		}
		if n, e := strconv.Atoi(fs[8]); e == nil {
			sum += n
		}
	}
	return sum, nil
}

func (a *App) applyIdentity(hostname, btName string) error {
	hostname = strings.TrimSpace(hostname)
	btName = strings.TrimSpace(btName)
	if hostname != "" {
		if len(hostname) > 63 || !regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`).MatchString(hostname) {
			return fmtErr("hostname must be 1–63 letters, numbers, or hyphens and cannot start or end with a hyphen")
		}
		if _, err := a.run.Run(8*time.Second, "hostnamectl", "set-hostname", hostname); err != nil {
			return err
		}
	}
	if btName != "" {
		if len([]byte(btName)) > 64 || strings.IndexFunc(btName, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return fmtErr("Bluetooth name must be 1–64 characters without control characters")
		}
		if err := writeBluetoothMainConfName(btName); err != nil {
			return err
		}
		if err := a.cfg.Update(func(c *Config) error { c.BluetoothName = btName; return nil }); err != nil {
			return err
		}
		// Do not restart bluetooth.service here: that tears down every audio link
		// and can stop dependent services. system-alias updates the live adapter,
		// while main.conf makes the name survive the next boot.
		if err := a.setBluetoothAlias(btName); err != nil {
			a.logf("Bluetooth name saved but live alias update failed: %v", err)
			return fmtErr("Bluetooth name was saved but could not be applied live; it will be retried automatically")
		}
		a.signalRefresh()
	}
	return nil
}

func (a *App) restartAudio() {
	_, _ = a.run.Run(10*time.Second, "systemctl", "restart", "openaudiohub-audio-tuning.service")
	a.requestReconcile("manual audio restart")
}
func (a *App) systemAction(action string) error {
	switch action {
	case "reboot":
		_, e := a.run.Run(3*time.Second, "systemctl", "reboot")
		return e
	case "restart-audio":
		a.restartAudio()
		return nil
	default:
		return fmtErr("unsupported system action")
	}
}

func ensureDir(p string) error       { return os.MkdirAll(filepath.Dir(p), 0755) }
func commandExists(name string) bool { _, e := exec.LookPath(name); return e == nil }
