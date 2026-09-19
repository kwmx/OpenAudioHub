package oah

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceiverOptionsBounds(t *testing.T) {
	for _, n := range []int{35, 53, 64, 250} {
		c := defaultConfig().Audio
		c.SecondarySBCMaxBitpool = n
		if err := validateReceiverOptions(c); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []int{-1, 0, 2, 34, 36, 251} {
		c := defaultConfig().Audio
		c.SecondarySBCMaxBitpool = n
		if validateReceiverOptions(c) == nil {
			t.Fatalf("accepted cap %d", n)
		}
	}
	for _, n := range []int{-1, 2001, 65535} {
		c := defaultConfig().Audio
		c.SecondaryAdvertisedDelayMS = n
		if validateReceiverOptions(c) == nil {
			t.Fatalf("accepted delay %d", n)
		}
	}
	for _, n := range []int{0, 1, 2000} {
		c := defaultConfig().Audio
		c.SecondaryAdvertisedDelayMS = n
		if err := validateReceiverOptions(c); err != nil {
			t.Fatal(err)
		}
	}
}
func TestPublicAudioProjectionDoesNotExposeSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := defaultConfig()
	c.PasswordHash = "SECRET_HASH"
	c.PasswordSalt = "SECRET_SALT"
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.writeAudioProjection(c.Audio); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "audio.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "password") {
		t.Fatal("secret in public projection")
	}
	var out AudioConfig
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.SecondarySBCMaxBitpool != 35 {
		t.Fatal("cap missing")
	}
	for _, tc := range []struct {
		name string
		mode os.FileMode
	}{{"config.json", 0600}, {"audio.json", 0644}} {
		st, err := os.Stat(filepath.Join(filepath.Dir(path), tc.name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != tc.mode {
			t.Fatalf("%s permissions %o", tc.name, st.Mode().Perm())
		}
	}
	invalid := c.Audio
	invalid.SecondarySBCMaxBitpool = 999
	if a.writeAudioProjection(invalid) == nil {
		t.Fatal("bad projection accepted")
	}
	after, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "audio.json"))
	if string(after) != string(b) {
		t.Fatal("rejected config changed projection")
	}
}
func TestRevisionAndCachedStateDoNotRegressSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := defaultConfig()
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.cacheState(State{Audio: c.Audio, Mixer: c.Mixer, Slots: c.Slots})
	if err = a.cfg.Update(func(c *Config) error { c.Audio.SecondarySBCMaxBitpool = 53; return nil }); err != nil {
		t.Fatal(err)
	}
	if a.cfg.Get().Revision != 1 {
		t.Fatal("revision did not increment")
	}
	if err = a.cfg.Update(func(c *Config) error { return errors.New("reject") }); err == nil {
		t.Fatal("expected reject")
	}
	if a.cfg.Get().Revision != 1 {
		t.Fatal("rejected update advanced revision")
	}
	s, ok := a.cachedState()
	if !ok || s.Revision != 1 || s.Audio.SecondarySBCMaxBitpool != 53 {
		t.Fatalf("stale cached state: %#v", s.Audio)
	}
	next := a.cfg.Get()
	next.Revision = 0
	if err = a.cfg.Replace(next); err != nil {
		t.Fatal(err)
	}
	if a.cfg.Get().Revision != 2 {
		t.Fatal("replacement regressed revision")
	}
}
