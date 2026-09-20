package oah

import (
	"path/filepath"
	"testing"
)

// Older configurations carry a single output slot. Normalization must add the
// second so existing installs gain Output 2, without ever dropping a slot that
// already holds an address.
func TestOutputSlotsPadToMaximumWithoutTruncating(t *testing.T) {
	c := defaultConfig()
	if len(c.Slots.Outputs) != maxOutputs {
		t.Fatalf("default has %d output slots, want %d", len(c.Slots.Outputs), maxOutputs)
	}
	c.Slots.Outputs = []string{"AA:BB:CC:DD:EE:FF"}
	normalizeConfig(&c)
	if len(c.Slots.Outputs) != maxOutputs {
		t.Fatalf("single-slot config normalized to %d slots", len(c.Slots.Outputs))
	}
	if c.Slots.Outputs[0] != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("existing assignment lost: %q", c.Slots.Outputs[0])
	}
	if c.Slots.Outputs[1] != "" {
		t.Fatalf("padded slot should be empty, got %q", c.Slots.Outputs[1])
	}
	// Hand-edited extra slots are preserved rather than silently dropped.
	c.Slots.Outputs = []string{"A", "B", "C"}
	normalizeConfig(&c)
	if len(c.Slots.Outputs) != 3 {
		t.Fatalf("normalization truncated user data to %d slots", len(c.Slots.Outputs))
	}
}

// roleFor drives both the UI and the state, so a second output must be reported
// as out2 rather than folded into out1.
func TestRoleForReportsSecondOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := defaultConfig()
	c.Slots.Outputs = []string{"AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02"}
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.roleFor("AA:BB:CC:DD:EE:01"); got != "out1" {
		t.Fatalf("first output reported as %q", got)
	}
	if got := a.roleFor("AA:BB:CC:DD:EE:02"); got != "out2" {
		t.Fatalf("second output reported as %q, want out2", got)
	}
	if got := a.roleFor("AA:BB:CC:DD:EE:03"); got != "" {
		t.Fatalf("unassigned device reported as %q", got)
	}
}

// A second output must survive an update round-trip; it is ordinary config.
func TestSecondOutputPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, defaultConfig()); err != nil {
		t.Fatal(err)
	}
	store, err := newConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *Config) error {
		c.Slots.Outputs[1] = "AA:BB:CC:DD:EE:02"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	back, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Slots.Outputs) < 2 || back.Slots.Outputs[1] != "AA:BB:CC:DD:EE:02" {
		t.Fatalf("second output lost on reload: %#v", back.Slots.Outputs)
	}
	// A clone must not alias the slice, or edits would leak across snapshots.
	clone := cloneConfig(back)
	clone.Slots.Outputs[1] = "changed"
	if back.Slots.Outputs[1] != "AA:BB:CC:DD:EE:02" {
		t.Fatal("output slots aliased across config clone")
	}
}
