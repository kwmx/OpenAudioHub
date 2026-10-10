package oah

import (
	"strings"
	"testing"
)

// Reconcile applies the mixer on every pass. Only a change, or a stream that
// reappeared under a new id, may cost pactl calls.
func TestApplyMixerSendsOnlyChanges(t *testing.T) {
	const in1 = "AA:BB:CC:DD:EE:01"
	streamID := "41"
	rec := &recordedRunner{reply: func(call string) (string, error) {
		if strings.HasSuffix(call, "pactl list sink-inputs") {
			return "Sink Input #" + streamID + "\n\tapi.bluez5.address = \"" + in1 + "\"\n\tnode.name = \"bluez_input.AA_BB_CC_DD_EE_01.2\"\n", nil
		}
		return "", nil
	}}
	a := reconcileApp(t, rec, "AA:BB:CC:DD:EE:09", in1)
	sets := func() int { return rec.count("runuser") - rec.countContaining("list sink-inputs") }

	a.applyMixer()
	if n := rec.countContaining("set-sink-input-volume " + streamID); n != 1 {
		t.Fatalf("first apply must set the stream volume once, got %d", n)
	}
	before := sets()
	a.applyMixer()
	if sets() != before {
		t.Fatalf("an unchanged mixer must not call pactl again: %v", rec.calls)
	}

	m := a.cfg.Get().Mixer
	m.Mutes[0] = true
	if err := a.updateMixer(m); err != nil {
		t.Fatal(err)
	}
	if rec.countContaining("set-sink-input-mute "+streamID+" 1") != 1 {
		t.Fatal("a changed mute must be sent")
	}

	// A restarted pipewire-pulse hands out ids from the start again, so the same
	// id can belong to a new stream at the default volume.
	a.restartAudio()
	a.applyMixer()
	if n := rec.countContaining("set-sink-input-volume " + streamID); n != 3 {
		t.Fatalf("after an audio restart the stream must be set again, got %d volume calls", n)
	}

	streamID = "57" // the source reconnected
	a.applyMixer()
	if rec.countContaining("set-sink-input-volume 57") != 1 {
		t.Fatal("a new stream must get the saved mixer settings")
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
