package oah

import (
	"strings"
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

// streamingRetry is the shortest gap between automatic attempts to reach a
// device that already failed to answer while a source is playing.
//
// The hub has one radio for everything. Paging an absent device holds it for
// the whole page timeout (5 s or more per attempt), and while it pages the
// controller starves the A2DP links that are playing: the audio stutters or
// drops out every few minutes for as long as an assigned device stays switched
// off. While audio flows, an absent device is therefore retried rarely; sources
// and headsets that come back usually reconnect to the hub by themselves, and
// Connect on the Devices page is never throttled.
const streamingRetry = 10 * time.Minute

type connectState struct {
	fails int
	next  time.Time
	last  time.Time // last failed attempt
}

func (a *App) connectTooSoon(addr string, now time.Time) bool {
	streaming := a.audioStreaming()
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	st := a.connectState[addr]
	if st == nil {
		return false
	}
	if now.Before(st.next) {
		return true
	}
	return streaming && st.fails > 0 && now.Before(st.last.Add(streamingRetry))
}

// audioStreaming reports whether a source is sending audio right now, as of
// the last transport list the hub read.
func (a *App) audioStreaming() bool { return a.streaming.Load() }

// noteTransports records whether audio is being heard: a source is playing
// and an output is connected to hear it. The output transport alone does not
// count, because it stays active with silence between sounds; and with no output
// connected nothing can stutter, so the hub should keep looking for one.
func (a *App) noteTransports(ts []Transport) {
	playing, output := false, false
	for _, t := range ts {
		switch {
		case strings.Contains(t.UUID, "Audio Sink") && t.State == "active":
			playing = true
		case strings.Contains(t.UUID, "Audio Source"):
			output = true
		}
	}
	heard := playing && output
	if a.streaming.Swap(heard) != heard && heard {
		a.logf("audio is playing: devices that do not answer are retried at most every %s", streamingRetry)
	}
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
	st.last = now
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
