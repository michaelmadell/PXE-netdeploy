package unattend

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"
)

// Unattend templates (ADR 0027): a Go text/template that writes the whole
// unattend file from Data. Every action's output is XML-escaped unless it
// goes through raw.

// BuiltinID is the built-in template's id.
const BuiltinID = "builtin"

// MaxTemplateSize is the largest template content accepted.
const MaxTemplateSize = 256 << 10

var (
	// ErrTemplateSyntax: the template doesn't parse.
	ErrTemplateSyntax = errors.New("template syntax")
	// ErrTemplateRender: the template failed while rendering.
	ErrTemplateRender = errors.New("template render")
)

// TemplateSpec is an unattend template as stored, and as copied into a
// deployment when it is created.
type TemplateSpec struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ETag        string `json:"etag,omitempty"`
	BuiltIn     bool   `json:"builtIn"`
	// Format is "" or "unattend" (Windows) or "autoinstall" (ADR 0035).
	Format            string            `json:"format,omitempty"`
	Content           string            `json:"content"`
	IdentityModes     []string          `json:"identityModes"`
	VariableDefaults  map[string]string `json:"variableDefaults,omitempty"`
	RequiredVariables []string          `json:"requiredVariables,omitempty"`
}

// Builtin is the shipped template: the Phase 4 unattend (ADR 0026).
func Builtin() TemplateSpec {
	return TemplateSpec{
		ID: BuiltinID, Name: "Built-in", BuiltIn: true, Content: builtinContent,
		Description:   "The unattend netdeploy ships: computer name, locales, product key, and the identity mode's accounts or domain join. Copy it to customise.",
		IdentityModes: []string{ModeNone, ModeLocal, ModeAD},
	}
}

// Supports reports whether the template renders identity mode m.
func (t TemplateSpec) Supports(m string) bool {
	for _, x := range t.IdentityModes {
		if x == m {
			return true
		}
	}
	return false
}

// Template is a parsed template.
type Template struct {
	Spec TemplateSpec
	t    *template.Template
}

// rawXML is a value raw marks as already XML.
type rawXML string

const escapeFunc = "_ndEscapeXML"

var templateFuncs = template.FuncMap{
	"first": nameFuncs["first"],
	"last":  nameFuncs["last"],
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"raw":   func(v any) rawXML { return rawXML(fmt.Sprint(v)) },
	"default": func(def, v any) any {
		if v == nil {
			return def
		}
		if rv := reflect.ValueOf(v); rv.IsZero() {
			return def
		}
		return v
	},
	escapeFunc: func(v any) string {
		if r, ok := v.(rawXML); ok {
			return string(r)
		}
		return xmlEscape(fmt.Sprint(v))
	},
}

// ParseTemplate parses a template's content and makes every action that
// writes output escape it.
func ParseTemplate(spec TemplateSpec) (*Template, error) {
	if len(spec.Content) > MaxTemplateSize {
		return nil, fmt.Errorf("%w: the template is larger than %d KiB", ErrTemplateSyntax, MaxTemplateSize>>10)
	}
	t, err := template.New("unattend").Funcs(templateFuncs).Option("missingkey=error").Parse(spec.Content)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTemplateSyntax, err)
	}
	for _, x := range t.Templates() {
		if x.Tree != nil {
			escapeNode(x.Tree, x.Tree.Root)
		}
	}
	return &Template{Spec: spec, t: t}, nil
}

// escapeNode appends the escaper to every action that writes output, as
// html/template does. Actions that only declare or assign variables write
// nothing; {{template}} runs a template whose own actions are escaped.
func escapeNode(tree *parse.Tree, n parse.Node) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			escapeNode(tree, c)
		}
	case *parse.ActionNode:
		if len(n.Pipe.Decl) > 0 {
			return
		}
		id := parse.NewIdentifier(escapeFunc).SetTree(tree).SetPos(n.Pos)
		n.Pipe.Cmds = append(n.Pipe.Cmds, &parse.CommandNode{NodeType: parse.NodeCommand, Pos: n.Pos, Args: []parse.Node{id}})
	case *parse.IfNode:
		escapeNode(tree, n.List)
		escapeNode(tree, n.ElseList)
	case *parse.RangeNode:
		escapeNode(tree, n.List)
		escapeNode(tree, n.ElseList)
	case *parse.WithNode:
		escapeNode(tree, n.List)
		escapeNode(tree, n.ElseList)
	}
}

// Execute renders the template. Unless the install method is smb, the
// windowsPE pass is removed: Setup has already run it (ADR 0027).
func (t *Template) Execute(d Data) ([]byte, error) {
	var b bytes.Buffer
	if err := t.t.Execute(&b, d); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTemplateRender, err)
	}
	out := b.Bytes()
	if d.InstallMethod != "smb" {
		out = stripPass(out, "windowsPE")
	}
	return out, nil
}

// newDecoder reads any declared encoding as is; Check reports one that
// isn't UTF-8.
func newDecoder(b []byte) *xml.Decoder {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	return d
}

// stripPass removes the settings elements for pass from an unattend file,
// with their lines when they are alone on them. A file that doesn't parse
// is returned as is.
func stripPass(b []byte, pass string) []byte {
	dec := newDecoder(b)
	depth, start := 0, int64(-1)
	var spans [][2]int
	for {
		off := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return b
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 2 && t.Name.Local == "settings" && attr(t.Attr, "pass") == pass {
				start = off
			}
		case xml.EndElement:
			if depth == 2 && start >= 0 {
				spans = append(spans, [2]int{int(start), int(dec.InputOffset())})
				start = -1
			}
			depth--
		}
	}
	if len(spans) == 0 {
		return b
	}
	out := append([]byte(nil), b...)
	for i := len(spans) - 1; i >= 0; i-- {
		s, e := spans[i][0], spans[i][1]
		ls, le := s, e
		for ls > 0 && (out[ls-1] == ' ' || out[ls-1] == '\t') {
			ls--
		}
		for le < len(out) && (out[le] == ' ' || out[le] == '\t' || out[le] == '\r') {
			le++
		}
		if (ls == 0 || out[ls-1] == '\n') && le < len(out) && out[le] == '\n' {
			s, e = ls, le+1
		}
		out = append(out[:s], out[e:]...)
	}
	return out
}

func attr(as []xml.Attr, name string) string {
	for _, a := range as {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
