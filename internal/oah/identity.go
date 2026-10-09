package oah

import (
	"os"
	"regexp"
	"strings"
	"time"
)

// bluetoothMainConf is BlueZ's daemon configuration. A variable so tests can
// point it at a temporary file.
var bluetoothMainConf = "/etc/bluetooth/main.conf"

func writeBluetoothMainConfName(name string) error {
	b, _ := os.ReadFile(bluetoothMainConf)
	return os.WriteFile(bluetoothMainConf, []byte(setMainConfName(string(b), name)), 0644)
}

func setMainConfName(s, name string) string {
	return setConfKey(s, "General", "Name", name)
}

// setConfKey sets key = val inside [section] of an INI-style file such as
// main.conf, replacing the first active or commented-out line for the key in that
// section, and adding the key (and the section) when absent. Other lines are kept
// byte for byte.
func setConfKey(s, section, key, val string) string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// [ \t] rather than \s: \s also matches newlines.
	keyRE := regexp.MustCompile(`^[ \t]*#?[ \t]*` + regexp.QuoteMeta(key) + `[ \t]*=`)
	entry := key + " = " + val + "\n"
	header, end := -1, len(lines)
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
			continue
		}
		if header >= 0 {
			end = i
			break
		}
		if strings.EqualFold(strings.Trim(t, "[]"), section) {
			header = i
		}
	}
	if header >= 0 {
		// Prefer the active line: BlueZ reads the last active value, so leaving
		// it while replacing a commented-out example would change nothing.
		commented := -1
		for i := header + 1; i < end; i++ {
			if !keyRE.MatchString(lines[i]) {
				continue
			}
			if !strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
				lines[i] = entry
				return strings.Join(lines, "")
			}
			if commented < 0 {
				commented = i
			}
		}
		if commented >= 0 {
			lines[commented] = entry
			return strings.Join(lines, "")
		}
		return strings.Join(insertLine(lines, header+1, entry), "")
	}
	if section == "General" {
		// BlueZ expects [General] first; put it at the top.
		return "[General]\n" + entry + s
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	if s != "" {
		s += "\n"
	}
	return s + "[" + section + "]\n" + entry
}

func insertLine(lines []string, at int, line string) []string {
	if at > 0 && !strings.HasSuffix(lines[at-1], "\n") {
		lines[at-1] += "\n"
	}
	out := append([]string{}, lines[:at]...)
	out = append(out, line)
	return append(out, lines[at:]...)
}

// mainConfValues returns the active (uncommented) key/value pairs of each section.
func mainConfValues(s string) map[string]map[string]string {
	res := map[string]map[string]string{}
	section := ""
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = strings.Trim(t, "[]")
			continue
		}
		if k, v, ok := strings.Cut(t, "="); ok {
			if res[section] == nil {
				res[section] = map[string]string{}
			}
			res[section][strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return res
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
