package oah

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const pbkdf2Iterations = 210000

// maxInputs and maxOutputs bound the routing model. Two inputs are the engine's
// hard limit (one A2DP sink SEP each from PipeWire and BlueALSA). Two outputs are
// supported by fanning the mix out through a combine sink; the model is a list so
// adding a third would not need new plumbing, only another SEP.
const (
	maxInputs  = 2
	maxOutputs = 2
)

func defaultConfig() Config {
	u, _ := user.Current()
	uid := os.Getuid()
	username := "openaudiohub"
	if u != nil {
		username = u.Username
		if n, err := strconv.Atoi(u.Uid); err == nil {
			uid = n
		}
	}
	return Config{
		Listen:        ":80",
		AudioUser:     username,
		AudioUID:      uid,
		BluetoothName: "OpenAudioHub",
		Slots:         SlotsConfig{Inputs: make([]string, maxInputs), Outputs: make([]string, maxOutputs)},
		Mixer:         MixerConfig{Gains: []float64{0, 0}, Mutes: []bool{false, false}, Placement: []string{"stereo", "stereo"}, Master: 0, HeadroomDB: -6, Limiter: false},
		Audio:         AudioConfig{SecondarySBCMaxBitpool: 35, Preset: "balanced", PreferredRate: 48000, AllowedRates: []int{44100, 48000}, Quantum: 2048, BlueALSAPeriodUS: 100000, BlueALSABufferUS: 500000, Resampler: "auto", CodecPolicy: "compatibility", LiveMeters: false},
		DevicePrefs:   map[string]DevicePrefs{},
		UI:            UIConfig{PairingTimeoutSec: 120},
		WiFi:          WiFiConfig{Interface: "wlan0", PreferredBand: "5GHz"},
	}
}

func InitConfig(path string) error {
	cfg := defaultConfig()
	if v := os.Getenv("OPENAUDIOHUB_AUDIO_USER"); v != "" {
		cfg.AudioUser = v
	}
	if v := os.Getenv("OPENAUDIOHUB_AUDIO_UID"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			cfg.AudioUID = n
		}
	}
	r := bufio.NewReader(os.Stdin)
	password, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	p := strings.TrimSpace(string(password))
	if len(p) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	cfg.PasswordSalt = base64.RawStdEncoding.EncodeToString(salt)
	cfg.PasswordHash = base64.RawStdEncoding.EncodeToString(pbkdf2SHA256([]byte(p), salt, pbkdf2Iterations, 32))
	return saveConfig(path, cfg)
}

func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	normalizeConfig(&cfg)
	return cfg, nil
}

