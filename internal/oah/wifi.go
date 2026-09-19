package oah

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *App) wifiState() WiFiState {
	cfg := a.cfg.Get()
	ifc := cfg.WiFi.Interface
	w := WiFiState{Interface: ifc, Networks: make([]WiFiNetwork, 0)}
	out, _ := a.run.Run(4*time.Second, "/usr/sbin/iw", "dev", ifc, "link")
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(s, "Connected to "):
			fs := strings.Fields(s)
			if len(fs) >= 3 {
				w.BSSID = fs[2]
			}
		case strings.HasPrefix(s, "SSID: "):
			w.SSID = strings.TrimSpace(strings.TrimPrefix(s, "SSID:"))
		case strings.HasPrefix(s, "freq: "):
			f := strings.TrimSpace(strings.TrimPrefix(s, "freq:"))
			ff, _ := strconv.ParseFloat(f, 64)
			w.Freq = int(ff)
		case strings.HasPrefix(s, "signal: "):
			fs := strings.Fields(strings.TrimPrefix(s, "signal:"))
			if len(fs) > 0 {
				r, _ := strconv.ParseFloat(fs[0], 64)
				w.RSSI = int(r)
			}
		}
	}
	w.Band = bandForFreq(w.Freq)
	w.Channel = channelForFreq(w.Freq)
	if iface, err := net.InterfaceByName(ifc); err == nil {
		if addrs, err := iface.Addrs(); err == nil {
			for _, x := range addrs {
				if ip, _, e := net.ParseCIDR(x.String()); e == nil && ip.To4() != nil {
					w.IP = ip.String()
					break
				}
			}
		}
	}
	a.mu.RLock()
	if a.networkApply != nil {
		cp := *a.networkApply
		w.Applying = &cp
	}
	w.Networks = append([]WiFiNetwork{}, a.wifiNetworks...)
	a.mu.RUnlock()
	return w
}

func (a *App) scanWiFi() []WiFiNetwork {
	ifc := a.cfg.Get().WiFi.Interface
	out, err := a.run.Run(12*time.Second, "/usr/sbin/iw", "dev", ifc, "scan")
	if err != nil {
		return make([]WiFiNetwork, 0)
	}
	list := make([]WiFiNetwork, 0)
	var cur WiFiNetwork
	flush := func() {
		if cur.SSID != "" {
			cur.Band = bandForFreq(cur.Freq)
			cur.Channel = channelForFreq(cur.Freq)
			list = append(list, cur)
		}
		cur = WiFiNetwork{}
	}
	reBSS := regexp.MustCompile(`^BSS\s+([^\s(]+)`)
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		if m := reBSS.FindStringSubmatch(s); len(m) == 2 {
			flush()
			cur.BSSID = m[1]
			continue
		}
		if strings.HasPrefix(s, "freq:") {
			cur.Freq = atoiLoose(strings.TrimSpace(strings.TrimPrefix(s, "freq:")))
		}
		if strings.HasPrefix(s, "signal:") {
			fs := strings.Fields(strings.TrimPrefix(s, "signal:"))
			if len(fs) > 0 {
				f, _ := strconv.ParseFloat(fs[0], 64)
				cur.RSSI = int(f)
			}
		}
		if strings.HasPrefix(s, "SSID:") {
			cur.SSID = strings.TrimSpace(strings.TrimPrefix(s, "SSID:"))
		}
		if strings.Contains(s, "RSN:") || strings.Contains(s, "WPA:") {
			cur.Secure = true
		}
	}
	flush()
	// Keep strongest BSSID per SSID/band to keep the UI concise while retaining multi-router networks.
	best := map[string]WiFiNetwork{}
	for _, n := range list {
		k := n.SSID + "|" + n.Band
		if old, ok := best[k]; !ok || n.RSSI > old.RSSI {
			best[k] = n
		}
	}
	list = list[:0]
	for _, n := range best {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].RSSI != list[j].RSSI {
			return list[i].RSSI > list[j].RSSI
		}
		return list[i].SSID < list[j].SSID
	})
	return list
}

