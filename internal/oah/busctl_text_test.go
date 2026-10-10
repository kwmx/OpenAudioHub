package oah

import (
	"strings"
	"testing"
)

func TestParseBusctlTextValues(t *testing.T) {
	// Captured from busctl for a device with a non-ASCII, quoted, tabbed name, a
	// struct, a double, a negative int64, an object path and an empty dict array.
	out := `a{oa{sa{sv}}} 1 "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_09" 1 "org.bluez.Device1" 6 ` +
		`"Name" s "Caf\303\251 \303\234n\303\257 \360\237\216\247 tab\there\nnl \'q\' \"x\" \\" ` +
		`"Struct" (su) "x y" 7 "Dbl" d 1.5 "Neg" x -5 "Path" o "/a/b" "Empty" aa{sv} 0` + "\n"
	sig, v, err := parseBusctlText(out)
	if err != nil {
		t.Fatal(err)
	}
	if sig != "a{oa{sa{sv}}}" {
		t.Fatalf("signature %q", sig)
	}
	props := v.(map[string]any)["/org/bluez/hci0/dev_AA_BB_CC_DD_EE_09"].(map[string]any)["org.bluez.Device1"].(map[string]any)
	if got, want := props["Name"], "Café Ünï 🎧 tab\there\nnl 'q' \"x\" \\"; got != want {
		t.Fatalf("name %q, want %q", got, want)
	}
	if st := props["Struct"].([]any); len(st) != 2 || st[0] != "x y" || st[1] != uint64(7) {
		t.Fatalf("struct %#v", st)
	}
	if props["Dbl"] != 1.5 || props["Neg"] != int64(-5) || props["Path"] != "/a/b" {
		t.Fatalf("scalars %#v %#v %#v", props["Dbl"], props["Neg"], props["Path"])
	}
	if e := props["Empty"].([]any); len(e) != 0 {
		t.Fatalf("empty array %#v", e)
	}
}

func TestParseBusctlTextNumberKeyedDicts(t *testing.T) {
	out := `a{sv} 2 "ManufacturerData" a{qv} 2 76 ay 2 16 5 6 ay 1 9 "AdvertisingData" a{yv} 1 255 ay 0`
	_, v, err := parseBusctlText(out)
	if err != nil {
		t.Fatal(err)
	}
	md := v.(map[string]any)["ManufacturerData"].(map[string]any)
	if b := md["76"].([]any); len(b) != 2 || b[0] != uint64(16) {
		t.Fatalf("manufacturer data %#v", md)
	}
	if _, ok := v.(map[string]any)["AdvertisingData"].(map[string]any)["255"]; !ok {
		t.Fatal("byte-keyed dictionary lost")
	}
}

func TestParseBusctlTextRejectsMalformedOutput(t *testing.T) {
	for _, bad := range []string{
		``,
		`a{sv} 2 "Name" s "only one"`,    // truncated
		`a{sv} 1 "Name" s "unterminated`, // unterminated string
		`a{sv} 1 "Paired" b maybe`,       // bad boolean
		`as -1`,                          // negative count
		`as 1 "a" extra`,                 // trailing data
		`a{sv} 1 "RSSI" n notanumber`,    // bad number
		`a{s 0`,                          // malformed signature
		`Failed to create new json object: Invalid argument`, // an error message, not a reply
	} {
		if _, _, err := parseBusctlText(bad); err == nil {
			t.Errorf("accepted malformed output %q", bad)
		}
	}
	if _, err := parseManagedObjects(`as 1 "x"`); err == nil {
		t.Error("a reply of the wrong type must be rejected")
	}
}

