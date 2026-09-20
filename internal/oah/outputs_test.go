package oah

import (
	"archive/zip"
	"bytes"
	"net/http/httptest"
	"path/filepath"
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

// A restore must never answer 202 when it applied nothing: a backup without the
// expected members, or with unreadable JSON, has to fail loudly.
func TestRestoreRejectsBackupsItCannotApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, defaultConfig()); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	post := func(members map[string]string) int {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range members {
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(body))
		}
		_ = zw.Close()
		req := httptest.NewRequest("POST", "/api/system/restore", &buf)
		rec := httptest.NewRecorder()
		a.handleRestore(rec, req)
		return rec.Code
	}
	cases := []struct {
		name    string
		members map[string]string
		want    int
	}{
		{"nothing recognisable", map[string]string{"readme.txt": "hi"}, 400},
		{"unreadable config", map[string]string{"config.json": "{not json"}, 400},
		{"no members", map[string]string{}, 400},
		{"valid config", map[string]string{"config.json": mustJSON(defaultConfig())}, 202},
	}
	for _, tc := range cases {
		got := post(tc.members)
		if got != tc.want {
			t.Fatalf("%s: got status %d, want %d", tc.name, got, tc.want)
		}
	}
}