func bandForFreq(f int) string {
	if f >= 4900 {
		return "5 GHz"
	}
	if f > 0 {
		return "2.4 GHz"
	}
	return "—"
}
func channelForFreq(f int) int {
	if f == 2484 {
		return 14
	}
	if f >= 2412 && f <= 2472 {
		return (f - 2407) / 5
	}
	if f >= 5000 {
		return (f - 5000) / 5
	}
	return 0
}

func (a *App) startNetworkApply(ssid, password, bssid, band string) (*NetworkApply, error) {
	if strings.TrimSpace(ssid) == "" {
		return nil, fmtErr("SSID is required")
	}
	if len([]byte(ssid)) > 32 {
		return nil, fmtErr("SSID is longer than the Wi-Fi standard allows")
	}
	if len(password) > 63 {
		return nil, fmtErr("Wi-Fi password is too long")
	}
	if password != "" && len(password) < 8 {
		return nil, fmtErr("Wi-Fi password must be at least 8 characters")
	}
	if bssid != "" && cleanAddr(bssid) == "" {
		return nil, fmtErr("invalid access point address")
	}
	if band != "" && band != "5GHz" && band != "2.4GHz" {
		return nil, fmtErr("invalid Wi-Fi band")
	}
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	backup := filepath.Join("/var/lib/openaudiohub/netplan-backups", id)
	if err := os.MkdirAll(backup, 0700); err != nil {
		return nil, err
	}
	matches, _ := filepath.Glob("/etc/netplan/*.yaml")
	matches2, _ := filepath.Glob("/etc/netplan/*.yml")
	matches = append(matches, matches2...)
	for _, p := range matches {
		b, e := os.ReadFile(p)
		if e == nil {
			_ = os.WriteFile(filepath.Join(backup, filepath.Base(p)), b, 0600)
		}
	}
	payload := map[string]any{"interface": a.cfg.Get().WiFi.Interface, "ssid": ssid, "password": password, "bssid": bssid, "band": band}
	raw, _ := json.Marshal(payload)
	payloadPath := filepath.Join("/run/openaudiohub", "network-"+id+".json")
	if err := os.MkdirAll("/run/openaudiohub", 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(payloadPath, raw, 0600); err != nil {
		return nil, err
	}
	defer os.Remove(payloadPath)
	if _, err := a.run.Run(10*time.Second, "/usr/local/lib/openaudiohub/netplan_wifi.py", "set", "--payload", payloadPath); err != nil {
		return nil, err
	}
	if _, err := a.run.Run(10*time.Second, "netplan", "generate"); err != nil {
		return nil, err
	}
	// Rollback is scheduled before the disruptive apply. Confirming cancels it.
	rollbackScript := fmt.Sprintf("sleep 60; rm -f /etc/netplan/*.yaml /etc/netplan/*.yml; cp %s/* /etc/netplan/ 2>/dev/null || true; netplan generate && netplan apply", quoteShell(backup))
	unit := "openaudiohub-netplan-rollback-" + id
	if _, err := a.run.Run(5*time.Second, "systemd-run", "--unit", unit, "/bin/bash", "-lc", rollbackScript); err != nil {
		return nil, err
	}
	apply := &NetworkApply{ID: id, State: "connecting", SSID: ssid, StartedAt: time.Now(), Message: "Switching networks; rollback is armed for 60 seconds."}
	a.mu.Lock()
	a.networkApply = apply
	a.mu.Unlock()
	a.signalRefresh()
	go func() {
		_, err := a.run.Run(25*time.Second, "netplan", "apply")
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.networkApply == nil || a.networkApply.ID != id {
			return
		}
		if err != nil {
			a.networkApply.State = "rolledback"
			a.logf("network apply failed: %v", err)
			a.networkApply.Message = "Network switch failed. The previous configuration will be restored automatically."
		} else {
			a.networkApply.State = "verifying"
			a.networkApply.Message = "Reconnect to OpenAudioHub and confirm this network before rollback."
		}
		a.signalRefresh()
	}()
	return apply, nil
}

func (a *App) confirmNetworkApply(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.networkApply == nil || a.networkApply.ID != id {
		return fmtErr("network apply not found")
	}
	unit := "openaudiohub-netplan-rollback-" + id + ".service"
	_, _ = a.run.Run(4*time.Second, "systemctl", "stop", unit)
	a.networkApply.State = "ok"
	a.networkApply.Message = "Network confirmed; rollback cancelled."
	return nil
}