func TestUnidentifiableDevicesAreHidden(t *testing.T) {
	a := &App{deviceNames: map[string]string{}, deviceInfo: map[string]deviceInfoEntry{}}
	withBluezStore(t)
	snap := map[string]bluezDevice{
		// LE-only gadget: public address, no name, no class.
		"AA:BB:CC:DD:EE:10": {Addr: "AA:BB:CC:DD:EE:10", AddressType: "public", Alias: "AA-BB-CC-DD-EE-10", RSSI: -50, HasRSSI: true},
		// Classic headphones that have not sent a name yet: shown with a type.
		"AA:BB:CC:DD:EE:11": {Addr: "AA:BB:CC:DD:EE:11", AddressType: "public", Alias: "AA-BB-CC-DD-EE-11", Class: 0x240418},
		// LE device with a name: shown.
		"AA:BB:CC:DD:EE:12": {Addr: "AA:BB:CC:DD:EE:12", AddressType: "public", Name: "Kitchen Plug", Alias: "Kitchen Plug"},
		// Unnamed and classless but assigned: never hidden.
		"AA:BB:CC:DD:EE:13": {Addr: "AA:BB:CC:DD:EE:13", AddressType: "public", Alias: "AA-BB-CC-DD-EE-13"},
		// No name or class yet, but an icon saying what it is: shown.
		"AA:BB:CC:DD:EE:16": {Addr: "AA:BB:CC:DD:EE:16", AddressType: "public", Alias: "AA-BB-CC-DD-EE-16", Icon: "audio-headphones"},
		// An icon BlueZ uses for nothing audio-related identifies nothing.
		"AA:BB:CC:DD:EE:17": {Addr: "AA:BB:CC:DD:EE:17", AddressType: "public", Alias: "AA-BB-CC-DD-EE-17", Icon: "unknown-gadget"},
		// Unnamed and classless but paired: never hidden.
		"AA:BB:CC:DD:EE:14": {Addr: "AA:BB:CC:DD:EE:14", AddressType: "public", Alias: "AA-BB-CC-DD-EE-14", Paired: true},
	}
	a.deviceNames["AA:BB:CC:DD:EE:15"] = "Remembered Speaker"
	// Unnamed and classless now, but the hub remembers its name: shown.
	snap["AA:BB:CC:DD:EE:15"] = bluezDevice{Addr: "AA:BB:CC:DD:EE:15", AddressType: "public", Alias: "AA-BB-CC-DD-EE-15"}

	devs := a.devicesFromSnapshot(snap, map[string]bool{"AA:BB:CC:DD:EE:13": true})
	got := map[string]string{}
	for _, d := range devs {
		got[d.Addr] = d.Name
	}
	for _, addr := range []string{"AA:BB:CC:DD:EE:10", "AA:BB:CC:DD:EE:17"} {
		if _, ok := got[addr]; ok {
			t.Fatalf("%s: an unnamed device with no class or recognised icon should be hidden", addr)
		}
	}
	for addr, want := range map[string]string{
		"AA:BB:CC:DD:EE:11": "Headphones · EE:11",
		"AA:BB:CC:DD:EE:12": "Kitchen Plug",
		"AA:BB:CC:DD:EE:15": "Remembered Speaker",
		"AA:BB:CC:DD:EE:16": "Headphones · EE:16",
	} {
		if got[addr] != want {
			t.Errorf("%s: got %q, want %q", addr, got[addr], want)
		}
	}
	for _, addr := range []string{"AA:BB:CC:DD:EE:13", "AA:BB:CC:DD:EE:14"} {
		if _, ok := got[addr]; !ok {
			t.Errorf("%s must stay listed", addr)
		}
	}
}

