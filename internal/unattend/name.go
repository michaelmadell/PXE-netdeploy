package unattend

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"text/template"
)

// NameData is what a computer name pattern can use.
type NameData struct {
	Client    NameClient
	Variables map[string]string
}

// NameClient is the client's identity for naming.
type NameClient struct {
	Serial, MAC, SMBIOSUUID, Hostname string
}

var nameFuncs = template.FuncMap{
	"first": func(n int, s string) string {
		r := []rune(s)
		if n < len(r) {
			r = r[:n]
		}
		return string(r)
	},
	"last": func(n int, s string) string {
		r := []rune(s)
		if n < len(r) {
			r = r[len(r)-n:]
		}
		return string(r)
	},
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
}

// ParseName parses a computer name pattern.
func ParseName(pattern string) (*template.Template, error) {
	t, err := template.New("computerName").Funcs(nameFuncs).Option("missingkey=error").Parse(pattern)
	if err != nil {
		return nil, fmt.Errorf("computer name pattern: %w", err)
	}
	return t, nil
}

// ErrInvalidName: the rendered name breaks Windows' rules.
var ErrInvalidName = errors.New("invalid computer name")

// RenderName renders a pattern and checks the result: 1–15 letters, digits
// or hyphens, not all digits.
func RenderName(pattern string, d NameData) (string, error) {
	t, err := ParseName(pattern)
	if err != nil {
		return "", err
	}
	if d.Variables == nil {
		d.Variables = map[string]string{}
	}
	// Firmware that leaves the serial unset reports filler such as
	// "Undefined" ("LAB-ined"); the MAC without colons names it instead.
	if PlaceholderSerial(d.Client.Serial) {
		d.Client.Serial = strings.ReplaceAll(strings.ReplaceAll(d.Client.MAC, ":", ""), "-", "")
	}
	// A field the client doesn't have would silently render as nothing
	// ("LAB-"): render once with a marker in the empty fields to catch it.
	const marker = "\x00"
	probe := d
	var missing []string
	for _, f := range []struct {
		name string
		v    *string
	}{{"Serial", &probe.Client.Serial}, {"MAC", &probe.Client.MAC}, {"SMBIOSUUID", &probe.Client.SMBIOSUUID}, {"Hostname", &probe.Client.Hostname}} {
		if *f.v == "" {
			*f.v = marker
			missing = append(missing, f.name)
		}
	}
	var b bytes.Buffer
	if err := t.Execute(&b, probe); err != nil {
		return "", fmt.Errorf("computer name pattern: %w", err)
	}
	if strings.Contains(b.String(), marker) {
		return "", fmt.Errorf("%w: the pattern uses a client field this client doesn't have (empty: %s)", ErrInvalidName, strings.Join(missing, ", "))
	}
	name := strings.TrimSpace(b.String())
	if err := CheckName(name); err != nil {
		return name, err
	}
	return name, nil
}

// CheckName applies Windows' computer name rules: 1–15 letters, digits
// or hyphens, not all digits, no hyphen at either end.
func CheckName(name string) error {
	if len(name) < 1 || len(name) > 15 {
		return fmt.Errorf("%w: %q must be 1 to 15 characters", ErrInvalidName, name)
	}
	digits := true
	for _, c := range name {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '-':
			digits = false
		default:
			return fmt.Errorf("%w: %q may only have letters, digits and hyphens", ErrInvalidName, name)
		}
	}
	if digits {
		return fmt.Errorf("%w: %q can't be all digits", ErrInvalidName, name)
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("%w: %q can't start or end with a hyphen (it is also the DNS host name)", ErrInvalidName, name)
	}
	return nil
}

// placeholderSerials is filler firmware reports when the maker never set a
// serial (SMBIOS), compared ignoring case and surrounding spaces.
var placeholderSerials = map[string]bool{
	"undefined": true, "default string": true, "to be filled by o.e.m.": true, "to be filled by oem": true,
	"system serial number": true, "chassis serial number": true, "serial number": true, "not specified": true,
	"not applicable": true, "not available": true, "n/a": true, "na": true, "none": true, "null": true,
	"invalid": true, "unknown": true, "oem": true, "0123456789": true, "123456789": true, "1234567890": true,
}

// PlaceholderSerial reports a serial that isn't one: empty, firmware filler
// text, or one character repeated ("00000000", "XXXXXXXX", "        ").
func PlaceholderSerial(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || placeholderSerials[s] {
		return true
	}
	return strings.Count(s, s[:1]) == len(s)
}
