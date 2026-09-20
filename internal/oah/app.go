package oah

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

type App struct {
	cfg     *configStore
	run     runner
	version string
	listen  string

	mu           sync.RWMutex
	pairing      PairingState
	networkApply *NetworkApply
	wifiNetworks []WiFiNetwork

	audioConfigMu   sync.Mutex
	mixerApplyMu    sync.Mutex
	reconcileMu     sync.Mutex
	btOpsMu         sync.Mutex
	deviceNamesMu   sync.Mutex
	deviceNames     map[string]string
	deviceNamesPath string
	// Bounded cache of BlueZ device detail for unpaired, discovered devices.
	deviceInfoMu sync.Mutex
	deviceInfo   map[string]deviceInfoEntry
	// Per-address Bluetooth connect backoff; see backoff.go.
	connectMu       sync.Mutex
	connectState    map[string]*connectState
	stateBuildMu    sync.Mutex
	stateCacheMu    sync.RWMutex
	stateCache      State
	stateCacheValid bool
	logMu           sync.Mutex
	logs            []string

	loginMu sync.Mutex

	audioHealMu   sync.Mutex
	audioLastHeal time.Time

	sseMu        sync.Mutex
	sse          map[chan []byte]struct{}
	refresh      chan struct{}
	reconcileReq chan string
}

func NewApp(configPath, version string) (*App, error) {
	cfg, err := newConfigStore(configPath)
	if err != nil {
		return nil, err
	}
	namePath := deviceNameCachePath(configPath)
	a := &App{cfg: cfg, run: runner{}, version: version, listen: cfg.Get().Listen, deviceNames: loadDeviceNameCache(namePath), deviceNamesPath: namePath, deviceInfo: map[string]deviceInfoEntry{}, connectState: map[string]*connectState{}, sse: map[chan []byte]struct{}{}, refresh: make(chan struct{}, 1), reconcileReq: make(chan string, 1)}
	return a, nil
}
func (a *App) SetListen(s string) { a.listen = s }

func (a *App) Run() error {
	a.logf("OpenAudioHub %s starting", a.version)
	if err := a.writeAudioProjection(a.cfg.Get().Audio); err != nil {
		a.logf("audio config projection failed: %v", err)
	}
	// Bluetooth/audio health must never gate the control plane. Start the HTTP
	// server even if BlueZ is still initializing or has failed. Reconcile and
	// identity synchronization recover independently in the background.
	go func() {
		_ = a.setPairing(false)
		a.syncBluetoothIdentity()
	}()
	go a.stateLoop()
	go a.reconcileLoop()
	go a.audioSupervisor()
	go a.mixerEventSupervisor()
	go a.routeSupervisor()
	go func() {
		// Give the lingering user manager a moment to settle, then make sure the
		// PipeWire/Pulse compatibility sockets exist before reconnecting routes.
		time.Sleep(2 * time.Second)
		_ = a.ensureAudioSession()
		time.Sleep(2 * time.Second)
		a.requestReconcile("daemon startup")
	}()
	mux := http.NewServeMux()
	a.routes(mux)
	srv := &http.Server{Addr: a.listen, Handler: securityHeaders(a.recoverHTTP(mux)), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	a.logf("listening on %s", a.listen)
	return srv.ListenAndServe()
}

func (a *App) logf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	log.Print(line)
	a.logMu.Lock()
	a.logs = append(a.logs, line)
	if len(a.logs) > 250 {
		a.logs = a.logs[len(a.logs)-250:]
	}
	a.logMu.Unlock()
}
func (a *App) requestReconcile(reason string) {
	select {
	case a.reconcileReq <- reason:
	default:
		// One reconcile is already queued. It reads the latest config when it runs,
		// so additional clicks/config changes are intentionally coalesced.
	}
}
func (a *App) reconcileLoop() {
	for reason := range a.reconcileReq {
		a.reconcileRoutes(reason)
	}
}

func (a *App) signalRefresh() {
	select {
	case a.refresh <- struct{}{}:
	default:
	}
}
func (a *App) cacheState(st State) {
	a.stateCacheMu.Lock()
	a.stateCache = st
	a.stateCacheValid = true
	a.stateCacheMu.Unlock()
}

