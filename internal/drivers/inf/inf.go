// Package inf parses Windows driver INF files (ADR 0019): the syntax
// (UTF-16LE, UTF-8 or ANSI; sections, comments, quoting, line continuation,
// %token% substitution from [Strings]) and the parts driver import needs:
// class, provider, DriverVer, catalog files, architectures and target OS
// versions from the [Manufacturer] decorations, hardware IDs from the
// models sections, and the files listed in [SourceDisksFiles].
package inf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxSize is the largest INF parsed (real ones are well under 1 MiB).
const MaxSize = 16 << 20

// ErrNotINF: the data has no [Version] section with a Signature.
var ErrNotINF = errors.New("inf: not a driver INF (no [Version] Signature)")

// Line is one entry of a section: an optional key and comma-separated
// values, unquoted, with %tokens% substituted.
type Line struct {
	Key    string
	HasKey bool
	Values []string
}

// File is a parsed INF.
type File struct {
	sections map[string][]Line // lower-case name -> lines
	strings  map[string]string // lower-case token -> value
}

// Parse decodes and parses an INF.
func Parse(b []byte) (*File, error) {
	if len(b) > MaxSize {
		return nil, fmt.Errorf("inf: %d bytes is too large", len(b))
	}
	text := decode(b)
	f := &File{sections: map[string][]Line{}, strings: map[string]string{}}
	raw := map[string][]rawLine{}
	cur := ""
	for _, l := range logicalLines(text) {
		if strings.HasPrefix(l, "[") {
			if end := strings.Index(l, "]"); end > 0 {
				cur = strings.ToLower(strings.TrimSpace(l[1:end]))
				if _, ok := raw[cur]; !ok {
					raw[cur] = nil
				}
				continue
			}
		}
		raw[cur] = append(raw[cur], splitLine(l))
	}
	// [Strings], then the US English variant over it.
	for _, name := range []string{"strings", "strings.0409", "strings.409"} {
		for _, rl := range raw[name] {
			if rl.hasKey && len(rl.values) > 0 {
				f.strings[strings.ToLower(rl.key)] = rl.values[0]
			}
		}
	}
	for name, lines := range raw {
		out := make([]Line, 0, len(lines))
		for _, rl := range lines {
			ln := Line{Key: f.subst(rl.key), HasKey: rl.hasKey, Values: make([]string, len(rl.values))}
			for i, v := range rl.values {
				ln.Values[i] = f.subst(v)
			}
			out = append(out, ln)
		}
		f.sections[name] = out
	}
	if f.Value("Version", "Signature") == "" {
		return nil, ErrNotINF
	}
	return f, nil
}

// Section returns a section's lines (case-insensitive name), nil if absent.
func (f *File) Section(name string) []Line { return f.sections[strings.ToLower(name)] }

// HasSection reports whether the section exists (possibly empty).
func (f *File) HasSection(name string) bool {
	_, ok := f.sections[strings.ToLower(name)]
	return ok
}

// Value returns the first value of key in section ("" if absent).
func (f *File) Value(section, key string) string {
	for _, l := range f.Section(section) {
		if l.HasKey && strings.EqualFold(l.Key, key) && len(l.Values) > 0 {
			return l.Values[0]
		}
	}
	return ""
}

// subst replaces %token% with its [Strings] value; %% is a literal %.
func (f *File) subst(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i+1:], '%')
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		tok := s[i+1 : i+1+j]
		switch v, ok := f.strings[strings.ToLower(tok)]; {
		case tok == "":
			b.WriteByte('%')
		case ok:
			b.WriteString(v)
		default:
			b.WriteString(s[i : i+2+j]) // unknown: keep %tok%
		}
		s = s[i+2+j:]
	}
}

// decode handles the encodings INFs come in: UTF-16LE/BE with a BOM,
// UTF-8 with or without one, else Windows-1252.
func decode(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xff, 0xfe}):
		return utf16String(b[2:], binary.LittleEndian)
	case bytes.HasPrefix(b, []byte{0xfe, 0xff}):
		return utf16String(b[2:], binary.BigEndian)
	case bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}):
		return string(b[3:])
	case utf8.Valid(b):
		return string(b)
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
		if c >= 0x80 && c < 0xa0 {
			r[i] = cp1252[c-0x80]
		}
	}
	return string(r)
}

var cp1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8d, 'Ž', 0x8f,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9d, 'ž', 'Ÿ',
}

