package oah

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// bluezStorageRoot is BlueZ's persistent device store. A variable so tests can
// point it at a temporary directory.
var bluezStorageRoot = "/var/lib/bluetooth"

// pairKeyWait is how long pairStoredKey waits between looks at the device store.
var pairKeyWait = 400 * time.Millisecond

// makeBondable holds the controller's bondable flag on for the duration of an
// outgoing pairing and returns a function that puts it back.
//
// The kernel downgrades a pairing to "No Bonding" when the controller is not
// bondable (net/bluetooth/hci_event.c, hci_io_capa_request_evt):
//
//	if (!hci_dev_test_flag(hdev, HCI_BONDABLE))
//		conn->auth_type &= HCI_AT_NO_BONDING_MITM;
//
// The controller still produces a link key in that case, but with the store hint
// cleared, so bluetoothd holds it in memory only. The device then reads
// "Paired: yes / Bonded: no" until the first disconnect, after which BlueZ answers
// br-connection-unknown and the UI asks to pair again. The hub switches pairable
// off at startup and whenever pairing mode ends, so every pairing started from
// the Devices page outside pairing mode was affected.
//
// BlueZ exposes no Adapter1 property for the bondable flag, so Adapter1.Pairable
// is the only lever that sets it. Unlike the source pairing window this does not
// make the hub discoverable, only pairable.
func (a *App) makeBondable() (func(), error) {
	if a.pairingActive() {
		// The source pairing window already has the adapter pairable.
		return func() {}, nil
	}
	if _, err := a.run.Run(4*time.Second, "bluetoothctl", "pairable", "on"); err != nil {
		return nil, err
	}
	return func() {
		// Pairing mode may have been switched on while this pairing ran; it owns
		// the adapter's pairable state now and turns it off itself.
		if a.pairingActive() {
			return
		}
		_, _ = a.run.Run(4*time.Second, "bluetoothctl", "pairable", "off")
	}, nil
}

// pairStoredKey waits briefly for bluetoothd to write the link key to the device
// store. The key notification arrives just before the controller reports
// authentication complete, so it is normally on disk by the time Pair returns;
// the grace period only keeps a slow write from being read as a failed pairing.
func (a *App) pairStoredKey(addr string) bool {
	for i := 0; i < 4; i++ {
		if bluezBondHasKey(addr) {
			return true
		}
		time.Sleep(pairKeyWait)
	}
	return false
}

// bluezBondHasKey reports whether BlueZ stored key material for addr.
//
// Both transports count. BR/EDR audio stores a [LinkKey]; accessories that pair
// over LE store a long-term key. A pairing that ends without either is not
// durable: BlueZ reports Paired: yes for the session and reverts to no on the
// first disconnect.
func bluezBondHasKey(addr string) bool {
	return bluezBondHasKeyIn(bluezStorageRoot, addr)
}

// bondKeySections are the BlueZ device-store sections that hold key material,
// lower-cased for comparison.
var bondKeySections = map[string]bool{
	"linkkey":               true,
	"longtermkey":           true,
	"peripherallongtermkey": true,
	"slavelongtermkey":      true,
}

func bluezBondHasKeyIn(root, addr string) bool {
	addr = cleanAddr(addr)
	if addr == "" {
		return false
	}
	paths, _ := filepath.Glob(filepath.Join(root, "*", addr, "info"))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		inKeySection := false
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "[") {
				inKeySection = bondKeySections[strings.ToLower(strings.Trim(line, "[]"))]
				continue
			}
			if inKeySection && strings.HasPrefix(line, "Key=") {
				_ = f.Close()
				return true
			}
		}
		_ = f.Close()
	}
	return false
}

// bluezDeviceStored reports whether BlueZ keeps a device record (an info file)
// for addr. A device that was only seen in a scan has none; BlueZ creates it on
// pairing or trusting, and keeps it after an authentication failure even though
// it drops the key.
func bluezDeviceStored(addr string) bool {
	addr = cleanAddr(addr)
	if addr == "" {
		return false
	}
	paths, _ := filepath.Glob(filepath.Join(bluezStorageRoot, "*", addr, "info"))
	return len(paths) > 0
}

// lostBondReason explains a device BlueZ remembers but holds no key for.
//
// A device record without a key is a different situation from a device you have
// not paired yet: BlueZ keeps the device object after an authentication failure
// but drops the key, so it appears in the list and has been used before while
// reporting Paired: no. Telling the user "not paired" then reads as a bug.
func lostBondReason(addr string, paired bool) string {
	if paired || !bluezDeviceStored(addr) || bluezBondHasKey(addr) {
		return ""
	}
	return "This device was paired before, but its stored key is gone. Pair it again to restore the link."
}

// sessionOnlyReason explains a pairing BlueZ reports as active but did not store
// (Paired: yes, Bonded: no). It is only known on BlueZ versions that expose the
// Bonded property; without it there is no way to tell.
func sessionOnlyReason(bd bluezDevice) string {
	if bd.Paired && bd.HasBonded && !bd.Bonded {
		return "Paired for this session only: no key was stored, so the hub forgets this device when it disconnects. Forget it, then pair it again."
	}
	return ""
}
