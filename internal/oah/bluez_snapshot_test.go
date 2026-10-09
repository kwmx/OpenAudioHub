package oah

import (
	"os"
	"testing"
	"time"
)

// The fixture is real busctl output, captured from a stand-in BlueZ
// ObjectManager serving a Windows PC, an LE-only beacon and unnamed headphones.
func TestParseManagedObjectsFromBusctl(t *testing.T) {
	b, err := os.ReadFile("testdata/busctl-managed-objects.json")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := parseManagedObjects(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) != 3 {
		t.Fatalf("got %d devices, want 3 (the adapter is not a device)", len(snap))
	}
	pc := snap["AA:BB:CC:DD:EE:01"]
	if pc.Name != "DESKTOP-WIN11" || pc.AddressType != "public" || !pc.Paired || !pc.Connected || pc.Trusted {
		t.Fatalf("unexpected PC entry: %+v", pc)
	}
	if !pc.HasRSSI || pc.RSSI != -58 || pc.Class != 0x2a010c {
		t.Fatalf("numeric properties not decoded: %+v", pc)
	}
	if caps := capsFromUUIDs(pc.UUIDs); len(caps) != 1 || caps[0] != "sends_audio" {
		t.Fatalf("caps %v", caps)
	}
	beacon := snap["AA:BB:CC:DD:EE:02"]
	if !hiddenNearby(beacon, false) {
		t.Fatal("an unpaired random-address device can never be an A2DP device and should be hidden")
	}
	if hiddenNearby(beacon, true) {
		t.Fatal("an assigned device must never be hidden")
	}
	if hiddenNearby(pc, false) {
		t.Fatal("a public-address device must be listed")
	}
	hp := snap["AA:BB:CC:DD:EE:03"]
	if got := fallbackDeviceName(hp.Addr, hp.Class, hp.Icon); got != "Headphones · EE:03" {
		t.Fatalf("fallback name %q", got)
	}
	if k := kindFromClass(hp.Class, hp.Icon); k != "output" {
		t.Fatalf("kind %q", k)
	}
}

func TestDeviceFromBluezNames(t *testing.T) {
	a := &App{deviceNames: map[string]string{}, deviceInfo: map[string]deviceInfoEntry{}}
	d := a.deviceFromBluez(bluezDevice{Addr: "AA:BB:CC:DD:EE:03", Alias: "AA-BB-CC-DD-EE-03", Class: 0x240418})
	if d.Name != "Headphones · EE:03" || !d.unnamed || d.Kind != "output" || len(d.Caps) != 0 {
		t.Fatalf("unnamed headphones: %+v", d)
	}
	a.deviceNames["AA:BB:CC:DD:EE:03"] = "Studio Buds"
	d = a.deviceFromBluez(bluezDevice{Addr: "AA:BB:CC:DD:EE:03", Alias: "AA-BB-CC-DD-EE-03", Class: 0x240418})
	if d.Name != "Studio Buds" || d.unnamed {
		t.Fatalf("a remembered name must beat the class label: %+v", d)
	}
	d = a.deviceFromBluez(bluezDevice{Addr: "AA:BB:CC:DD:EE:04", Name: "Pixel 8", Alias: "Pixel 8", Class: 0x5a020c})
	if d.Name != "Pixel 8" || d.Kind != "source" {
		t.Fatalf("phone: %+v", d)
	}
}

func TestClassLabel(t *testing.T) {
	cases := map[uint32]string{
		0x2a010c: "Computer",      // laptop
		0x5a020c: "Phone",         // smartphone
		0x240404: "Headset",       // wearable headset
		0x240418: "Headphones",    // headphones
		0x240414: "Speaker",       // loudspeaker
		0x200420: "Car audio",     // car audio
		0x00043c: "TV or display", // display and loudspeaker
	}
	for class, want := range cases {
		if got := classLabel(class, ""); got != want {
			t.Errorf("classLabel(%#x)=%q, want %q", class, got, want)
		}
	}
	if got := classLabel(0, "audio-headphones"); got != "Headphones" {
		t.Errorf("icon fallback %q", got)
	}
	if got := classLabel(0, ""); got != "" {
		t.Errorf("no information should give no label, got %q", got)
	}
}

