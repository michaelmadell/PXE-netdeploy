// Package autoinstall renders Ubuntu autoinstall user-data from templates
// (ADR 0035): Go text/template documents, as unattend templates are (ADR
// 0027), whose actions write YAML values.
package autoinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

// Format is the unattend template format these templates have.
const Format = "autoinstall"

// BuiltinID is the built-in autoinstall template's id.
const BuiltinID = "builtin-autoinstall"

// Template is a parsed autoinstall template.
type Template struct {
	Spec unattend.TemplateSpec
	t    *template.Template
}

// rawYAML is a value raw marks as already YAML.
type rawYAML string

const escapeFunc = "_ndYAML"

func pick(n int, s string, last bool) string {
	r := []rune(s)
	if n < len(r) {
		if last {
			r = r[len(r)-n:]
		} else {
			r = r[:n]
		}
	}
	return string(r)
}

var templateFuncs = template.FuncMap{
	"first": func(n int, s string) string { return pick(n, s, false) },
	"last":  func(n int, s string) string { return pick(n, s, true) },
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"raw":   func(v any) rawYAML { return rawYAML(fmt.Sprint(v)) },
	// shquote quotes a string for a POSIX shell.
	"shquote": func(v any) string { return "'" + strings.ReplaceAll(fmt.Sprint(v), "'", `'\''`) + "'" },
	"default": func(def, v any) any {
		if v == nil {
			return def
		}
		if rv := reflect.ValueOf(v); rv.IsZero() {
			return def
		}
		return v
	},
	escapeFunc: yamlValue,
}

// yamlValue writes v as a YAML value: strings double-quoted (JSON strings
// are YAML), numbers and booleans as they are, anything else as JSON (flow
// style). raw values are written unchanged.
func yamlValue(v any) string {
	switch x := v.(type) {
	case rawYAML:
		return string(x)
	case nil:
		return `""`
	case bool, int, int64, float64:
		return fmt.Sprint(x)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return `""`
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ParseTemplate parses an autoinstall template and makes every action that
// writes output a YAML value.
func ParseTemplate(spec unattend.TemplateSpec) (*Template, error) {
	if len(spec.Content) > unattend.MaxTemplateSize {
		return nil, fmt.Errorf("%w: the template is larger than %d KiB", unattend.ErrTemplateSyntax, unattend.MaxTemplateSize>>10)
	}
	t, err := template.New("user-data").Funcs(templateFuncs).Option("missingkey=error").Parse(spec.Content)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", unattend.ErrTemplateSyntax, err)
	}
	for _, x := range t.Templates() {
		if x.Tree != nil {
			escapeNode(x.Tree, x.Tree.Root)
		}
	}
	return &Template{Spec: spec, t: t}, nil
}

// escapeNode appends the escaper to every action that writes output (as
// unattend templates do).
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

// Execute renders the template.
func (t *Template) Execute(d Data) ([]byte, error) {
	var b bytes.Buffer
	if err := t.t.Execute(&b, d); err != nil {
		return nil, fmt.Errorf("%w: %v", unattend.ErrTemplateRender, err)
	}
	return b.Bytes(), nil
}