func (a *App) cachedState() (State, bool) {
	a.stateCacheMu.RLock()
	defer a.stateCacheMu.RUnlock()
	st := a.stateCache
	c := a.cfg.Get()
	st.Revision, st.Audio, st.Mixer, st.Slots = c.Revision, c.Audio, c.Mixer, c.Slots
	st.DelayReport = reconcileDelayReport(st.DelayReport, c.Audio.SecondaryAdvertisedDelayMS, st.DelayReport.Capable)
	return st, a.stateCacheValid
}

func (a *App) stateLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var last string
	for {
		// Build once immediately at startup, then only on the periodic timer or an
		// explicit state-changing action. HTTP clients read this cache rather than
		// launching their own BlueZ/iw command trees, so extra tabs or aggressive
		// refreshes cannot turn into an appliance-wide subprocess storm.
		st := a.buildState(false)
		a.cacheState(st)
		b, _ := json.Marshal(st)
		s := string(b)
		if s != last {
			last = s
			a.broadcast(b)
		}
		select {
		case <-ticker.C:
		case <-a.refresh:
		}
	}
}
func (a *App) broadcast(b []byte) {
	a.sseMu.Lock()
	defer a.sseMu.Unlock()
	for ch := range a.sse {
		select {
		case ch <- b:
		default:
		}
	}
}
func randToken() string { b := make([]byte, 32); _, _ = rand.Read(b); return hex.EncodeToString(b) }

// Session cookies are stateless and signed with key material derived from the
// password hash. This keeps sessions valid across daemon restarts/reboots while
// making every existing session invalid immediately after a password change.
func sessionSigningKey(c Config) []byte {
	s := sha256.Sum256([]byte("OpenAudioHub/session/v1\x00" + c.PasswordSalt + "\x00" + c.PasswordHash))
	return s[:]
}

func mintSessionToken(c Config, exp time.Time) string {
	payload := "v1." + strconv.FormatInt(exp.Unix(), 10) + "." + randToken()
	m := hmac.New(sha256.New, sessionSigningKey(c))
	_, _ = m.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	return payload + "." + sig
}

func validSessionToken(c Config, token string, now time.Time) bool {
	p := strings.Split(token, ".")
	if len(p) != 4 || p[0] != "v1" || len(p[2]) < 32 {
		return false
	}
	expUnix, err := strconv.ParseInt(p[1], 10, 64)
	if err != nil || now.Unix() > expUnix {
		return false
	}
	payload := strings.Join(p[:3], ".")
	m := hmac.New(sha256.New, sessionSigningKey(c))
	_, _ = m.Write([]byte(payload))
	want := m.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(p[3])
	return err == nil && hmac.Equal(got, want)
}

func (a *App) recoverHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				ref := randToken()[:10]
				a.logf("panic[%s] %s %s: %v", ref, r.Method, r.URL.Path, v)
				log.Printf("OpenAudioHub panic[%s]: %v\n%s", ref, v, debug.Stack())
				if !strings.HasPrefix(r.URL.Path, "/events") {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "That request failed. The hub is still running.", "reference": ref})
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) authenticated(r *http.Request) bool {
	c, err := r.Cookie("oah_session")
	if err != nil {
		return false
	}
	return validSessionToken(a.cfg.Get(), c.Value, time.Now())
}
func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			writeJSON(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (a *App) writeProblem(w http.ResponseWriter, status int, publicMessage, operation string, err error) {
	payload := map[string]any{"error": publicMessage}
	if err != nil {
		ref := randToken()[:10]
		a.logf("error[%s] %s: %v", ref, operation, err)
		payload["reference"] = ref
	}
	writeJSON(w, status, payload)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request must contain exactly one JSON value")
		}
		return err
	}
	return nil
}
func cleanAddr(s string) string { m := macRE.FindString(s); return strings.ToUpper(m) }
func fileExists(p string) bool  { _, e := os.Stat(p); return e == nil }
