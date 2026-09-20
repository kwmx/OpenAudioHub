package oah

import (
	"time"
)

// Bluetooth connect attempts are slow and stateful: killing one mid-flight can
// leave BlueZ with an in-progress connection, and a failed attempt with the peer
// busy makes the next one fail immediately. Reconcile runs every 45s, so a device
// that is switched off or refusing would be retried forever — flooding the journal
// and racing any manual connect the user makes from the UI.
//
// This bounds that: each address backs off after consecutive failures, and one
// success clears it.
const (
	pairTimeout    = 60 * time.Second
	connectTimeout = 45 * time.Second
)

var connectBackoff = []time.Duration{
	0,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
}

type connectState struct {
	fails int
	next  time.Time
}

func (a *App) connectTooSoon(addr string, now time.Time) bool {
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	st := a.connectState[addr]
	return st != nil && now.Before(st.next)
}

func (a *App) noteConnectAttempt(addr string, ok bool, now time.Time) {
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	if a.connectState == nil {
		a.connectState = map[string]*connectState{}
	}
	st := a.connectState[addr]
	if st == nil {
		st = &connectState{}
		a.connectState[addr] = st
	}
	if ok {
		st.fails = 0
		st.next = time.Time{}
		return
	}
	st.fails++
	d := connectBackoff[len(connectBackoff)-1]
	if st.fails < len(connectBackoff) {
		d = connectBackoff[st.fails]
	}
	st.next = now.Add(d)
}

// connectFailureCount lets the state builder distinguish "trying right now" from
// "repeatedly failing", so the UI can offer a retry instead of showing
// "Connecting…" forever.
func (a *App) connectFailureCount(addr string) int {
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	if st := a.connectState[addr]; st != nil {
		return st.fails
	}
	return 0
}

func (a *App) forgetConnectState(addr string) {
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	delete(a.connectState, addr)
}

// resetConnectState drops all backoff, used when roles change so a deliberate
// action is not throttled by earlier failures.
func (a *App) resetConnectState() {
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	a.connectState = map[string]*connectState{}
}
