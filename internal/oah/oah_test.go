package oah

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	c := defaultConfig()
	if err := setPassword(&c, "correct-horse"); err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(c, "correct-horse") {
		t.Fatal("valid password rejected")
	}
	if verifyPassword(c, "wrong-password") {
		t.Fatal("invalid password accepted")
	}
	if _, err := base64.RawStdEncoding.DecodeString(c.PasswordHash); err != nil {
		t.Fatal(err)
	}
}

func TestBluetoothPathAddress(t *testing.T) {
	got := addrFromBluezPath("/org/bluez/hci0/dev_00_00_5E_00_53_01/fd5")
	if got != "00:00:5E:00:53:01" {
		t.Fatalf("got %q", got)
	}
}

func TestWiFiBandChannel(t *testing.T) {
	if bandForFreq(2412) != "2.4 GHz" || channelForFreq(2412) != 1 {
		t.Fatal("2.4 GHz mapping")
	}
	if bandForFreq(5180) != "5 GHz" || channelForFreq(5180) != 36 {
		t.Fatal("5 GHz mapping")
	}
	if channelForFreq(5540) != 108 {
		t.Fatalf("DFS channel got %d", channelForFreq(5540))
	}
}

func TestConfigUpdateIsAtomicOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := defaultConfig()
	cfg.Slots.Inputs[0] = "AA:BB:CC:DD:EE:FF"
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := newConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := store.Get().Slots.Inputs[0]
	err = store.Update(func(c *Config) error {
		c.Slots.Inputs[0] = "11:22:33:44:55:66"
		return errors.New("simulated write rejection")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := store.Get().Slots.Inputs[0]; got != want {
		t.Fatalf("failed update mutated live config: got %q want %q", got, want)
	}
}

func TestClearStaleBluetoothRoleWithoutDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := defaultConfig()
	cfg.Slots.Inputs[0] = "AA:BB:CC:DD:EE:FF"
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := newConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: store}
	if err := a.setRole("AA:BB:CC:DD:EE:FF", ""); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().Slots.Inputs[0]; got != "" {
		t.Fatalf("stale role not cleared: %q", got)
	}
}

func TestHTTPPanicIsSanitized(t *testing.T) {
	a := &App{}
	h := a.recoverHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret internal detail") }))
	r := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "secret internal detail") {
		t.Fatal("internal panic detail leaked to client")
	}
	if !strings.Contains(w.Body.String(), "recovered from an internal error") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestBluetoothNameResolutionRejectsMACAliases(t *testing.T) {
	addr := "00:00:5E:00:53:03"
	if usableBluetoothName("00-00-5E-00-53-03", addr) {
		t.Fatal("MAC-like alias accepted as a device name")
	}
	got := firstUsableBluetoothName(addr, "00-00-5E-00-53-03", "", "Test Headphones")
	if got != "Test Headphones" {
		t.Fatalf("got %q", got)
	}
}

func TestDeviceNameCachePersistsDiscoveryName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-names.json")
	a := &App{deviceNames: map[string]string{}, deviceNamesPath: path}
	a.rememberDeviceName("00:00:5E:00:53:03", "Test Headphones")
	got := loadDeviceNameCache(path)["00:00:5E:00:53:03"]
	if got != "Test Headphones" {
		t.Fatalf("cached name got %q", got)
	}
}

func TestSignedSessionSurvivesDaemonRestartAndPasswordChange(t *testing.T) {
	cfg := defaultConfig()
	if err := setPassword(&cfg, "correct-horse"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token := mintSessionToken(cfg, now.Add(90*24*time.Hour))
	if !validSessionToken(cfg, token, now.Add(time.Minute)) {
		t.Fatal("fresh signed session rejected")
	}
	// A new daemon loads the same config and can validate the same cookie.
	reloaded := cfg
	if !validSessionToken(reloaded, token, now.Add(time.Hour)) {
		t.Fatal("session did not survive daemon restart")
	}
	if err := setPassword(&reloaded, "different-password"); err != nil {
		t.Fatal(err)
	}
	if validSessionToken(reloaded, token, now.Add(time.Hour)) {
		t.Fatal("old session remained valid after password change")
	}
}

func TestDefaultAudioBridgeTimingIsFrameAligned(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Audio.BlueALSAPeriodUS != 100000 || cfg.Audio.BlueALSABufferUS != 500000 {
		t.Fatalf("unexpected balanced BlueALSA timing: %d/%d", cfg.Audio.BlueALSAPeriodUS, cfg.Audio.BlueALSABufferUS)
	}
	if cfg.Audio.BlueALSAPeriodUS%10000 != 0 {
		t.Fatal("period must stay on a 10ms boundary for 44.1/48k frame alignment")
	}
	if cfg.Audio.BlueALSABufferUS%cfg.Audio.BlueALSAPeriodUS != 0 {
		t.Fatal("buffer must be an exact multiple of period")
	}
}

func TestParseBluetoothDeviceLinesKeepsDiscoveryName(t *testing.T) {
	out := "Device 00:00:5E:00:53:03 Test Headphones\nDevice 00:00:5E:00:53:01 xyz's Mac mini\n"
	got := parseBluetoothDeviceLines(out)
	if got["00:00:5E:00:53:03"] != "Test Headphones" {
		t.Fatalf("unexpected discovery name: %q", got["00:00:5E:00:53:03"])
	}
	if got["00:00:5E:00:53:01"] != "xyz's Mac mini" {
		t.Fatalf("unexpected discovery name: %q", got["00:00:5E:00:53:01"])
	}
}

func TestConfigCloneDoesNotAliasDevicePrefs(t *testing.T) {
	cfg := defaultConfig()
	cfg.DevicePrefs["AA:BB:CC:DD:EE:FF"] = DevicePrefs{AutoConnect: true}
	clone := cloneConfig(cfg)
	clone.DevicePrefs["AA:BB:CC:DD:EE:FF"] = DevicePrefs{AutoConnect: false}
	if !cfg.DevicePrefs["AA:BB:CC:DD:EE:FF"].AutoConnect {
		t.Fatal("device preferences map was aliased across config clone")
	}
}

func TestFindSinkInputSeparatesDirectAndBlueALSAPaths(t *testing.T) {
	blocks := map[string]string{
		"101": "Sink Input #101\nProperties:\n  application.name = \"OpenAudioHub-BlueALSA\"\n  node.name = \"openaudiohub.bluealsa\"\n",
		"102": "Sink Input #102\nProperties:\n  api.bluez5.address = \"00:00:5E:00:53:02\"\n  node.name = \"bluez_input.00_00_5E_00_53_02.2\"\n",
	}
	bluealsa := map[string]bool{"00:00:5E:00:53:01": true}
	if got := findSinkInputFor(blocks, "00:00:5E:00:53:02", bluealsa); got != "102" {
		t.Fatalf("direct PipeWire source mapped to %q", got)
	}
	if got := findSinkInputFor(blocks, "00:00:5E:00:53:01", bluealsa); got != "101" {
		t.Fatalf("BlueALSA source mapped to %q", got)
	}
}
