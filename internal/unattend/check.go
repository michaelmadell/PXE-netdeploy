package unattend

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Finding is one result of checking a rendered unattend file (ADR 0027).
type Finding struct {
	Severity string // error or warning
	Code     string
	Message  string
	Line     int // of the rendered file; 0 when not known
	// Sample names the sample render that found it (CheckTemplate), which
	// also prefixes Message.
	Sample string
}

// Finding severities.
const (
	SevError   = "error"
	SevWarning = "warning"
)

const unattendNS = "urn:schemas-microsoft-com:unattend"

var validPasses = map[string]bool{
	"windowsPE": true, "offlineServicing": true, "generalize": true, "specialize": true,
	"auditSystem": true, "auditUser": true, "oobeSystem": true,
}

// reservedAccounts are built-in Windows accounts: Setup won't create a
// local account with one of these names (Phase 4 findings).
var reservedAccounts = map[string]bool{"administrator": true, "guest": true, "defaultaccount": true, "wdagutilityaccount": true}

// IsReservedAccount reports whether name is a built-in account's.
func IsReservedAccount(name string) bool {
	return reservedAccounts[strings.ToLower(strings.TrimSpace(name))]
}

type node struct {
	name     xml.Name
	attrs    []xml.Attr
	children []*node
	text     strings.Builder
	line     int
}

func (n *node) attr(name string) string { return attr(n.attrs, name) }

// child returns the first child element called local.
func (n *node) child(local string) *node {
	for _, c := range n.children {
		if c.name.Local == local {
			return c
		}
	}
	return nil
}

// path follows child elements; nil if any is missing.
func (n *node) path(locals ...string) *node {
	for _, l := range locals {
		if n = n.child(l); n == nil {
			return nil
		}
	}
	return n
}

func (n *node) value() string { return strings.TrimSpace(n.text.String()) }

// walk calls f for n and every element under it.
func (n *node) walk(f func(*node)) {
	f(n)
	for _, c := range n.children {
		c.walk(f)
	}
}

// parseDoc reads an unattend file into elements, with the XML
// declaration's encoding.
func parseDoc(b []byte) (root *node, encoding string, err error, line int) {
	var starts []int // offsets where lines start
	starts = append(starts, 0)
	for i, c := range b {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	lineAt := func(off int64) int { return sort.SearchInts(starts, int(off)+1) }
	dec := newDecoder(b)
	var stack []*node
	for {
		off := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			var se *xml.SyntaxError
			if errors.As(err, &se) {
				return nil, "", err, se.Line
			}
			return nil, "", err, lineAt(off)
		}
		switch t := tok.(type) {
		case xml.ProcInst:
			if t.Target == "xml" {
				s := string(t.Inst)
				if i := strings.Index(s, "encoding="); i >= 0 && len(s) > i+10 {
					q := s[i+9]
					if j := strings.IndexByte(s[i+10:], q); j >= 0 {
						encoding = s[i+10 : i+10+j]
					}
				}
			}
		case xml.StartElement:
			n := &node{name: t.Name, attrs: t.Attr, line: lineAt(off)}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		}
	}
	if root == nil {
		return nil, "", errors.New("no root element"), 1
	}
	return root, encoding, nil, 0
}

// components returns the pass's components called name.
func components(root *node, pass, name string) []*node {
	var out []*node
	for _, s := range root.children {
		if s.name.Local != "settings" || s.attr("pass") != pass {
			continue
		}
		for _, c := range s.children {
			if c.name.Local == "component" && c.attr("name") == name {
				out = append(out, c)
			}
		}
	}
	return out
}