func utf16String(b []byte, order binary.ByteOrder) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// logicalLines strips comments and joins continuation lines (a trailing
// backslash outside quotes).
func logicalLines(text string) []string {
	var out []string
	var pending strings.Builder
	for _, physical := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l := stripComment(physical)
		trimmed := strings.TrimRight(l, " \t\r")
		if strings.HasSuffix(trimmed, "\\") && !strings.HasSuffix(trimmed, "\\\\") {
			pending.WriteString(strings.TrimSuffix(trimmed, "\\"))
			continue
		}
		pending.WriteString(l)
		if s := strings.TrimSpace(pending.String()); s != "" {
			out = append(out, s)
		}
		pending.Reset()
	}
	if s := strings.TrimSpace(pending.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func stripComment(l string) string {
	inQuote := false
	for i, c := range l {
		switch c {
		case '"':
			inQuote = !inQuote
		case ';':
			if !inQuote {
				return l[:i]
			}
		}
	}
	return l
}

type rawLine struct {
	key    string
	hasKey bool
	values []string
}

// splitLine splits "key = v1, "v,2", v3" outside quotes.
func splitLine(l string) rawLine {
	var rl rawLine
	rest := l
	if i := indexOutsideQuotes(l, '='); i >= 0 {
		rl.key, rl.hasKey, rest = unquote(strings.TrimSpace(l[:i])), true, l[i+1:]
	}
	start, inQuote := 0, false
	for i := 0; i <= len(rest); i++ {
		if i < len(rest) && rest[i] == '"' {
			inQuote = !inQuote
			continue
		}
		if i == len(rest) || (rest[i] == ',' && !inQuote) {
			rl.values = append(rl.values, unquote(strings.TrimSpace(rest[start:i])))
			start = i + 1
		}
	}
	if !rl.hasKey && len(rl.values) == 1 && rl.values[0] == "" {
		rl.values = nil
	}
	return rl
}

func indexOutsideQuotes(s string, c byte) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
		case s[i] == c && !inQuote:
			return i
		}
	}
	return -1
}

