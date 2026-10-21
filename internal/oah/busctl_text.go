package oah

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// busctl's text output is a complete, typed serialization: each value follows
// its D-Bus signature, containers carry an element count, variants carry their
// signature, and strings are quoted with C escapes. For example:
//
//	a{oa{sa{sv}}} 1 "/org/bluez/hci0/dev_AA_BB" 1 "org.bluez.Device1" 2
//	  "Name" s "Caf\303\251" "ManufacturerData" a{qv} 1 76 ay 2 1 2
//
// It is parsed here instead of busctl --json because the JSON mode refuses the
// whole reply when any dictionary has non-string keys, and BlueZ publishes
// ManufacturerData as a{qv} and AdvertisingData as a{yv}. One nearby phone or
// LE gadget advertising manufacturer data was enough to break the device list.
//
// Decoded values are string, int64, uint64, float64, bool, []any (arrays and
// structs) and map[string]any (dictionaries, keys formatted as strings).
type busctlParser struct {
	s   string
	pos int
}

func parseBusctlText(s string) (sig string, v any, err error) {
	p := &busctlParser{s: s}
	sig, err = p.bare()
	if err != nil {
		return "", nil, err
	}
	if v, err = p.value(sig); err != nil {
		return "", nil, err
	}
	if p.skipSpace(); p.pos != len(p.s) {
		return "", nil, fmt.Errorf("busctl: trailing data at offset %d", p.pos)
	}
	return sig, v, nil
}

func (p *busctlParser) skipSpace() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\n' || p.s[p.pos] == '\t' || p.s[p.pos] == '\r') {
		p.pos++
	}
}

// bare reads an unquoted token such as a number, a boolean or a signature.
func (p *busctlParser) bare() (string, error) {
	p.skipSpace()
	start := p.pos
	for p.pos < len(p.s) && p.s[p.pos] != ' ' && p.s[p.pos] != '\n' && p.s[p.pos] != '\t' && p.s[p.pos] != '\r' {
		p.pos++
	}
	if start == p.pos {
		return "", fmt.Errorf("busctl: unexpected end of output")
	}
	return p.s[start:p.pos], nil
}

// quoted reads a double-quoted string and undoes busctl's C escaping, which
// writes non-ASCII bytes as octal (\303\251 for "é").
func (p *busctlParser) quoted() (string, error) {
	p.skipSpace()
	if p.pos >= len(p.s) || p.s[p.pos] != '"' {
		return p.bare() // tolerate an unquoted string token
	}
	p.pos++
	var b []byte
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		p.pos++
		switch {
		case c == '"':
			return string(b), nil
		case c != '\\':
			b = append(b, c)
			continue
		}
		if p.pos >= len(p.s) {
			break
		}
		e := p.s[p.pos]
		p.pos++
		switch e {
		case 'a':
			b = append(b, '\a')
		case 'b':
			b = append(b, '\b')
		case 'f':
			b = append(b, '\f')
		case 'n':
			b = append(b, '\n')
		case 'r':
			b = append(b, '\r')
		case 't':
			b = append(b, '\t')
		case 'v':
			b = append(b, '\v')
		case 'x':
			n, used := 0, 0
			for used < 2 && p.pos < len(p.s) && isHex(p.s[p.pos]) {
				n = n*16 + hexVal(p.s[p.pos])
				p.pos++
				used++
			}
			b = append(b, byte(n))
		case '0', '1', '2', '3', '4', '5', '6', '7':
			n, used := int(e-'0'), 1
			for used < 3 && p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '7' {
				n = n*8 + int(p.s[p.pos]-'0')
				p.pos++
				used++
			}
			b = append(b, byte(n))
		default: // \\ \" \' and anything else stand for themselves
			b = append(b, e)
		}
	}
	return "", fmt.Errorf("busctl: unterminated string")
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
func hexVal(c byte) int {
	switch {
	case c >= 'a':
		return int(c-'a') + 10
	case c >= 'A':
		return int(c-'A') + 10
	}
	return int(c - '0')
}

// nextType splits the first complete type off a signature.
func nextType(sig string) (string, string, error) {
	if sig == "" {
		return "", "", fmt.Errorf("busctl: empty signature")
	}
	switch sig[0] {
	case 'a':
		elem, rest, err := nextType(sig[1:])
		return "a" + elem, rest, err
	case '(', '{':
		closer := byte(')')
		if sig[0] == '{' {
			closer = '}'
		}
		depth := 0
		for i := 0; i < len(sig); i++ {
			switch sig[i] {
			case '(', '{':
				depth++
			case ')', '}':
				depth--
				if depth == 0 {
					if sig[i] != closer {
						return "", "", fmt.Errorf("busctl: malformed signature %q", sig)
					}
					return sig[:i+1], sig[i+1:], nil
				}
			}
		}
		return "", "", fmt.Errorf("busctl: unbalanced signature %q", sig)
	}
	return sig[:1], sig[1:], nil
}

