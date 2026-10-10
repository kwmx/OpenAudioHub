package oah

import (
	"fmt"
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

// parseManagedObjects decodes busctl's text rendering of an ObjectManager
// GetManagedObjects reply (a{oa{sa{sv}}}) into the BlueZ devices it lists.
func parseManagedObjects(out string) (map[string]bluezDevice, error) {
	sig, v, err := parseBusctlText(strings.TrimSpace(out))
	if err != nil {
		return nil, err
	}
	if sig != "a{oa{sa{sv}}}" {
		return nil, fmt.Errorf("unexpected GetManagedObjects reply type %q", sig)
	}
	objects, _ := v.(map[string]any)
	res := map[string]bluezDevice{}
	for _, ifv := range objects {
		ifaces, _ := ifv.(map[string]any)
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
		res[d.Addr] = d
	}
	return res, nil
}
