package oah

import (
	"testing"
)

// There is one output slot: the engine exposes a single A2DP source endpoint, so a
// second could never be live. A configuration written while a second slot existed
// must collapse cleanly rather than keep a phantom output.
func TestSingleOutputSlot(t *testing.T) {
	c := defaultConfig()
	if len(c.Slots.Outputs) != maxOutputs || maxOutputs != 1 {
		t.Fatalf("default has %d output slots (maxOutputs=%d)", len(c.Slots.Outputs), maxOutputs)
	}
	c.Slots.Outputs = []string{"AA:BB:CC:DD:EE:FF"}
	normalizeConfig(&c)
	if len(c.Slots.Outputs) != 1 || c.Slots.Outputs[0] != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("output assignment not preserved: %#v", c.Slots.Outputs)
	}
	c.Slots.Outputs = []string{"AA:BB:CC:DD:EE:FF", "AA:BB:CC:DD:EE:02"}
	normalizeConfig(&c)
	if len(c.Slots.Outputs) != 1 {
		t.Fatalf("stale second output slot survived: %#v", c.Slots.Outputs)
	}
	if c.Slots.Outputs[0] != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("kept the wrong slot: %#v", c.Slots.Outputs)
	}
}
