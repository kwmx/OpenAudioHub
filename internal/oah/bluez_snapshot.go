package oah

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// bluezDevice is the subset of org.bluez.Device1 the device list needs.
//
// One ObjectManager.GetManagedObjects call returns these properties for every
// device BlueZ knows, nearby ones included. The older path ran `bluetoothctl info`
// once per device and, to bound that cost, inspected at most a handful of nearby
// devices per refresh; the rest were shown as "Nearby device" of unknown type even
// though BlueZ already had their name and class.
type bluezDevice struct {
	Addr        string
	AddressType string
	Name        string
	Alias       string
	Icon        string
	Class       uint32
	UUIDs       []string
	RSSI        int
	HasRSSI     bool
	Paired      bool
	Bonded      bool
	// HasBonded is false on BlueZ versions without the Bonded property, where a
	// session-only pairing cannot be told apart from a stored one.
	HasBonded bool
	Trusted   bool
	Connected bool
}

const (
	uuidA2DPSource = "0000110a-0000-1000-8000-00805f9b34fb"
	uuidA2DPSink   = "0000110b-0000-1000-8000-00805f9b34fb"
)

func (a *App) bluezSnapshot() (map[string]bluezDevice, error) {
	// Text output, not --json: see busctl_text.go for why JSON fails on real hubs.
	out, err := a.run.Run(4*time.Second, "busctl", "call", "org.bluez", "/",
		"org.freedesktop.DBus.ObjectManager", "GetManagedObjects")
	if err != nil {
		return nil, err
	}
	return parseManagedObjects(out)
}

// classLabel names a device from its Bluetooth Class of Device, falling back to
// BlueZ's Icon. It is only used when the device has not told us a name.
func classLabel(class uint32, icon string) string {
	major := (class >> 8) & 0x1f
	minor := (class >> 2) & 0x3f
	switch major {
	case 1:
		return "Computer"
	case 2:
		return "Phone"
	case 4:
		switch minor {
		case 1, 2:
			return "Headset"
		case 5:
			return "Speaker"
		case 6:
			return "Headphones"
		case 7:
			return "Portable audio"
		case 8:
			return "Car audio"
		case 10:
			return "Hi-Fi audio"
		case 14, 15:
			return "TV or display"
		default:
			return "Audio device"
		}
	case 5:
		return "Input device"
	case 7:
		return "Wearable"
	}
	switch icon {
	case "audio-headset":
		return "Headset"
	case "audio-headphones":
		return "Headphones"
	case "audio-card":
		return "Speaker"
	case "phone":
		return "Phone"
	case "computer":
		return "Computer"
	case "input-keyboard", "input-mouse", "input-gaming":
		return "Input device"
	}
	return ""
}

// kindFromClass guesses a display kind for a device that has not published its
// service UUIDs yet (usual before pairing). Role assignment still requires the
// real UUIDs, so this never makes a device assignable.
func kindFromClass(class uint32, icon string) string {
	switch classLabel(class, icon) {
	case "Computer", "Phone":
		return "source"
	case "Headset", "Speaker", "Headphones", "Portable audio", "Car audio", "Hi-Fi audio", "TV or display", "Audio device":
		return "output"
	}
	return "unknown"
}

func capsFromUUIDs(uuids []string) []string {
	caps := make([]string, 0, 2)
	for _, u := range uuids {
		switch strings.ToLower(u) {
		case uuidA2DPSource:
			caps = appendUnique(caps, "sends_audio")
		case uuidA2DPSink:
			caps = appendUnique(caps, "plays_audio")
		}
	}
	return caps
}

func kindFromCaps(caps []string) string {
	switch {
	case contains(caps, "sends_audio") && contains(caps, "plays_audio"):
		return "audio-bidirectional"
	case contains(caps, "sends_audio"):
		return "source"
	case contains(caps, "plays_audio"):
		return "output"
	}
	return "unknown"
}

