package oah

import (
	"fmt"
	"strings"
)

func (a *App) buildState(includeDiag bool) State {
	a.stateBuildMu.Lock()
	defer a.stateBuildMu.Unlock()
	c := a.cfg.Get()
	// Gather transports once and share them: each entry costs a bluetoothctl
	// subprocess pair, and this runs on every state build.
	transports := a.listTransports()
	devices, btErr := a.listBluetoothDevices(transports)
	wifi := a.wifiState()
	sys := a.systemState()
	audioErr := a.audioReady()
	xruns := 0
	st := State{Revision: c.Revision, Devices: devices, Slots: c.Slots, Mixer: c.Mixer, WiFi: wifi, Audio: c.Audio, System: sys}
	st.DelayReport = a.delayReport(transports)
	st.Inputs = InputCapacity{Max: maxInputs, Proven: provenInputs}
	st.ReceiverInputs = bluealsaReceiverLabel(c)
	a.mu.RLock()
	st.Pairing = a.pairing
	a.mu.RUnlock()
	h := Health{}
	if wifi.SSID == "" {
		h.WiFi.State = "error"
		h.WiFi.Value = "Disconnected"
	} else {
		h.WiFi.State = "connected"
		h.WiFi.Value = fmt.Sprintf("%s · %s", wifi.SSID, wifi.Band)
		h.WiFi.Warning = wifi.Band == "2.4 GHz"
		if h.WiFi.Warning {
			h.WiFi.State = "warning"
		}
	}
	activeIn, activeOut := 0, 0
	for _, d := range devices {
		if !d.Connected {
			continue
		}
		if strings.HasPrefix(d.Role, "in") {
			activeIn++
		}
		if strings.HasPrefix(d.Role, "out") {
			activeOut++
		}
	}
	h.Bluetooth.ActiveSources = activeIn
	h.Bluetooth.ActiveOutputs = activeOut
	switch {
	case btErr != nil:
		h.Bluetooth.State = "error"
		h.Bluetooth.Value = "Bluetooth unavailable"
	case activeOut == 0:
		h.Bluetooth.State = "warning"
		h.Bluetooth.Value = "No output"
	default:
		h.Bluetooth.State = "connected"
		h.Bluetooth.Value = fmt.Sprintf("%d input · %d output", activeIn, activeOut)
	}
	h.Audio.Xruns = xruns
	switch {
	case audioErr != nil && activeIn+activeOut > 0:
		// Bluetooth transports may continue to flow while the local PipeWire
		// control socket is recovering. Do not claim that audio is unavailable
		// when the transport graph is visibly active.
		h.Audio.State = "warning"
		h.Audio.Value = "Audio controls recovering"
	case audioErr != nil:
		h.Audio.State = "error"
		h.Audio.Value = "Audio graph unavailable"
	case xruns > 0:
		h.Audio.State = "warning"
		h.Audio.Value = fmt.Sprintf("%d Hz · q%d · %d errors", c.Audio.PreferredRate, c.Audio.Quantum, xruns)
	default:
		h.Audio.State = "connected"
		h.Audio.Value = fmt.Sprintf("%d Hz · q%d", c.Audio.PreferredRate, c.Audio.Quantum)
	}
	h.System.State = "connected"
	h.System.Value = "Healthy"
	st.Health = h
	if includeDiag {
		st.Diagnostics = a.diagnostics()
	}
	return st
}
