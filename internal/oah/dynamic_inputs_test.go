package oah

import (
	"path/filepath"
	"strings"
	"testing"
)

// Capacity is a limit, not a promise: the model allows maxInputs slots while the
// UI flags anything above provenInputs as experimental.
func TestInputCapacityModel(t *testing.T) {
	if maxInputs < provenInputs {
		t.Fatalf("proven (%d) exceeds capacity (%d)", provenInputs, maxInputs)
	}
	c := defaultConfig()
	if len(c.Slots.Inputs) != maxInputs {
		t.Fatalf("default has %d input slots, want %d", len(c.Slots.Inputs), maxInputs)
	}
	// A config written when only two slots existed grows without losing data.
	c.Slots.Inputs = []string{"AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02"}
	normalizeConfig(&c)
	if len(c.Slots.Inputs) != maxInputs {
		t.Fatalf("normalized to %d slots", len(c.Slots.Inputs))
	}
	if c.Slots.Inputs[0] != "AA:BB:CC:DD:EE:01" || c.Slots.Inputs[1] != "AA:BB:CC:DD:EE:02" {
		t.Fatalf("existing assignments lost: %#v", c.Slots.Inputs)
	}
	for i := 2; i < maxInputs; i++ {
		if c.Slots.Inputs[i] != "" {
			t.Fatalf("slot %d should be empty, got %q", i+1, c.Slots.Inputs[i])
		}
	}
	// Mixer channels must cover every slot, or a later gain edit fails validation.
	if len(c.Mixer.Gains) != len(c.Slots.Inputs) || len(c.Mixer.Mutes) != len(c.Slots.Inputs) || len(c.Mixer.Placement) != len(c.Slots.Inputs) {
		t.Fatalf("mixer not sized to inputs: g=%d m=%d p=%d inputs=%d",
			len(c.Mixer.Gains), len(c.Mixer.Mutes), len(c.Mixer.Placement), len(c.Slots.Inputs))
	}
	// Oversized configs are trimmed rather than indexing out of range.
	c.Slots.Inputs = []string{"1", "2", "3", "4", "5", "6"}
	normalizeConfig(&c)
	if len(c.Slots.Inputs) != maxInputs {
		t.Fatalf("oversized input list not trimmed: %d", len(c.Slots.Inputs))
	}
}

// The Audio page and the delay report name the receiver, not a single slot: every
// input after the first shares BlueALSA.
func TestBluealsaReceiverLabelTracksAssignedInputs(t *testing.T) {
	c := defaultConfig()
	if got := bluealsaReceiverLabel(c); got != "Input 2 (BlueALSA receiver)" {
		t.Fatalf("no assignment: %q", got)
	}
	c.Slots.Inputs = []string{"A", "B", "", ""}
	if got := bluealsaReceiverLabel(c); got != "Input 2 (BlueALSA receiver)" {
		t.Fatalf("one secondary input: %q", got)
	}
	c.Slots.Inputs = []string{"A", "B", "C", "D"}
	got := bluealsaReceiverLabel(c)
	if !strings.Contains(got, "Inputs 2") || !strings.Contains(got, "4") {
		t.Fatalf("expected an input range for three shared inputs, got %q", got)
	}
	// A single assigned secondary slot is named alone, even with gaps around it.
	c.Slots.Inputs = []string{"A", "", "", "D"}
	if got := bluealsaReceiverLabel(c); got != "Input 4 (BlueALSA receiver)" {
		t.Fatalf("single secondary slot with gaps: %q", got)
	}
	// Two or more share a range.
	c.Slots.Inputs = []string{"A", "B", "", "D"}
	if got := bluealsaReceiverLabel(c); got != "Inputs 2\u20134 (BlueALSA receiver)" {
		t.Fatalf("expected a range, got %q", got)
	}
	// Mis-assigning only slot 4 still names the receiver, never "Input 1".
	c.Slots.Inputs = []string{"", "", "", "D"}
	if got := bluealsaReceiverLabel(c); strings.Contains(got, "Input 1") {
		t.Fatalf("PipeWire slot named as the receiver: %q", got)
	}
}

// Roles are generic now, so slot N needs no code change.
func TestInputRolesAreGeneric(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := defaultConfig()
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxInputs; i++ {
		addr := "AA:BB:CC:DD:EE:0" + string(rune('0'+i))
		if err := a.cfg.Update(func(c *Config) error {
			c.Slots.Inputs[i-1] = addr
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		want := "in" + string(rune('0'+i))
		if got := a.roleFor(addr); got != want {
			t.Fatalf("slot %d reported %q, want %q", i, got, want)
		}
		if got := roleRank(want); got != i-1 {
			t.Fatalf("roleRank(%s)=%d, want %d", want, got, i-1)
		}
	}
	// Outputs still sort after every input.
	if roleRank("out1") <= roleRank("in"+string(rune('0'+maxInputs))) {
		t.Fatal("output should rank after inputs")
	}
}
