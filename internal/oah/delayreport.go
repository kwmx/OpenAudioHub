package oah

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Observable states for the secondary receiver's fixed rendering-delay report.
const (
	delayDefault     = "default"     // requested 0: engine default, nothing is reported
	delayUnsupported = "unsupported" // no supported owning-process mechanism
	delayPending     = "pending"     // applied, not yet confirmed by the transport
	delayReported    = "reported"    // the acquired transport currently reports the requested total
	delayRejected    = "rejected"    // the owning D-Bus write was refused
	delayMismatch    = "mismatch"    // attempted or accepted, but the transport does not show it
)

// BlueZ MediaTransport1.Delay is expressed in 0.1 ms units; the UI and the
// configuration use whole milliseconds. These two helpers are the only place the
// conversion happens, and they mirror `g_variant_new_uint16(ms * 10)` in
// patches/bluealsa-receiver.py.
func delayMSToUnits(ms int) int { return ms * 10 }
func delayUnitsToMS(units int) int {
	return units / 10
}

// DelayReport is the observable outcome of the experimental rendering-delay
// report on the secondary (BlueALSA) input.
//
// Only the process that acquired the transport may write MediaTransport1.Delay,
// so the attempt is made inside the patched BlueALSA daemon and recorded in a
// state file. That record is merged here with the live BlueZ property, because a
// successful D-Bus write is not proof that the value stuck.
//
// Neither a stored value nor a stored attempt is evidence that a source
// application corrected its video. A/V sync itself is not observable from here.
type DelayReport struct {
	RequestedMS int    `json:"requestedMs"`
	State       string `json:"state"`
	Input       string `json:"input"`
	Addr        string `json:"addr,omitempty"`
	Attempt     string `json:"attempt,omitempty"`
	Detail      string `json:"detail,omitempty"`
	ActualMS    int    `json:"actualMs"`
	ActualKnown bool   `json:"actualKnown"`
	AttemptedAt string `json:"attemptedAt,omitempty"`
	Capable     bool   `json:"capable"`
}

func delayStatePath() string {
	if v := strings.TrimSpace(os.Getenv("OPENAUDIOHUB_DELAY_STATE_FILE")); v != "" {
		return v
	}
	return "/run/openaudiohub/delay-report.state"
}

func receiverBinaryPath() string {
	if v := strings.TrimSpace(os.Getenv("OPENAUDIOHUB_RECEIVER_BIN")); v != "" {
		return v
	}
	return "/usr/local/lib/openaudiohub/bluealsa-4.3.1-oah"
}

var (
	capMu     sync.Mutex
	capPath   string
	capSize   int64
	capMod    time.Time
	capResult bool
)

// receiverSupportsDelayReport reports whether the installed isolated receiver
// carries the delay-report patch. The patched daemon reads this environment
// variable, so the literal name in the binary is a dependable capability marker.
// The result is cached against the file's size and modification time.
func receiverSupportsDelayReport() bool {
	path := receiverBinaryPath()
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	capMu.Lock()
	defer capMu.Unlock()
	if capPath == path && capSize == st.Size() && capMod.Equal(st.ModTime()) {
		return capResult
	}
	b, err := os.ReadFile(path)
	ok := err == nil && strings.Contains(string(b), "OAH_ADVERTISED_DELAY_MS")
	capPath, capSize, capMod, capResult = path, st.Size(), st.ModTime(), ok
	return ok
}

// delayAttempt is the record the patched BlueALSA daemon writes after it tries to
// report a rendering delay from the connection that owns the transport.
type delayAttempt struct {
	Transport   string
	RequestedMS int
	Result      string
	Detail      string
	Epoch       int64
	Addr        string
}

