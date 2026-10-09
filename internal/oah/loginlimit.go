package oah

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Failed sign-ins are throttled per client address. The hub has a single shared
// password and is often reachable by everyone on the LAN, so without a limit a
// guesser could try indefinitely at the speed of PBKDF2. A few typos are free;
// after that each failure doubles the wait, up to a ceiling.
const (
	loginFreeFailures = 5
	loginBaseLockout  = 30 * time.Second
	loginMaxLockout   = 15 * time.Minute
	loginForgetAfter  = time.Hour
	loginMaxTracked   = 1024
)

type loginFailures struct {
	count int
	until time.Time
	last  time.Time
}

type loginLimiter struct {
	mu      sync.Mutex
	clients map[string]*loginFailures
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{clients: map[string]*loginFailures{}}
}

// retryAfter returns how long key must wait before another attempt, or 0.
func (l *loginLimiter) retryAfter(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f := l.clients[key]; f != nil && now.Before(f.until) {
		return f.until.Sub(now)
	}
	return 0
}

func (l *loginLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.clients) >= loginMaxTracked {
		l.pruneLocked(now)
	}
	f := l.clients[key]
	if f == nil || now.Sub(f.last) > loginForgetAfter {
		f = &loginFailures{}
		l.clients[key] = f
	}
	f.count++
	f.last = now
	if f.count < loginFreeFailures {
		return
	}
	d := loginBaseLockout
	for i := loginFreeFailures; i < f.count && d < loginMaxLockout; i++ {
		d *= 2
	}
	if d > loginMaxLockout {
		d = loginMaxLockout
	}
	f.until = now.Add(d)
}

func (l *loginLimiter) succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, key)
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	for k, f := range l.clients {
		if now.Sub(f.last) > loginForgetAfter && !now.Before(f.until) {
			delete(l.clients, k)
		}
	}
	// Still full: an address-spraying client. Drop everything rather than grow
	// without bound; the per-request PBKDF2 serialization still applies.
	if len(l.clients) >= loginMaxTracked {
		l.clients = map[string]*loginFailures{}
	}
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