// fallbackDeviceName is shown when no source has a real name for the device.
func fallbackDeviceName(addr string, class uint32, icon string) string {
	if l := classLabel(class, icon); l != "" {
		return l + " · " + tailAddr(addr)
	}
	return "Nearby device · " + tailAddr(addr)
}

// deviceFromBluez builds a Device from a snapshot entry. It reads only local
// files beyond the snapshot, so it is cheap enough to run for every device.
func (a *App) deviceFromBluez(bd bluezDevice) Device {
	d := Device{
		ID: strings.ReplaceAll(bd.Addr, ":", "_"), Addr: bd.Addr, Icon: bd.Icon,
		Paired: bd.Paired || bd.Bonded, Trusted: bd.Trusted, Connected: bd.Connected,
		Caps: capsFromUUIDs(bd.UUIDs), Status: "disconnected",
	}
	if bd.HasRSSI {
		d.RSSI = bd.RSSI
	}
	candidates := []string{bd.Alias, bd.Name}
	candidates = append(candidates, bluezStoredNames(bd.Addr)...)
	candidates = append(candidates, a.rememberedDeviceName(bd.Addr))
	if best := firstUsableBluetoothName(bd.Addr, candidates...); best != "" {
		d.Name = best
		// Persist names only for devices the hub keeps. Nearby devices change
		// constantly, and saving each would rewrite the file on the SD card.
		if d.Paired {
			a.rememberDeviceName(bd.Addr, best)
		}
	} else {
		d.Name = fallbackDeviceName(bd.Addr, bd.Class, bd.Icon)
		d.unnamed = true
	}
	if d.Connected {
		d.Status = "connected"
	}
	d.Reason = sessionOnlyReason(bd)
	d.Kind = kindFromCaps(d.Caps)
	if d.Kind == "unknown" {
		d.Kind = kindFromClass(bd.Class, bd.Icon)
	}
	return d
}

// hiddenNearby reports whether a discovered device should be left out of the
// list. A2DP runs over BR/EDR, and BR/EDR devices always use a public address, so
// an unpaired device seen only with a random (LE) address can never be an audio
// input or output. These are mostly phones' and watches' rotating BLE
// advertisements, which otherwise filled the list with unnamed entries.
func hiddenNearby(bd bluezDevice, assigned bool) bool {
	return !assigned && !bd.Paired && !bd.Bonded && !bd.Connected && strings.EqualFold(bd.AddressType, "random")
}

// unidentifiable reports whether a listed device gives the user nothing to act
// on: unpaired, unassigned, no name from any source, and neither a Class of
// Device nor an Icon that says what it is. BR/EDR inquiry always reports a
// Class, so such a device was only seen over LE (smart plugs, trackers) and
// cannot be an A2DP input or output. An Icon alone is kept: a dual-mode headset
// seen first over LE carries one (from its Appearance) before inquiry reports
// its Class, and hiding it meanwhile kept it out of reach for pairing.
func unidentifiable(d Device, bd bluezDevice, assigned bool) bool {
	return !assigned && !d.Paired && !d.Connected && d.unnamed && classLabel(bd.Class, bd.Icon) == "" && len(d.Caps) == 0
}

// readBluezNameCache reads a [General] Name/Alias from a BlueZ storage file.
func readBluezNameCache(path, addr string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := []string{}
	sc := bufio.NewScanner(f)
	section := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		if section != "General" {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			k = strings.TrimSpace(k)
			if (k == "Alias" || k == "Name") && usableBluetoothName(v, addr) {
				out = append(out, strings.TrimSpace(v))
			}
		}
	}
	return out
}

// bluezNameCachePaths lists where BlueZ persists names for addr: the bonded
// device's info file, and the name cache it keeps for every device whose name
// it has ever resolved, paired or not.
func bluezNameCachePaths(root, addr string) []string {
	info, _ := filepath.Glob(filepath.Join(root, "*", addr, "info"))
	cache, _ := filepath.Glob(filepath.Join(root, "*", "cache", addr))
	return append(info, cache...)
}