func (p *busctlParser) value(sig string) (any, error) {
	t, rest, err := nextType(sig)
	if err != nil {
		return nil, err
	}
	if rest != "" {
		return nil, fmt.Errorf("busctl: expected a single type, got %q", sig)
	}
	switch t[0] {
	case 'a':
		tok, err := p.bare()
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(tok)
		if err != nil || n < 0 || n > len(p.s) {
			return nil, fmt.Errorf("busctl: bad element count %q", tok)
		}
		elem := t[1:]
		if elem[0] == '{' {
			kt, vt, err := nextType(elem[1 : len(elem)-1])
			if err != nil {
				return nil, err
			}
			m := make(map[string]any, n)
			for i := 0; i < n; i++ {
				k, err := p.value(kt)
				if err != nil {
					return nil, err
				}
				v, err := p.value(vt)
				if err != nil {
					return nil, err
				}
				m[fmt.Sprint(k)] = v
			}
			return m, nil
		}
		list := make([]any, 0, n)
		for i := 0; i < n; i++ {
			v, err := p.value(elem)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return list, nil
	case '(':
		var fields []any
		inner := t[1 : len(t)-1]
		for inner != "" {
			ft, more, err := nextType(inner)
			if err != nil {
				return nil, err
			}
			v, err := p.value(ft)
			if err != nil {
				return nil, err
			}
			fields = append(fields, v)
			inner = more
		}
		return fields, nil
	case 'v':
		vsig, err := p.bare()
		if err != nil {
			return nil, err
		}
		return p.value(vsig)
	case 's', 'o', 'g':
		return p.quoted()
	case 'b':
		tok, err := p.bare()
		if err != nil {
			return nil, err
		}
		switch tok {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, fmt.Errorf("busctl: bad boolean %q", tok)
	case 'y', 'q', 'u', 't':
		tok, err := p.bare()
		if err != nil {
			return nil, err
		}
		return strconv.ParseUint(tok, 10, 64)
	case 'n', 'i', 'x', 'h':
		tok, err := p.bare()
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(tok, 10, 64)
	case 'd':
		tok, err := p.bare()
		if err != nil {
			return nil, err
		}
		return strconv.ParseFloat(tok, 64)
	}
	return nil, fmt.Errorf("busctl: unsupported type %q", t)
}

// bluezObjects is what one GetManagedObjects reply tells the hub: every device,
// every A2DP media transport and the address of the adapter in use.
type bluezObjects struct {
	Devices    map[string]bluezDevice
	Transports []Transport
	// Adapter is the address of hci0 (or of the only adapter), "" when unknown.
	Adapter string
}

// parseManagedObjects decodes busctl's text rendering of an ObjectManager
// GetManagedObjects reply (a{oa{sa{sv}}}) into the BlueZ devices it lists.
func parseManagedObjects(out string) (map[string]bluezDevice, error) {
	objs, err := parseBluezObjects(out)
	if err != nil {
		return nil, err
	}
	return objs.Devices, nil
}

func parseBluezObjects(out string) (bluezObjects, error) {
	res := bluezObjects{Devices: map[string]bluezDevice{}, Transports: []Transport{}}
	sig, v, err := parseBusctlText(strings.TrimSpace(out))
	if err != nil {
		return res, err
	}
	if sig != "a{oa{sa{sv}}}" {
		return res, fmt.Errorf("unexpected GetManagedObjects reply type %q", sig)
	}
	objects, _ := v.(map[string]any)
	adapters := map[string]string{}
	for path, ifv := range objects {
		ifaces, _ := ifv.(map[string]any)
		if props, ok := ifaces["org.bluez.Adapter1"].(map[string]any); ok {
			if s, _ := props["Address"].(string); cleanAddr(s) != "" {
				adapters[path] = cleanAddr(s)
			}
		}
		if props, ok := ifaces["org.bluez.MediaTransport1"].(map[string]any); ok {
			if t, ok := transportFromProps(path, props); ok {
				res.Transports = append(res.Transports, t)
			}
		}
		props, ok := ifaces["org.bluez.Device1"].(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { s, _ := props[k].(string); return strings.TrimSpace(s) }
		boolean := func(k string) bool { b, _ := props[k].(bool); return b }
		var d bluezDevice
		d.Addr = strings.ToUpper(cleanAddr(str("Address")))
		if d.Addr == "" {
			continue
		}
		d.AddressType = str("AddressType")
		d.Name = str("Name")
		d.Alias = str("Alias")
		d.Icon = str("Icon")
		if c, ok := props["Class"].(uint64); ok {
			d.Class = uint32(c)
		}
		if list, ok := props["UUIDs"].([]any); ok {
			for _, u := range list {
				if s, ok := u.(string); ok {
					d.UUIDs = append(d.UUIDs, s)
				}
			}
		}
		if r, ok := props["RSSI"].(int64); ok {
			d.RSSI, d.HasRSSI = int(r), true
		}
		d.Paired = boolean("Paired")
		d.Bonded = boolean("Bonded")
		_, d.HasBonded = props["Bonded"]
		d.Trusted = boolean("Trusted")
		d.Connected = boolean("Connected")
		res.Devices[d.Addr] = d
	}
	if addr, ok := adapters["/org/bluez/hci0"]; ok {
		res.Adapter = addr
	} else if len(adapters) == 1 {
		for _, addr := range adapters {
			res.Adapter = addr
		}
	}
	// Map iteration order is random; keep the transport list stable for the UI
	// and for the state change detection that compares successive builds.
	sort.Slice(res.Transports, func(i, j int) bool { return res.Transports[i].Path < res.Transports[j].Path })
	return res, nil
}

// transportFromProps describes a MediaTransport1 object the way the
// bluetoothctl transport.show parser does, so both sources are interchangeable.
func transportFromProps(path string, props map[string]any) (Transport, bool) {
	t := Transport{Path: path, Addr: addrFromBluezPath(path)}
	if dev, _ := props["Device"].(string); t.Addr == "" && dev != "" {
		t.Addr = addrFromBluezPath(dev)
	}
	uuid, _ := props["UUID"].(string)
	switch strings.ToLower(uuid) {
	case uuidA2DPSink:
		t.UUID = "Audio Sink (" + uuid + ")"
	case uuidA2DPSource:
		t.UUID = "Audio Source (" + uuid + ")"
	default:
		// LE Audio and other transports are not part of the A2DP routing.
		return t, false
	}
	t.State, _ = props["State"].(string)
	if v, ok := props["Volume"].(uint64); ok {
		t.Volume, t.VolumeKnown = int(v), true
	}
	if v, ok := props["Delay"].(uint64); ok {
		t.Delay, t.DelayKnown = int(v), true
	}
	codec, _ := props["Codec"].(uint64)
	var conf []byte
	if list, ok := props["Configuration"].([]any); ok {
		for _, b := range list {
			if n, ok := b.(uint64); ok {
				conf = append(conf, byte(n))
			}
		}
	}
	t.Codec, t.Rate, t.SBCMaxBitpool = describeA2DPConfig(byte(codec), conf)
	return t, true
}

// describeA2DPConfig names an A2DP codec and reads the negotiated sample rate
// (and the SBC bitpool ceiling) from its configuration blob, per the A2DP
// specification's codec information elements.
func describeA2DPConfig(codec byte, conf []byte) (name string, rate, maxBitpool int) {
	pick := func(b byte, table map[byte]int) int {
		for bit, r := range table {
			if b&bit != 0 {
				return r
			}
		}
		return 0
	}
	switch codec {
	case 0x00:
		name = "SBC"
		if len(conf) >= 4 {
			rate = pick(conf[0]&0xf0, map[byte]int{0x80: 16000, 0x40: 32000, 0x20: 44100, 0x10: 48000})
			maxBitpool = int(conf[3])
		}
	case 0x01:
		name = "MP3"
	case 0x02:
		name = "AAC"
		if len(conf) >= 3 {
			if rate = pick(conf[2]&0xf0, map[byte]int{0x80: 48000, 0x40: 64000, 0x20: 88200, 0x10: 96000}); rate == 0 {
				rate = pick(conf[1], map[byte]int{0x01: 44100, 0x02: 32000, 0x04: 24000, 0x08: 22050, 0x10: 16000, 0x20: 12000, 0x40: 11025, 0x80: 8000})
			}
		}
	case 0xff:
		name = "Vendor codec"
		if len(conf) >= 6 {
			vendor := uint32(conf[0]) | uint32(conf[1])<<8 | uint32(conf[2])<<16 | uint32(conf[3])<<24
			id := uint16(conf[4]) | uint16(conf[5])<<8
			switch {
			case vendor == 0x4f && id == 0x01:
				name = "aptX"
			case vendor == 0xd7 && id == 0x24:
				name = "aptX HD"
			case vendor == 0x12d && id == 0xaa:
				name = "LDAC"
			}
		}
	default:
		name = fmt.Sprintf("0x%02x", codec)
	}
	return name, rate, maxBitpool
}
