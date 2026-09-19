package oah

import (
	"os"
	"regexp"
	"strings"
	"time"
)

func writeBluetoothMainConfName(name string) error {
	p := "/etc/bluetooth/main.conf"
	b, _ := os.ReadFile(p)
	s := string(b)
	if !strings.Contains(s, "[General]") {
		s = "[General]\n" + s
	}
	re := regexp.MustCompile(`(?m)^\s*#?\s*Name\s*=.*$`)
	if re.MatchString(s) {
		s = re.ReplaceAllStringFunc(s, func(string) string { return "Name = " + name })
	} else {
		s = strings.Replace(s, "[General]", "[General]\nName = "+name, 1)
	}
	return os.WriteFile(p, []byte(s), 0644)
}

func (a *App) setBluetoothAlias(name string) error {
	_, err := a.run.Run(4*time.Second, "bluetoothctl", "system-alias", name)
	return err
}

func (a *App) syncBluetoothIdentity() {
	name := strings.TrimSpace(a.cfg.Get().BluetoothName)
	if name == "" {
		return
	}
	// BlueZ can come up a few seconds after the daemon during boot. Retrying here
	// also repairs stale aliases left by manual bluetoothctl prototyping.
	var lastErr error
	for i := 0; i < 20; i++ {
		if err := a.setBluetoothAlias(name); err == nil {
			if i > 0 {
				a.logf("Bluetooth identity synchronized as %q", name)
			}
			a.signalRefresh()
			return
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	if lastErr != nil {
		a.logf("Bluetooth identity sync deferred: %v", lastErr)
	}
}
