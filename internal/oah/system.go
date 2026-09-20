package oah

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func (a *App) systemState() SystemState {
	host, _ := os.Hostname()
	c := a.cfg.Get()
	return SystemState{Hostname: host, MDNS: host + ".local", BTName: c.BluetoothName, Version: a.version, OS: osRelease(), Time: time.Now().Format(time.RFC3339), Uptime: uptimeHuman()}
}

func osRelease() string {
	b, _ := os.ReadFile("/etc/os-release")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(l, "PRETTY_NAME="), "\"")
		}
	}
	return runtime.GOOS
}
func uptimeHuman() string {
	b, _ := os.ReadFile("/proc/uptime")
	fs := strings.Fields(string(b))
	if len(fs) == 0 {
		return "—"
	}
	f, _ := strconv.ParseFloat(fs[0], 64)
	d := time.Duration(f) * time.Second
	days := int(d / (24 * time.Hour))
	d %= 24 * time.Hour
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

func (a *App) diagnostics() Diagnostics {
	xruns, _ := a.audioXRuns()
	d := Diagnostics{CPUPercent: cpuUsageSample(), MemPercent: memUsage(), TempC: temperature(), Xruns: xruns, Transports: a.listTransports(), Services: make([]ServiceInfo, 0), Logs: make([]string, 0)}
	services := []string{"bluetooth", "openaudiohub-bt-agent", "openaudiohub-bluealsa", "openaudiohub-bluealsa-bridge", "openaudiohubd", "rtkit-daemon", "avahi-daemon"}
	for _, s := range services {
		out, _ := a.run.Run(2*time.Second, "systemctl", "is-active", s)
		d.Services = append(d.Services, ServiceInfo{Name: s, State: strings.TrimSpace(out)})
	}
	for _, s := range []string{"pipewire", "pipewire-pulse", "wireplumber"} {
		out, _ := a.audioUserCommand(2*time.Second, "systemctl", "--user", "is-active", s+".service")
		d.Services = append(d.Services, ServiceInfo{Name: s + " (user)", State: strings.TrimSpace(out)})
	}
	a.logMu.Lock()
	if len(a.logs) > 80 {
		d.Logs = append([]string{}, a.logs[len(a.logs)-80:]...)
	} else {
		d.Logs = append([]string{}, a.logs...)
	}
	a.logMu.Unlock()
	// Diagnostics are loaded only on demand, so include recent service-level audio
	// and Bluetooth events. This is essential for distinguishing a BlueALSA PCM
	// underrun from an AVDTP/SEP negotiation failure without polling journals in
	// the normal dashboard path.
	if out, err := a.run.Run(4*time.Second, "journalctl", "-b", "--no-pager", "-n", "80",
		"-u", "openaudiohubd.service", "-u", "openaudiohub-update.service", "-u", "openaudiohub-bluealsa.service",
		"-u", "openaudiohub-bluealsa-bridge.service", "-u", "bluetooth.service"); err == nil {
		d.Logs = append(d.Logs, "--- recent service journal ---")
		d.Logs = append(d.Logs, strings.Split(strings.TrimSpace(out), "\n")...)
	}
	return d
}

func memUsage() float64 {
	f, e := os.Open("/proc/meminfo")
	if e != nil {
		return 0
	}
	defer f.Close()
	vals := map[string]float64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 2 {
			v, _ := strconv.ParseFloat(fs[1], 64)
			vals[strings.TrimSuffix(fs[0], ":")] = v
		}
	}
	total := vals["MemTotal"]
	avail := vals["MemAvailable"]
	if total <= 0 {
		return 0
	}
	return (total - avail) / total * 100
}
func temperature() float64 {
	paths := []string{"/sys/class/thermal/thermal_zone0/temp", "/sys/class/hwmon/hwmon0/temp1_input"}
	for _, p := range paths {
		b, e := os.ReadFile(p)
		if e == nil {
			v, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
			if v > 1000 {
				v /= 1000
			}
			return v
		}
	}
	return 0
}
func cpuUsageSample() float64 {
	read := func() (float64, float64) {
		b, _ := os.ReadFile("/proc/stat")
		fs := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
		if len(fs) < 5 {
			return 0, 0
		}
		var total float64
		for _, x := range fs[1:] {
			v, _ := strconv.ParseFloat(x, 64)
			total += v
		}
		idle, _ := strconv.ParseFloat(fs[4], 64)
		return total, idle
	}
	t1, i1 := read()
	time.Sleep(100 * time.Millisecond)
	t2, i2 := read()
	if t2 <= t1 {
		return 0
	}
	return (1 - (i2-i1)/(t2-t1)) * 100
}