func readDelayAttempt() *delayAttempt {
	b, err := os.ReadFile(delayStatePath())
	if err != nil {
		return nil
	}
	at := &delayAttempt{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "transport":
			at.Transport = v
			at.Addr = addrFromBluezPath(v)
		case "requested_ms":
			at.RequestedMS, _ = strconv.Atoi(v)
		case "result":
			at.Result = v
		case "detail":
			at.Detail = v
		case "epoch":
			at.Epoch, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if at.Result == "" {
		return nil
	}
	return at
}

// reconcileDelayReport keeps a cached report consistent with the configured value.
//
// A cached report may predate the most recent apply, so serving its verdict next to
// a different configured value in the same response would contradict itself: the
// audio block would say 150 ms while the report still described 0. The verdict is
// withheld until the next full rebuild rather than guessed.
func reconcileDelayReport(rep DelayReport, requested int, capable bool) DelayReport {
	if rep.RequestedMS == requested {
		return rep
	}
	out := DelayReport{
		RequestedMS: requested,
		Input:       rep.Input,
		Addr:        rep.Addr,
		Capable:     capable,
		State:       delayPending,
		Detail:      "Waiting for the receiver to confirm the new value.",
	}
	if out.Input == "" {
		out.Input = "Input 2 (BlueALSA receiver)"
	}
	if requested == 0 {
		out.State = delayDefault
		out.Detail = "Engine default. Nothing is reported to the source."
	}
	return out
}

// delayReport derives the state shown for the secondary input. transports must be
// the list already gathered for this state build so no extra bluetoothctl calls
// are made on a 1 GB board.
func (a *App) delayReport(transports []Transport) DelayReport {
	cfg := a.cfg.Get()
	requested := cfg.Audio.SecondaryAdvertisedDelayMS
	rep := DelayReport{
		RequestedMS: requested,
		Input:       "Input 2 (BlueALSA receiver)",
		Capable:     receiverSupportsDelayReport(),
	}
	secondary := ""
	if len(cfg.Slots.Inputs) > 1 {
		secondary = strings.ToUpper(cfg.Slots.Inputs[1])
	}
	var live *Transport
	for i := range transports {
		t := &transports[i]
		if !strings.Contains(t.UUID, "Audio Sink") {
			continue
		}
		if secondary != "" && strings.EqualFold(t.Addr, secondary) {
			live = t
			break
		}
	}
	if live != nil {
		rep.Addr = live.Addr
		rep.ActualKnown = live.DelayKnown
		rep.ActualMS = delayUnitsToMS(live.Delay)
	}
	// The attempt record only describes a non-zero request, and a record made for a
	// previous value must never be attributed to the current one.
	if requested > 0 {
		if at := readDelayAttempt(); at != nil && at.RequestedMS == requested {
			rep.Attempt = at.Result
			if at.Detail != "" {
				rep.Detail = at.Detail
			}
			if at.Epoch > 0 {
				rep.AttemptedAt = time.Unix(at.Epoch, 0).UTC().Format(time.RFC3339)
			}
		}
	}

	switch {
	case requested == 0:
		rep.State = delayDefault
		if rep.Detail == "" {
			rep.Detail = "Engine default. Nothing is reported to the source."
		}
	case !rep.Capable:
		rep.State = delayUnsupported
		rep.Detail = "The installed secondary receiver has no delay-report support. Rebuild it with scripts/build-bluealsa.sh."
	case live == nil:
		rep.State = delayPending
		rep.Detail = "Waiting for the secondary source to reconnect and acquire the receiver."
	case rep.Attempt == "rejected":
		rep.State = delayRejected
		if rep.Detail == "" {
			rep.Detail = "BlueZ refused the report from the owning connection."
		}
	// With no readable property there is no evidence of a mismatch, only an
	// inability to verify, so this must not be reported as a failure.
	case !rep.ActualKnown:
		rep.State = delayPending
		if rep.Attempt == "reported" {
			rep.Detail = "BlueZ accepted the report, but this transport exposes no delay to read back."
		} else {
			rep.Detail = "Applied. BlueZ does not expose a delay for this transport yet."
		}
	case rep.ActualMS == requested:
		rep.State = delayReported
		rep.Detail = "The acquired transport reports this total."
	case rep.Attempt == "reported":
		rep.State = delayMismatch
		rep.Detail = "BlueZ accepted the report, but the transport does not show it."
	default:
		rep.State = delayPending
		rep.Detail = "Applied. Waiting for the receiver to report this total."
	}
	return rep
}