// unquote removes quotes around parts of a value; "" inside quotes is ".
func unquote(s string) string {
	if !strings.Contains(s, `"`) {
		return s
	}
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			if inQuote && i+1 < len(s) && s[i+1] == '"' {
				b.WriteByte('"')
				i++
				continue
			}
			inQuote = !inQuote
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Driver is what driver import records about an INF.
type Driver struct {
	Class            string
	ClassGUID        string
	Provider         string
	Version          string
	Date             *time.Time
	Architectures    []string // "x64", "arm64": from NTamd64 / NTarm64 decorations
	TargetOSVersions []string // e.g. "10.0...22000"; empty: no restriction
	HardwareIDs      []string
	// Catalogs by architecture ("x64", "arm64"); the same file for both
	// when only CatalogFile / CatalogFile.NT is given.
	Catalogs map[string]string
	// Files the INF installs, relative to its directory ('/'-separated),
	// from [SourceDisksFiles] for x64/arm64 and [SourceDisksNames].
	Files []string
}

// Read parses an INF and extracts the Driver.
func Read(b []byte) (Driver, *File, error) {
	f, err := Parse(b)
	if err != nil {
		return Driver{}, nil, err
	}
	d := Driver{
		Class:     f.Value("Version", "Class"),
		ClassGUID: f.Value("Version", "ClassGuid"),
		Provider:  f.Value("Version", "Provider"),
		Catalogs:  map[string]string{},
	}
	if dv := f.Section("Version"); dv != nil {
		for _, l := range dv {
			if l.HasKey && strings.EqualFold(l.Key, "DriverVer") {
				if len(l.Values) > 0 {
					if t, err := time.Parse("01/02/2006", strings.TrimSpace(l.Values[0])); err == nil {
						d.Date = &t
					}
				}
				if len(l.Values) > 1 {
					d.Version = l.Values[1]
				}
			}
		}
	}
	d.Catalogs = catalogs(f)
	d.Architectures, d.TargetOSVersions, d.HardwareIDs = models(f)
	d.Files = files(f)
	return d, f, nil
}

// archOf maps an INF architecture decoration to ours. x64 and arm64
// drivers must use NTamd64 / NTarm64 models sections; undecorated and plain
// NT sections only apply to x86, so they aren't ours.
func archOf(s string) (string, bool) {
	switch strings.ToLower(s) {
	case "amd64":
		return "x64", true
	case "arm64":
		return "arm64", true
	}
	return "", false // x86, ia64, arm: not ours
}

// splitDecoration splits "NTamd64.10.0...22000" into "amd64" and
// "10.0...22000". ok is false if it isn't an NT decoration.
func splitDecoration(dec string) (arch, os string, ok bool) {
	dec = strings.TrimSpace(dec)
	if len(dec) < 2 || !strings.EqualFold(dec[:2], "nt") {
		return "", "", false
	}
	rest := dec[2:]
	arch, os, _ = strings.Cut(rest, ".")
	return arch, os, true
}

func catalogs(f *File) map[string]string {
	out := map[string]string{}
	var base string
	for _, l := range f.Section("Version") {
		if !l.HasKey || len(l.Values) == 0 || l.Values[0] == "" {
			continue
		}
		k := strings.ToLower(l.Key)
		switch {
		case k == "catalogfile", k == "catalogfile.nt":
			base = l.Values[0]
		case strings.HasPrefix(k, "catalogfile.nt"):
			if a, ok := archOf(strings.TrimPrefix(k, "catalogfile.nt")); ok {
				out[a] = l.Values[0]
			}
		}
	}
	if base != "" {
		for _, a := range []string{"x64", "arm64"} {
			if _, ok := out[a]; !ok {
				out[a] = base
			}
		}
	}
	return out
}

// models walks [Manufacturer] and the models sections it names.
func models(f *File) (archs, osVersions, hwids []string) {
	seenArch, seenOS, seenID := map[string]bool{}, map[string]bool{}, map[string]bool{}
	addArch := func(a string) {
		if !seenArch[a] {
			seenArch[a] = true
			archs = append(archs, a)
		}
	}
	for _, l := range f.Section("Manufacturer") {
		vals := l.Values
		if len(vals) == 0 || vals[0] == "" {
			continue
		}
		base := vals[0]
		var sections []string
		for _, dec := range vals[1:] {
			arch, os, ok := splitDecoration(dec)
			if !ok {
				continue
			}
			a, ours := archOf(arch)
			if !ours {
				continue
			}
			addArch(a)
			if os != "" && !seenOS[os] {
				seenOS[os] = true
				osVersions = append(osVersions, os)
			}
			sections = append(sections, base+"."+strings.TrimSpace(dec))
		}
		for _, sec := range sections {
			for _, ml := range f.Section(sec) {
				if !ml.HasKey || len(ml.Values) < 2 {
					continue
				}
				for _, id := range ml.Values[1:] {
					id = strings.TrimSpace(id)
					if id != "" && !seenID[strings.ToLower(id)] {
						seenID[strings.ToLower(id)] = true
						hwids = append(hwids, id)
					}
				}
			}
		}
	}
	slices.Sort(archs)
	return archs, osVersions, hwids
}

// files lists [SourceDisksFiles] (undecorated, .amd64, .arm64) as paths
// relative to the INF, using [SourceDisksNames] for disk paths.
func files(f *File) []string {
	disks := map[string]string{}
	for _, sec := range []string{"SourceDisksNames", "SourceDisksNames.amd64", "SourceDisksNames.arm64"} {
		for _, l := range f.Section(sec) {
			if l.HasKey && len(l.Values) >= 4 {
				disks[strings.TrimSpace(l.Key)] = l.Values[3]
			} else if l.HasKey {
				if _, ok := disks[strings.TrimSpace(l.Key)]; !ok {
					disks[strings.TrimSpace(l.Key)] = ""
				}
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, sec := range []string{"SourceDisksFiles", "SourceDisksFiles.amd64", "SourceDisksFiles.arm64"} {
		for _, l := range f.Section(sec) {
			if !l.HasKey || l.Key == "" {
				continue
			}
			dir, sub := "", ""
			if len(l.Values) > 0 {
				dir = disks[strings.TrimSpace(l.Values[0])]
			}
			if len(l.Values) > 1 {
				sub = l.Values[1]
			}
			p := clean(path.Join(slash(dir), slash(sub), slash(l.Key)))
			if p != "" && !seen[strings.ToLower(p)] {
				seen[strings.ToLower(p)] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func slash(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), `\`, "/") }

func clean(p string) string {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "." {
		return ""
	}
	return p
}
