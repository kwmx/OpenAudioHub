package oah

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var macLikeNameRE = regexp.MustCompile(`(?i)^[0-9a-f]{2}([:_-]?[0-9a-f]{2}){5}$`)

func deviceNameCachePath(configPath string) string {
	if v := strings.TrimSpace(os.Getenv("OPENAUDIOHUB_DEVICE_NAME_CACHE")); v != "" {
		return v
	}
	if strings.HasPrefix(filepath.Clean(configPath), "/etc/openaudiohub/") {
		return "/var/lib/openaudiohub/device-names.json"
	}
	return filepath.Join(filepath.Dir(configPath), "device-names.json")
}

func loadDeviceNameCache(path string) map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	out := map[string]string{}
	for addr, name := range m {
		addr = cleanAddr(addr)
		if addr == "" || !usableBluetoothName(name, addr) {
			continue
		}
		out[addr] = strings.TrimSpace(name)
	}
	return out
}

func (a *App) rememberedDeviceName(addr string) string {
	addr = cleanAddr(addr)
	if addr == "" {
		return ""
	}
	a.deviceNamesMu.Lock()
	defer a.deviceNamesMu.Unlock()
	return a.deviceNames[addr]
}

func (a *App) rememberDeviceName(addr, name string) {
	addr = cleanAddr(addr)
	name = strings.TrimSpace(name)
	if addr == "" || !usableBluetoothName(name, addr) {
		return
	}
	a.deviceNamesMu.Lock()
	if a.deviceNames == nil {
		a.deviceNames = map[string]string{}
	}
	if a.deviceNames[addr] == name {
		a.deviceNamesMu.Unlock()
		return
	}
	a.deviceNames[addr] = name
	copyMap := make(map[string]string, len(a.deviceNames))
	for k, v := range a.deviceNames {
		copyMap[k] = v
	}
	path := a.deviceNamesPath
	a.deviceNamesMu.Unlock()

	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	b, err := json.MarshalIndent(copyMap, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func usableBluetoothName(name, addr string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	low := strings.ToLower(name)
	if low == "unknown" || low == "unknown device" || low == "n/a" || low == "none" || strings.HasPrefix(low, "unnamed device") {
		return false
	}
	if macLikeNameRE.MatchString(name) {
		return false
	}
	compact := strings.NewReplacer(":", "", "-", "", "_", "", " ", "").Replace(strings.ToUpper(name))
	addrCompact := strings.ReplaceAll(strings.ToUpper(cleanAddr(addr)), ":", "")
	if addrCompact != "" && compact == addrCompact {
		return false
	}
	if strings.EqualFold(name, addr) || strings.EqualFold(name, strings.ReplaceAll(addr, ":", "-")) || strings.EqualFold(name, strings.ReplaceAll(addr, ":", "_")) {
		return false
	}
	return true
}

// bluezStoredNames reads BlueZ's persistent Device1 cache. It is a fallback
// only: discovery/Device1 properties are preferred, but the cache often keeps
// the original remote name after a device's Alias later degrades to a MAC-like
// value.
func bluezStoredNames(addr string) []string {
	addr = cleanAddr(addr)
	if addr == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join("/var/lib/bluetooth", "*", addr, "info"))
	out := []string{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
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
				if (k == "Alias" || k == "Name") && usableBluetoothName(v, addr) {
					out = append(out, strings.TrimSpace(v))
				}
			}
		}
		_ = f.Close()
	}
	return out
}

func firstUsableBluetoothName(addr string, candidates ...string) string {
	for _, name := range candidates {
		if usableBluetoothName(name, addr) {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// nearbyInfoTTL and nearbyInfoBudget bound how often a discovered-but-unpaired
// device is inspected with bluetoothctl. BlueZ persists Icon and UUIDs for
// discovered devices, so a cached lookup still yields a real device type while
// keeping the per-build subprocess count bounded on a 1 GB board.
const (
	nearbyInfoTTL    = 90 * time.Second
	nearbyInfoBudget = 8
)

type deviceInfoEntry struct {
	d  Device
	at time.Time
}

func (a *App) cachedDeviceInfo(addr string, ttl time.Duration) (Device, bool) {
	addr = cleanAddr(addr)
	if addr == "" {
		return Device{}, false
	}
	a.deviceInfoMu.Lock()
	defer a.deviceInfoMu.Unlock()
	e, ok := a.deviceInfo[addr]
	if !ok || time.Since(e.at) > ttl {
		return Device{}, false
	}
	return e.d, true
}

func (a *App) rememberDeviceInfo(addr string, d Device) {
	addr = cleanAddr(addr)
	if addr == "" {
		return
	}
	a.deviceInfoMu.Lock()
	defer a.deviceInfoMu.Unlock()
	if a.deviceInfo == nil {
		a.deviceInfo = map[string]deviceInfoEntry{}
	}
	a.deviceInfo[addr] = deviceInfoEntry{d: d, at: time.Now()}
}