func normalizeConfig(c *Config) {
	if c.Audio.SecondarySBCMaxBitpool == 0 {
		c.Audio.SecondarySBCMaxBitpool = 35
	}
	if len(c.Slots.Inputs) < 2 {
		c.Slots.Inputs = append(c.Slots.Inputs, make([]string, 2-len(c.Slots.Inputs))...)
	}
	if len(c.Slots.Outputs) < 1 {
		c.Slots.Outputs = []string{""}
	}
	// Older configurations carry a single output slot; pad to the supported
	// count so existing installs gain Output 2 without a migration step. Never
	// truncate: a slot holding an address is user data.
	for len(c.Slots.Outputs) < maxOutputs {
		c.Slots.Outputs = append(c.Slots.Outputs, "")
	}
	if len(c.Mixer.Gains) < len(c.Slots.Inputs) {
		c.Mixer.Gains = append(c.Mixer.Gains, make([]float64, len(c.Slots.Inputs)-len(c.Mixer.Gains))...)
	}
	if len(c.Mixer.Mutes) < len(c.Slots.Inputs) {
		c.Mixer.Mutes = append(c.Mixer.Mutes, make([]bool, len(c.Slots.Inputs)-len(c.Mixer.Mutes))...)
	}
	for len(c.Mixer.Placement) < len(c.Slots.Inputs) {
		c.Mixer.Placement = append(c.Mixer.Placement, "stereo")
	}
	if c.Listen == "" {
		c.Listen = ":80"
	}
	if c.BluetoothName == "" {
		c.BluetoothName = "OpenAudioHub"
	}
	if c.UI.PairingTimeoutSec <= 0 {
		c.UI.PairingTimeoutSec = 120
	}
	if c.WiFi.Interface == "" {
		c.WiFi.Interface = "wlan0"
	}
	if c.WiFi.PreferredBand == "" {
		c.WiFi.PreferredBand = "5GHz"
	}
	if c.DevicePrefs == nil {
		c.DevicePrefs = map[string]DevicePrefs{}
	}
	if len(c.Audio.AllowedRates) == 0 {
		c.Audio.AllowedRates = []int{44100, 48000}
	}
	if c.Audio.PreferredRate == 0 {
		c.Audio.PreferredRate = 48000
	}
	if c.Audio.Quantum == 0 {
		c.Audio.Quantum = 2048
	}
	if c.Audio.BlueALSAPeriodUS == 0 {
		c.Audio.BlueALSAPeriodUS = 100000
	}
	if c.Audio.BlueALSABufferUS == 0 {
		c.Audio.BlueALSABufferUS = 500000
	}
	// Debian/Trixie BlueALSA 4.3.1 does not expose an adaptive-resampler
	// control to bluealsa-aplay. Keep configuration truthful: PipeWire handles
	// rate conversion and no unsupported BlueALSA resampler knob is persisted.
	c.Audio.Resampler = "auto"
	c.Audio.LiveMeters = false
	if c.Audio.CodecPolicy == "" {
		c.Audio.CodecPolicy = "compatibility"
	}
	if c.Audio.Preset == "" {
		c.Audio.Preset = "balanced"
	}
}

func saveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func verifyPassword(cfg Config, password string) bool {
	salt, err1 := base64.RawStdEncoding.DecodeString(cfg.PasswordSalt)
	want, err2 := base64.RawStdEncoding.DecodeString(cfg.PasswordHash)
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, len(want))
	return hmac.Equal(got, want)
}

func setPassword(cfg *Config, password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	cfg.PasswordSalt = base64.RawStdEncoding.EncodeToString(salt)
	cfg.PasswordHash = base64.RawStdEncoding.EncodeToString(pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, 32))
	return nil
}

func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hLen := 32
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	for block := 1; block <= blocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := 0; j < hLen; j++ {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func cloneConfig(c Config) Config {
	n := c
	n.Slots.Inputs = append([]string{}, c.Slots.Inputs...)
	n.Slots.Outputs = append([]string{}, c.Slots.Outputs...)
	n.Mixer.Gains = append([]float64{}, c.Mixer.Gains...)
	n.Mixer.Mutes = append([]bool{}, c.Mixer.Mutes...)
	n.Mixer.Placement = append([]string{}, c.Mixer.Placement...)
	n.Audio.AllowedRates = append([]int{}, c.Audio.AllowedRates...)
	n.DevicePrefs = make(map[string]DevicePrefs, len(c.DevicePrefs))
	for k, v := range c.DevicePrefs {
		n.DevicePrefs[k] = v
	}
	return n
}

type configStore struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func newConfigStore(path string) (*configStore, error) {
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	return &configStore{path: path, cfg: cfg}, nil
}
func (s *configStore) Get() Config { s.mu.RLock(); defer s.mu.RUnlock(); return cloneConfig(s.cfg) }
func (s *configStore) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneConfig(s.cfg)
	if err := fn(&next); err != nil {
		return err
	}
	normalizeConfig(&next)
	next.Revision = s.cfg.Revision + 1
	if err := saveConfig(s.path, next); err != nil {
		return err
	}
	s.cfg = next
	return nil
}
func (s *configStore) Replace(next Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next = cloneConfig(next)
	normalizeConfig(&next)
	next.Revision = s.cfg.Revision + 1
	if err := saveConfig(s.path, next); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

func quoteShell(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func mustJSON(v any) string      { b, _ := json.Marshal(v); return string(b) }
func parseBool(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "yes") || strings.EqualFold(strings.TrimSpace(s), "true")
}
func atoiLoose(s string) int                  { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
func fmtErr(format string, args ...any) error { return fmt.Errorf(format, args...) }
