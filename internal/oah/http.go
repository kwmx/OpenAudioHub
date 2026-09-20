package oah

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *App) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/session", a.handleLogin)
	mux.HandleFunc("DELETE /api/session", a.handleLogout)
	mux.HandleFunc("GET /api/public", a.handlePublic)
	mux.HandleFunc("GET /api/health", a.handleHealth)
	mux.HandleFunc("GET /api/state", a.requireAuth(a.handleState))
	mux.HandleFunc("GET /events", a.requireAuth(a.handleEvents))

	mux.HandleFunc("POST /api/bluetooth/scan", a.requireAuth(a.handleBTScan))
	mux.HandleFunc("POST /api/bluetooth/pairing", a.requireAuth(a.handleBTPairing))
	mux.HandleFunc("POST /api/bluetooth/action", a.requireAuth(a.handleBTAction))
	mux.HandleFunc("POST /api/bluetooth/role", a.requireAuth(a.handleBTRole))
	mux.HandleFunc("POST /api/bluetooth/auto", a.requireAuth(a.handleBTAuto))
	mux.HandleFunc("POST /api/bluetooth/volume", a.requireAuth(a.handleBTVolume))

	mux.HandleFunc("POST /api/mixer", a.requireAuth(a.handleMixer))
	mux.HandleFunc("POST /api/audio", a.requireAuth(a.handleAudio))
	mux.HandleFunc("POST /api/audio/delay", a.requireAuth(a.handleAudioDelay))

	mux.HandleFunc("POST /api/network/scan", a.requireAuth(a.handleNetworkScan))
	mux.HandleFunc("POST /api/network/join", a.requireAuth(a.handleNetworkJoin))
	mux.HandleFunc("POST /api/network/confirm", a.requireAuth(a.handleNetworkConfirm))

	mux.HandleFunc("POST /api/system/identity", a.requireAuth(a.handleIdentity))
	mux.HandleFunc("POST /api/system/action", a.requireAuth(a.handleSystemAction))
	mux.HandleFunc("POST /api/system/password", a.requireAuth(a.handlePasswordChange))
	mux.HandleFunc("GET /api/system/backup", a.requireAuth(a.handleBackup))
	mux.HandleFunc("POST /api/system/restore", a.requireAuth(a.handleRestore))
	mux.HandleFunc("GET /api/diagnostics", a.requireAuth(a.handleDiagnostics))

	webRoot := os.Getenv("OPENAUDIOHUB_WEB")
	if webRoot == "" {
		if fileExists("web/dist/index.html") {
			webRoot = "web/dist"
		} else {
			webRoot = "/usr/share/openaudiohub/web"
		}
	}
	fs := http.FileServer(http.Dir(webRoot))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/events" {
			http.NotFound(w, r)
			return
		}
		p := filepath.Join(webRoot, filepath.Clean(r.URL.Path))
		if r.URL.Path != "/" {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, filepath.Join(webRoot, "index.html"))
	})
}

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	s := a.systemState()
	writeJSON(w, 200, map[string]any{"hostname": s.Hostname, "mdns": s.MDNS, "btName": s.BTName, "version": s.Version, "authenticated": a.authenticated(r)})
}
func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Deliberately cheap and non-sensitive: detailed component health lives behind
	// authentication in /api/state and /api/diagnostics.
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": a.version})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	// PBKDF2 is intentionally expensive. Serialize login verification so a noisy
	// client cannot starve the 1 GB hub with parallel password checks.
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	var q struct {
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if !verifyPassword(a.cfg.Get(), q.Password) {
		time.Sleep(350 * time.Millisecond)
		writeJSON(w, 401, map[string]string{"error": "Incorrect password"})
		return
	}
	// Normal sessions last a week. "Stay signed in" is deliberately long for a
	// local appliance and survives daemon restarts/reboots because the cookie is
	// stateless and HMAC-signed.
	exp := time.Now().Add(7 * 24 * time.Hour)
	maxAge := 7 * 24 * 3600
	if q.Remember {
		exp = time.Now().Add(90 * 24 * time.Hour)
		maxAge = 90 * 24 * 3600
	}
	token := mintSessionToken(a.cfg.Get(), exp)
	http.SetCookie(w, &http.Cookie{Name: "oah_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Expires: exp})
	writeJSON(w, 200, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "oah_session", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(204)
}
func (a *App) handleState(w http.ResponseWriter, r *http.Request) {
	if st, ok := a.cachedState(); ok {
		writeJSON(w, 200, st)
		return
	}
	st := a.buildState(false)
	a.cacheState(st)
	writeJSON(w, 200, st)
}
func (a *App) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.buildState(true))
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan []byte, 4)
	a.sseMu.Lock()
	a.sse[ch] = struct{}{}
	a.sseMu.Unlock()
	defer func() { a.sseMu.Lock(); delete(a.sse, ch); a.sseMu.Unlock() }()
	st, ok := a.cachedState()
	if !ok {
		st = a.buildState(false)
		a.cacheState(st)
	}
	initial, _ := json.Marshal(st)
	fmt.Fprintf(w, "event: state\ndata: %s\n\n", initial)
	f.Flush()
	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	for {
		select {
		case b := <-ch:
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", b)
			f.Flush()
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
			f.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (a *App) handleBTScan(w http.ResponseWriter, r *http.Request) {
	a.scanBluetooth()
	writeJSON(w, 202, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}
func (a *App) handleBTPairing(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.setPairing(q.Enabled); err != nil {
		a.writeProblem(w, 503, "Bluetooth is temporarily unavailable. Try again in a moment.", "set pairing mode", err)
		return
	}
	a.signalRefresh()
	writeJSON(w, 200, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}
func (a *App) handleBTAction(w http.ResponseWriter, r *http.Request) {
	var q struct{ Addr, Action string }
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	q.Addr = cleanAddr(q.Addr)
	if q.Addr == "" {
		writeJSON(w, 400, map[string]string{"error": "invalid address"})
		return
	}
	if err := a.btAction(q.Addr, q.Action); err != nil {
		// Actionable validation must reach the user; only internal failures are
		// replaced by a generic message with a reference for the journal.
		if isUserErr(err) {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		msg := "Bluetooth did not complete that action. Try again, or check Diagnostics."
		if q.Action == "forget" {
			msg = "The device was unassigned but not removed. Try Forget again."
		}
		a.writeProblem(w, 409, msg, "bluetooth "+q.Action+" "+q.Addr, err)
		return
	}
	a.signalRefresh()
	writeJSON(w, 200, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}
func (a *App) handleBTRole(w http.ResponseWriter, r *http.Request) {
	var q struct{ Addr, Role string }
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	q.Addr = cleanAddr(q.Addr)
	if q.Addr == "" {
		writeJSON(w, 400, map[string]string{"error": "invalid address"})
		return
	}

	oldRole := a.roleFor(q.Addr)
	// Capture the previous occupant so replacing a slot cannot leave a stale
	// connected source consuming one of the two A2DP sink endpoints.
	oldOccupant := ""
	before := a.cfg.Get()
	switch q.Role {
	case "in1":
		if len(before.Slots.Inputs) > 0 {
			oldOccupant = before.Slots.Inputs[0]
		}
	case "in2":
		if len(before.Slots.Inputs) > 1 {
			oldOccupant = before.Slots.Inputs[1]
		}
	case "out1", "out2":
		idx := atoiLoose(strings.TrimPrefix(q.Role, "out")) - 1
		if idx >= 0 && idx < len(before.Slots.Outputs) {
			oldOccupant = before.Slots.Outputs[idx]
		}
	}

	a.btOpsMu.Lock()
	err := a.setRole(q.Addr, q.Role)
	if err == nil && oldRole != "" && oldRole != q.Role {
		// A device removed from or moved between slots must release its current SEP
		// before the new route is staged. Otherwise an unassigned stale AVDTP
		// transport can consume one of the hub's two input endpoints.
		_, _ = a.run.Run(6*time.Second, "bluetoothctl", "disconnect", q.Addr)
	}
	if err == nil && oldOccupant != "" && !strings.EqualFold(oldOccupant, q.Addr) {
		_, _ = a.run.Run(6*time.Second, "bluetoothctl", "disconnect", oldOccupant)
	}
	a.btOpsMu.Unlock()
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	reason := "device role changed"
	if strings.HasPrefix(oldRole, "in") || strings.HasPrefix(q.Role, "in") {
		reason = "input roles changed"
	}
	a.resetConnectState()
	a.requestReconcile(reason)
	writeJSON(w, 202, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}

func (a *App) handleBTAuto(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Addr    string `json:"addr"`
		Enabled bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.setAutoConnect(q.Addr, q.Enabled); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if q.Enabled {
		a.requestReconcile("auto-connect enabled")
	}
	a.signalRefresh()
	writeJSON(w, 200, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}

func (a *App) handleBTVolume(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Addr   string `json:"addr"`
		Volume int    `json:"volume"`
	}
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.setTransportVolume(q.Addr, q.Volume); err != nil {
		a.writeProblem(w, 409, "Bluetooth volume could not be changed for that device.", "set Bluetooth transport volume", err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}

func (a *App) handleMixer(w http.ResponseWriter, r *http.Request) {
	var q MixerConfig
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.updateMixer(q); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	a.signalRefresh()
	writeJSON(w, 200, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}

// handleAudioDelay applies only the secondary receiver's advertised delay. It is
// deliberately separate from handleAudio so Apply/Reset cannot restart the audio
// graph or disturb unsaved edits to other audio settings.
//
// It returns 202 with the freshly computed DelayReport. That report is normally
// "pending": the value can only be confirmed once the source reconnects and BlueZ
// exposes the acquired transport, so the caller must not treat this response as
// proof that the report took effect.
func (a *App) handleAudioDelay(w http.ResponseWriter, r *http.Request) {
	var q struct {
		MS int `json:"ms"`
	}
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.applyReceiverDelay(q.MS); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	a.signalRefresh()
	rep := a.delayReport(a.listTransports())
	writeJSON(w, 202, map[string]any{"ok": true, "revision": a.cfg.Get().Revision, "delayReport": rep})
}

func (a *App) handleAudio(w http.ResponseWriter, r *http.Request) {
	var q AudioConfig
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.applyAudioConfig(q); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}

func (a *App) handleNetworkScan(w http.ResponseWriter, r *http.Request) {
	list := a.scanWiFi()
	a.mu.Lock()
	a.wifiNetworks = list
	a.mu.Unlock()
	a.signalRefresh()
	writeJSON(w, 200, map[string]any{"networks": list})
}
func (a *App) handleNetworkJoin(w http.ResponseWriter, r *http.Request) {
	var q struct{ SSID, Password, BSSID, Band string }
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	ap, err := a.startNetworkApply(q.SSID, q.Password, q.BSSID, q.Band)
	if err != nil {
		// Input validation errors are safe to return; command/system failures stay in diagnostics.
		msg := err.Error()
		if strings.Contains(msg, "netplan") || strings.Contains(msg, "systemd-run") || strings.Contains(msg, "exit status") || strings.Contains(msg, "timed out") {
			a.writeProblem(w, 500, "The network change could not be prepared safely. Existing Wi-Fi was left in place.", "network join", err)
			return
		}
		writeJSON(w, 400, map[string]string{"error": msg})
		return
	}
	writeJSON(w, 202, ap)
}
func (a *App) handleNetworkConfirm(w http.ResponseWriter, r *http.Request) {
	var q struct{ ID string }
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.confirmNetworkApply(q.ID); err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	a.signalRefresh()
	writeJSON(w, 200, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}

func (a *App) handleIdentity(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Hostname string `json:"hostname"`
		BTName   string `json:"btName"`
	}
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.applyIdentity(q.Hostname, q.BTName); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "hostnamectl") || strings.Contains(msg, "/etc/") || strings.Contains(msg, "exit status") || strings.Contains(msg, "timed out") {
			a.writeProblem(w, 500, "Identity could not be updated. Existing settings were kept where possible.", "update identity", err)
			return
		}
		writeJSON(w, 400, map[string]string{"error": msg})
		return
	}
	a.signalRefresh()
	writeJSON(w, 202, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}
func (a *App) handleSystemAction(w http.ResponseWriter, r *http.Request) {
	var q struct{ Action string }
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.systemAction(q.Action); err != nil {
		a.writeProblem(w, 500, "That system action failed.", "system action "+q.Action, err)
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}
func (a *App) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	var q struct{ Current, New string }
	if err := decodeJSON(r, &q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if !verifyPassword(a.cfg.Get(), q.Current) {
		writeJSON(w, 403, map[string]string{"error": "current password is incorrect"})
		return
	}
	if err := a.cfg.Update(func(c *Config) error { return setPassword(c, q.New) }); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	// Session signatures are derived from PasswordHash, so changing the password
	// automatically invalidates all existing cookies without an in-memory list.
	writeJSON(w, 200, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}

func (a *App) handleBackup(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		f, _ := zw.Create(name)
		_, _ = f.Write(b)
	}
	add("config.json", a.cfg.path)
	add("audio.json", "/etc/openaudiohub/audio.json")
	add("wireplumber.conf", "/etc/openaudiohub/wireplumber.conf")
	_ = zw.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=openaudiohub-backup.zip")
	w.Write(buf.Bytes())
}
func (a *App) handleRestore(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	tmp, err := os.CreateTemp("", "oah-restore-*.zip")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, r.Body)
	_ = tmp.Close()
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	zr, err := zip.OpenReader(tmp.Name())
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid backup"})
		return
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "config.json" && f.Name != "audio.json" {
			continue
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
		_ = rc.Close()
		if f.Name == "config.json" {
			var c Config
			if json.Unmarshal(b, &c) == nil {
				_ = a.cfg.Replace(c)
			}
		} else {
			_ = os.WriteFile("/etc/openaudiohub/audio.json", b, 0644)
		}
	}
	a.requestReconcile("configuration restored")
	writeJSON(w, 202, map[string]any{"ok": true, "revision": a.cfg.Get().Revision})
}

func intParam(v string, def int) int {
	n, e := strconv.Atoi(v)
	if e != nil {
		return def
	}
	return n
}
