package unattend

import (
	"errors"
	"strings"
	"testing"
)

func mustTemplate(t *testing.T, content string, modes ...string) *Template {
	t.Helper()
	if len(modes) == 0 {
		modes = []string{ModeNone}
	}
	tp, err := ParseTemplate(TemplateSpec{Name: "t", Content: content, IdentityModes: modes})
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

func TestEscaping(t *testing.T) {
	tp := mustTemplate(t, `{{.Variables.v}}|{{raw .Variables.v}}|{{ $x := .Variables.v }}{{$x}}|{{if true}}{{.Variables.v | upper}}{{end}}|{{range .Identity.Accounts}}{{.Name}}{{end}}|{{define "d"}}{{.}}{{end}}{{template "d" .Variables.v}}|{{default "dflt" (index .Variables "missing")}}|{{with .Variables.v}}{{.}}{{end}}`)
	d := Data{Variables: map[string]string{"v": `<a & "b">`}, Identity: IdentityData{Accounts: []UserAccount{{Name: "x<y"}}}}
	b, err := tp.Execute(d)
	if err != nil {
		t.Fatal(err)
	}
	want := `&lt;a &amp; &#34;b&#34;&gt;|<a & "b">|&lt;a &amp; &#34;b&#34;&gt;|&lt;A &amp; &#34;B&#34;&gt;|x&lt;y|&lt;a &amp; &#34;b&#34;&gt;|dflt|&lt;a &amp; &#34;b&#34;&gt;`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

func TestTemplateErrors(t *testing.T) {
	if _, err := ParseTemplate(TemplateSpec{Content: `{{ .X `}); !errors.Is(err, ErrTemplateSyntax) {
		t.Errorf("syntax: %v", err)
	}
	if _, err := ParseTemplate(TemplateSpec{Content: strings.Repeat("x", MaxTemplateSize+1)}); !errors.Is(err, ErrTemplateSyntax) {
		t.Errorf("size: %v", err)
	}
	for _, c := range []string{`{{.Variables.undeclared}}`, `{{.Identity.Nope}}`} {
		tp := mustTemplate(t, c)
		if _, err := tp.Execute(Data{Variables: map[string]string{}}); !errors.Is(err, ErrTemplateRender) {
			t.Errorf("%s: %v", c, err)
		}
	}
}

func TestStripWindowsPE(t *testing.T) {
	src := "<unattend xmlns=\"urn:schemas-microsoft-com:unattend\">\n  <settings pass=\"windowsPE\">\n    <component name=\"x\"><a>1</a></component>\n  </settings>\n  <settings pass=\"specialize\"/>\n  <settings pass=\"windowsPE\"/>\n</unattend>\n"
	tp := mustTemplate(t, src)
	got, err := tp.Execute(Data{InstallMethod: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	want := "<unattend xmlns=\"urn:schemas-microsoft-com:unattend\">\n  <settings pass=\"specialize\"/>\n</unattend>\n"
	if string(got) != want {
		t.Errorf("agent:\n%s", got)
	}
	if got, _ := tp.Execute(Data{InstallMethod: "smb"}); string(got) != src {
		t.Errorf("smb changed it:\n%s", got)
	}
}

func TestBuiltinChecksClean(t *testing.T) {
	_, fs, err := CheckTemplate(Builtin())
	if err != nil || len(fs) != 0 {
		t.Fatalf("%v %+v", err, fs)
	}
}

func codes(fs []Finding) map[string]int {
	out := map[string]int{}
	for _, f := range fs {
		out[f.Code]++
	}
	return out
}

func TestCheckTemplate(t *testing.T) {
	builtin := Builtin().Content
	for _, c := range []struct {
		name, content string
		modes         []string
		want          []string // finding codes
	}{
		{"no join", strings.Replace(builtin, "{{- with .Identity.Join}}", "{{- with .Identity.Join}}{{end}}{{- with false}}", 1), []string{ModeAD}, []string{"unattend_missing_join"}},
		{"no odj", strings.Replace(builtin, "{{- with .Identity.ODJ}}", "{{- with .Identity.ODJ}}{{end}}{{- with false}}", 1), []string{ModeAD}, []string{"unattend_missing_join"}},
		{"no accounts", strings.Replace(builtin, "{{- if .Identity.Accounts}}", "{{- if false}}", 1), []string{ModeLocal}, []string{"unattend_missing_account", "unattend_missing_account"}},
		{"no autologon", strings.Replace(builtin, "{{- with .Identity.AutoLogon}}", "{{- with false}}", 1), []string{ModeLocal}, []string{"unattend_missing_autologon"}},
		{"name ignored", strings.Replace(builtin, "{{if .ComputerName}}{{.ComputerName}}{{else}}*{{end}}", "*", 1), []string{ModeNone}, []string{"unattend_ignores_computer_name"}},
		{"hard-coded arch", strings.Replace(builtin, `processorArchitecture="{{.Arch}}"`, `processorArchitecture="arm64"`, 1), []string{ModeNone}, []string{"unattend_architecture"}},
		{"reserved", strings.Replace(builtin, "        </LocalAccounts>", "          <LocalAccount><Name>Administrator</Name></LocalAccount>\n        </LocalAccounts>", 1), []string{ModeLocal}, []string{"reserved_account_name"}},
		{"encoding", strings.Replace(builtin, `encoding="utf-8"`, `encoding="utf-16"`, 1), []string{ModeNone}, []string{"unattend_encoding"}},
		{"root", `<unattend><settings pass="specialize"/></unattend>`, []string{ModeNone}, []string{"unattend_root"}},
		{"passes", `<unattend xmlns="urn:schemas-microsoft-com:unattend"><settings pass="specialize"/><settings pass="specialize"/><settings pass="nope"/></unattend>`, []string{ModeNone}, []string{"unattend_pass", "unattend_pass", "unattend_ignores_computer_name"}},
		{"not xml", `<unattend>`, []string{ModeNone}, []string{"unattend_xml"}},
		{"entra", builtin, []string{ModeEntra}, []string{"identity_mode_not_implemented"}},
	} {
		_, fs, err := CheckTemplate(TemplateSpec{Name: c.name, Content: c.content, IdentityModes: c.modes})
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		got := codes(fs)
		want := map[string]int{}
		for _, w := range c.want {
			want[w]++
		}
		if len(got) != len(want) {
			t.Errorf("%s: %+v", c.name, fs)
			continue
		}
		for k, n := range want {
			if got[k] != n {
				t.Errorf("%s: %+v", c.name, fs)
			}
		}
	}
	// A required variable gets a sample value; an undeclared one fails.
	spec := TemplateSpec{Content: `<unattend xmlns="urn:schemas-microsoft-com:unattend"><!-- {{.Variables.site}} --></unattend>`, IdentityModes: []string{ModeNone}}
	if _, _, err := CheckTemplate(spec); !errors.Is(err, ErrTemplateRender) {
		t.Errorf("undeclared variable: %v", err)
	}
	spec.RequiredVariables = []string{"site"}
	if _, fs, err := CheckTemplate(spec); err != nil || codes(fs)["unattend_ignores_computer_name"] != 1 || len(fs) != 1 {
		t.Errorf("required variable: %v %+v", err, fs)
	}
	if m := MissingVariables(spec, map[string]string{"site": " "}); len(m) != 1 {
		t.Errorf("missing: %v", m)
	}
}

func TestPreviewMasksSecrets(t *testing.T) {
	for _, mode := range []string{ModeNone, ModeLocal, ModeAD} {
		for _, c := range sampleCases[mode] {
			p, _ := Parse([]byte(c.profile))
			in := SampleInput(nil, nil)
			in.Secrets = map[string][]byte{"sample": []byte("TOP-SECRET")} // must not be read
			b, err := Render(p, in)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "TOP-SECRET") || strings.Contains(string(b), encodePassword("TOP-SECRET")) {
				t.Errorf("%s: secret in a preview", c.label)
			}
			if mode != ModeNone && !strings.Contains(string(b), Mask) {
				t.Errorf("%s: no mask:\n%s", c.label, b)
			}
		}
	}
}