func TestSnapshotFailureIsLoggedOncePerChange(t *testing.T) {
	a := &App{}
	count := func() int {
		a.logMu.Lock()
		defer a.logMu.Unlock()
		n := 0
		for _, l := range a.logs {
			if strings.Contains(l, "snapshot") {
				n++
			}
		}
		return n
	}
	err := errFake("busctl: exit status 1")
	a.noteSnapshotResult(err)
	a.noteSnapshotResult(err)
	a.noteSnapshotResult(err)
	if n := count(); n != 1 {
		t.Fatalf("a repeating failure must be logged once, got %d lines", n)
	}
	a.noteSnapshotResult(nil)
	a.noteSnapshotResult(nil)
	if n := count(); n != 2 {
		t.Fatalf("recovery must be logged once, got %d lines", n)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

func TestDoctorReportsBrokenDeviceList(t *testing.T) {
	fake := healthyFake(t)
	fake.busctl = "Failed to create new json object: Invalid argument"
	a := doctorApp(t, fake)
	c := checkByID(t, a.runDoctor(nil), "device-list")
	if c.Status != doctorWarning || !strings.Contains(c.Detail, "fewer names") {
		t.Fatalf("broken snapshot: %+v", c)
	}
	fake.busctl = healthyFake(t).busctl
	if c := checkByID(t, a.runDoctor(nil), "device-list"); c.Status != doctorOK {
		t.Fatalf("working snapshot: %+v", c)
	}
}

// Transports and the adapter address come from the same reply as the devices,
// so one busctl call replaces one bluetoothctl process per transport.
func TestParseBluezObjectsTransportsAndAdapter(t *testing.T) {
	out := `a{oa{sa{sv}}} 3 ` +
		`"/org/bluez/hci0" 1 "org.bluez.Adapter1" 1 "Address" s "11:22:33:44:55:66" ` +
		`"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_01/sep1/fd0" 1 "org.bluez.MediaTransport1" 7 ` +
		`"Device" o "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_01" "UUID" s "0000110b-0000-1000-8000-00805f9b34fb" ` +
		`"Codec" y 0 "Configuration" ay 4 33 21 2 53 "State" s "active" "Volume" q 100 "Delay" q 1500 ` +
		`"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_09/sep2/fd1" 1 "org.bluez.MediaTransport1" 4 ` +
		`"UUID" s "0000110a-0000-1000-8000-00805f9b34fb" "Codec" y 2 "Configuration" ay 6 128 0 140 0 0 0 "State" s "idle"`
	objs, err := parseBluezObjects(out)
	if err != nil {
		t.Fatal(err)
	}
	if objs.Adapter != "11:22:33:44:55:66" {
		t.Fatalf("adapter = %q", objs.Adapter)
	}
	if len(objs.Transports) != 2 {
		t.Fatalf("transports = %+v", objs.Transports)
	}
	in, out2 := objs.Transports[0], objs.Transports[1]
	if in.Addr != "AA:BB:CC:DD:EE:01" || !strings.Contains(in.UUID, "Audio Sink") || in.Codec != "SBC" || in.Rate != 44100 ||
		in.SBCMaxBitpool != 53 || in.State != "active" || !in.VolumeKnown || in.Volume != 100 || !in.DelayKnown || in.Delay != 1500 {
		t.Fatalf("input transport = %+v", in)
	}
	if out2.Addr != "AA:BB:CC:DD:EE:09" || !strings.Contains(out2.UUID, "Audio Source") || out2.Codec != "AAC" || out2.Rate != 48000 || out2.VolumeKnown || out2.DelayKnown {
		t.Fatalf("output transport = %+v", out2)
	}
}

func TestDescribeA2DPConfig(t *testing.T) {
	cases := []struct {
		codec byte
		conf  []byte
		name  string
		rate  int
	}{
		{0x00, []byte{0x11, 0x15, 2, 35}, "SBC", 48000},
		{0x02, []byte{0x80, 0x01, 0x0c, 0, 0, 0}, "AAC", 44100},
		{0xff, []byte{0x4f, 0, 0, 0, 0x01, 0, 0x22}, "aptX", 0},
		{0xff, []byte{0x2d, 0x01, 0, 0, 0xaa, 0, 0x34, 0x07}, "LDAC", 0},
		{0x04, nil, "0x04", 0},
	}
	for _, c := range cases {
		name, rate, _ := describeA2DPConfig(c.codec, c.conf)
		if name != c.name || rate != c.rate {
			t.Errorf("codec %#x %v: got %q %d, want %q %d", c.codec, c.conf, name, rate, c.name, c.rate)
		}
	}
}