// Check checks a rendered unattend file against what the data says it must
// contain (ADR 0027).
func Check(out []byte, d Data) []Finding {
	var fs []Finding
	add := func(sev, code string, line int, format string, a ...any) {
		fs = append(fs, Finding{Severity: sev, Code: code, Message: fmt.Sprintf(format, a...), Line: line})
	}
	root, enc, err, line := parseDoc(out)
	if err != nil {
		add(SevError, "unattend_xml", line, "The unattend file isn't well-formed XML: %v.", err)
		return fs
	}
	if enc != "" && !strings.EqualFold(enc, "utf-8") && !strings.EqualFold(enc, "utf8") {
		add(SevError, "unattend_encoding", 1, "The XML declaration says encoding %q; the file is served as UTF-8, so declare utf-8 or leave it out.", enc)
	}
	if root.name.Local != "unattend" || root.name.Space != unattendNS {
		add(SevError, "unattend_root", root.line, "The root element must be unattend in the %s namespace.", unattendNS)
		return fs
	}
	seen := map[string]bool{}
	for _, s := range root.children {
		if s.name.Local != "settings" {
			continue
		}
		p := s.attr("pass")
		switch {
		case !validPasses[p]:
			add(SevError, "unattend_pass", s.line, "Unknown pass %q.", p)
		case seen[p]:
			add(SevError, "unattend_pass", s.line, "Pass %q appears more than once.", p)
		}
		seen[p] = true
	}
	root.walk(func(n *node) {
		switch n.name.Local {
		case "component":
			if a := n.attr("processorArchitecture"); a != "" && a != d.Arch {
				add(SevError, "unattend_architecture", n.line, "Component %s is for %s but the install image is %s; Setup would skip it. Use {{.Arch}}.", n.attr("name"), a, d.Arch)
			}
		case "LocalAccount":
			if nm := n.child("Name"); nm != nil && IsReservedAccount(nm.value()) {
				add(SevError, "reserved_account_name", nm.line, "%q is a built-in Windows account; Setup won't create it. Choose another name.", nm.value())
			}
		}
	})

	// The identity mode's settings, as the data says they must be.
	shell := components(root, "oobeSystem", "Microsoft-Windows-Shell-Setup")
	for _, a := range d.Identity.Accounts {
		found := false
		for _, c := range shell {
			if las := c.path("UserAccounts", "LocalAccounts"); las != nil {
				for _, la := range las.children {
					if nm := la.child("Name"); la.name.Local == "LocalAccount" && nm != nil && nm.value() == a.Name {
						found = true
					}
				}
			}
		}
		if !found {
			add(SevError, "unattend_missing_account", 0, "Account %q isn't in oobeSystem's Microsoft-Windows-Shell-Setup UserAccounts/LocalAccounts; render {{range .Identity.Accounts}}.", a.Name)
		}
	}
	if al := d.Identity.AutoLogon; al != nil {
		found := false
		for _, c := range shell {
			if u := c.path("AutoLogon", "Username"); u != nil && u.value() == al.Username {
				found = true
			}
		}
		if !found {
			add(SevError, "unattend_missing_autologon", 0, "Auto-logon for %q isn't in oobeSystem's Microsoft-Windows-Shell-Setup AutoLogon; render {{with .Identity.AutoLogon}}.", al.Username)
		}
	}
	join := components(root, "specialize", "Microsoft-Windows-UnattendedJoin")
	if j := d.Identity.Join; j != nil {
		found := false
		for _, c := range join {
			if jd := c.path("Identification", "JoinDomain"); jd != nil && jd.value() == j.JoinDomain && c.path("Identification", "Credentials") != nil {
				found = true
			}
		}
		if !found {
			add(SevError, "unattend_missing_join", 0, "The domain join isn't in specialize's Microsoft-Windows-UnattendedJoin Identification (JoinDomain %s and Credentials); render {{with .Identity.Join}}. Without it Setup finishes unjoined.", j.JoinDomain)
		}
	}
	if o := d.Identity.ODJ; o != nil {
		found := false
		for _, c := range join {
			if ad := c.path("Identification", "Provisioning", "AccountData"); ad != nil && ad.value() == o.AccountData {
				found = true
			}
		}
		if !found {
			add(SevError, "unattend_missing_join", 0, "The offline join isn't in specialize's Microsoft-Windows-UnattendedJoin Identification/Provisioning/AccountData; render {{with .Identity.ODJ}}. Without it Setup finishes unjoined.")
		}
	}
	var names []*node
	for _, c := range components(root, "specialize", "Microsoft-Windows-Shell-Setup") {
		if n := c.child("ComputerName"); n != nil {
			names = append(names, n)
		}
	}
	if d.Identity.ODJ != nil {
		if len(names) > 0 {
			add(SevWarning, "unattend_computer_name_with_odj", names[0].line, "The file sets ComputerName, but with offline domain join the ODJ blob names the computer.")
		}
	} else if d.ComputerName != "" {
		found := false
		for _, n := range names {
			found = found || n.value() == d.ComputerName
		}
		if !found {
			add(SevWarning, "unattend_ignores_computer_name", 0, "The profile names the computer (%s) but the file doesn't use it; render {{.ComputerName}} in specialize's Microsoft-Windows-Shell-Setup ComputerName.", d.ComputerName)
		}
	}
	return fs
}
