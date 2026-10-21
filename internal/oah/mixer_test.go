package oah

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakePulse is a sink-input whose volume and mute change when pactl sets them,
// like pipewire-pulse.
type fakePulse struct {
	mu          sync.Mutex
	id, addr    string
	left, right int
	mute        bool
}

func (p *fakePulse) reply(call string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fs := strings.Fields(call)
	switch {
	case strings.HasSuffix(call, "pactl list sink-inputs"):
		m := "no"
		if p.mute {
			m = "yes"
		}
		node := "bluez_input." + strings.ReplaceAll(p.addr, ":", "_") + ".2"
		return fmt.Sprintf("Sink Input #%s\n\tMute: %s\n\tVolume: front-left: 0 / %d%% / 0.00 dB,   front-right: 0 / %d%% / 0.00 dB\n\t        balance 0.00\n\tProperties:\n\t\tnode.name = \"%s\"\n",
			p.id, m, p.left, p.right, node), nil
	case strings.Contains(call, "set-sink-input-volume "+p.id+" "):
		fmt.Sscanf(fs[len(fs)-2], "%d%%", &p.left)
		fmt.Sscanf(fs[len(fs)-1], "%d%%", &p.right)
	case strings.Contains(call, "set-sink-input-mute "+p.id+" "):
		p.mute = fs[len(fs)-1] == "1"
	}
	return "", nil
}

// Reconcile applies the mixer on every pass. pactl is only asked to change a
// stream whose volume or mute differs from the mixer settings, and a stream
// PipeWire reset is always put back.
func TestApplyMixerSendsOnlyWhatDiffers(t *testing.T) {
	const in1 = "AA:BB:CC:DD:EE:01"
	pw := &fakePulse{id: "41", addr: in1, left: 100, right: 100}
	rec := &recordedRunner{reply: pw.reply}
	a := reconcileApp(t, rec, "AA:BB:CC:DD:EE:09", in1)
	m := a.cfg.Get().Mixer
	m.Placement[0] = "left"
	if err := a.updateMixer(m); err != nil {
		t.Fatal(err)
	}
	vol := dbToPercent(m.Gains[0] + m.HeadroomDB + m.Master)
	if pw.left != vol || pw.right != 0 {
		t.Fatalf("Left placement: stream at %d%%/%d%%, want %d%%/0%%", pw.left, pw.right, vol)
	}

	sets := rec.countContaining("set-sink-input")
	a.applyMixer()
	if rec.countContaining("set-sink-input") != sets {
		t.Fatalf("a stream already at the mixer settings must not be set again: %v", rec.calls)
	}

	// The source paused and resumed: PipeWire put both channels back to one
	// level. The next pass must restore the placement.
	pw.mu.Lock()
	pw.left, pw.right = 100, 100
	pw.mu.Unlock()
	a.applyMixer()
	if pw.left != vol || pw.right != 0 {
		t.Fatalf("after a PipeWire reset the stream is at %d%%/%d%%, want %d%%/0%%", pw.left, pw.right, vol)
	}

	m.Placement[0] = "right"
	m.Mutes[0] = true
	if err := a.updateMixer(m); err != nil {
		t.Fatal(err)
	}
	if pw.left != 0 || pw.right != vol || !pw.mute {
		t.Fatalf("Right placement and mute: %d%%/%d%% mute=%v", pw.left, pw.right, pw.mute)
	}
}

func TestStreamHas(t *testing.T) {
	block := "Sink Input #7\n\tMute: no\n\tVolume: front-left: 52429 /  80% / -5.81 dB,   front-right: 0 /   0% / -inf dB\n\t        balance -1.00\n"
	if !streamHas(block, 80, 0, false) {
		t.Fatal("80%/0% unmuted must match")
	}
	if streamHas(block, 80, 80, false) || streamHas(block, 80, 0, true) {
		t.Fatal("a different volume or mute must not match")
	}
	if streamHas("Sink Input #7\n\tMute: no\n\tVolume: mono: 65536 / 100% / 0.00 dB\n", 100, 100, false) {
		t.Fatal("a mono stream cannot show a placement")
	}
	if streamHas("", 100, 100, false) {
		t.Fatal("an unreadable block must count as different")
	}
}

func (r *recordedRunner) countContaining(sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}