// Scan output is mostly property changes. Only [NEW] lines and Name/Alias
// changes carry names; the rest must never be stored as one.
func TestParseScanNamesIgnoresPropertyChanges(t *testing.T) {
	out := "Discovery started\n" +
		"\x1b[0;92m[NEW]\x1b[0m Device AA:BB:CC:DD:EE:01 DESKTOP-WIN11\n" +
		"[NEW] Device AA:BB:CC:DD:EE:02 AA-BB-CC-DD-EE-02\n" +
		"[CHG] Device AA:BB:CC:DD:EE:02 RSSI: 0xffffffc4 (-60)\n" +
		"[CHG] Device AA:BB:CC:DD:EE:02 ManufacturerData Key: 0x004c (76)\n" +
		"[CHG] Device AA:BB:CC:DD:EE:02 Name: Living Room Speaker\n" +
		"[CHG] Device AA:BB:CC:DD:EE:03 TxPower: 0x0c (12)\n" +
		"\r\x1b[K[CHG] Device AA:BB:CC:DD:EE:04 Alias: Jo's iPhone\n" +
		"[DEL] Device AA:BB:CC:DD:EE:05 Old Thing\n"
	got := parseScanNames(out)
	want := map[string]string{
		"AA:BB:CC:DD:EE:01": "DESKTOP-WIN11",
		"AA:BB:CC:DD:EE:02": "Living Room Speaker",
		"AA:BB:CC:DD:EE:04": "Jo's iPhone",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestUnassignedSortNamedAndHeardFirst(t *testing.T) {
	a := &App{cfg: &configStore{cfg: defaultConfig()}, policed: map[string]time.Time{}, connectState: map[string]*connectState{}}
	devs := a.decorateDevices([]Device{
		{Addr: "AA:BB:CC:DD:EE:01", Name: "Nearby device · EE:01", unnamed: true, RSSI: -40},
		{Addr: "AA:BB:CC:DD:EE:02", Name: "Stale Speaker", RSSI: 0},
		{Addr: "AA:BB:CC:DD:EE:03", Name: "Far Phone", RSSI: -85},
		{Addr: "AA:BB:CC:DD:EE:04", Name: "Near Laptop", RSSI: -50},
	}, nil)
	want := []string{"Near Laptop", "Far Phone", "Stale Speaker", "Nearby device · EE:01"}
	for i := range want {
		if devs[i].Name != want[i] {
			t.Fatalf("position %d is %q, want order %v", i, devs[i].Name, want)
		}
	}
}

func TestAdoptIntoFreeInput(t *testing.T) {
	c := defaultConfig()
	c.Slots.Inputs[0] = "AA:BB:CC:DD:EE:10"
	if slot := adoptIntoFreeInput(&c, "AA:BB:CC:DD:EE:01"); slot != 1 {
		t.Fatalf("slot %d, want 1", slot)
	}
	if !c.DevicePrefs["AA:BB:CC:DD:EE:01"].AutoConnect {
		t.Fatal("an adopted source must auto-connect")
	}
	if slot := adoptIntoFreeInput(&c, "AA:BB:CC:DD:EE:01"); slot != -1 {
		t.Fatal("an already assigned source must not take a second slot")
	}
	for i := range c.Slots.Inputs {
		if c.Slots.Inputs[i] == "" {
			c.Slots.Inputs[i] = "AA:BB:CC:DD:EE:2" + string(rune('0'+i))
		}
	}
	if slot := adoptIntoFreeInput(&c, "AA:BB:CC:DD:EE:02"); slot != -1 {
		t.Fatal("no slot is free")
	}
}

func TestRecentlyPolicedExpires(t *testing.T) {
	a := &App{policed: map[string]time.Time{}}
	now := time.Unix(1_000_000, 0)
	a.notePoliced("AA:BB:CC:DD:EE:01", now)
	if !a.recentlyPoliced("AA:BB:CC:DD:EE:01", now.Add(time.Minute)) {
		t.Fatal("should be reported shortly after")
	}
	if a.recentlyPoliced("AA:BB:CC:DD:EE:01", now.Add(11*time.Minute)) {
		t.Fatal("should expire")
	}
}

func TestBluezNameCacheIncludesUnpairedDevices(t *testing.T) {
	root := t.TempDir()
	addr := "AA:BB:CC:DD:EE:07"
	if err := os.MkdirAll(root+"/11:22:33:44:55:66/cache", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/11:22:33:44:55:66/cache/"+addr, []byte("[General]\nName=Kitchen Speaker\n\n[ServiceRecords]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range bluezNameCachePaths(root, addr) {
		names = append(names, readBluezNameCache(p, addr)...)
	}
	if len(names) != 1 || names[0] != "Kitchen Speaker" {
		t.Fatalf("names %v", names)
	}
}
