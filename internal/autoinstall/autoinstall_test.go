package autoinstall

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

func codes(fs []unattend.Finding) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		out[f.Code] = true
	}
	return out
}

func render(t *testing.T, content string, d Data) string {
	t.Helper()
	tpl, err := ParseTemplate(unattend.TemplateSpec{Content: content, Format: Format})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tpl.Execute(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestYAMLValues(t *testing.T) {
	d := Data{Hostname: `a"b: #c`, Packages: []string{"htop", "x y"}, Account: AccountData{SSHPasswordAuth: true, Username: "o'neil"}}
	got := render(t, `h: {{ .Hostname }}
p: {{ .Packages }}
b: {{ .Account.SSHPasswordAuth }}
r: {{ raw "[1, 2]" }}
s: {{ shquote .Account.Username }}
l: {{ upper (last 2 .Hostname) }}`, d)
	want := `h: "a\"b: #c"
p: ["htop","x y"]
b: true
r: [1, 2]
s: "'o'\\''neil'"
l: "#C"`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(got), &m); err != nil || m["h"] != `a"b: #c` || m["s"] != `'o'\''neil'` {
		t.Errorf("YAML %v %v", m, err)
	}
}

func TestBuiltinChecksClean(t *testing.T) {
	_, fs, err := CheckTemplate(Builtin())
	if err != nil || len(fs) != 0 {
		t.Fatalf("%v %+v", err, fs)
	}
}

// userData renders the built-in template.
func userData(t *testing.T, p Profile, in Input) (string, map[string]any) {
	t.Helper()
	tpl, err := ParseTemplate(Builtin())
	if err != nil {
		t.Fatal(err)
	}
	out, err := tpl.Execute(Prepare(p, in))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out), doc["autoinstall"].(map[string]any)
}

func TestBuiltin(t *testing.T) {
	p := SampleProfile()
	hash := HashPassword("pw")
	in := SampleInput(nil)
	in.PasswordHash = hash
	out, ai := userData(t, p, in)
	id := ai["identity"].(map[string]any)
	ssh := ai["ssh"].(map[string]any)
	if id["hostname"] != SampleHostname || id["username"] != "sample" || id["password"] != hash || ssh["install-server"] != true ||
		len(ssh["authorized-keys"].([]any)) != 1 || ssh["allow-pw"] != false || ai["locale"] != "en_GB.UTF-8" ||
		ai["storage"].(map[string]any)["layout"].(map[string]any)["name"] != "lvm" || ai["packages"].([]any)[0] != "htop" {
		t.Errorf("with a password:\n%s", out)
	}
	late := ai["late-commands"].([]any)
	if len(late) != 2 || !strings.Contains(late[0].(string), "efibootmgr -o") || strings.Contains(out, "sudoers") {
		t.Errorf("late-commands: %q", late)
	}

	// SSH keys only, network first: locked password, passwordless sudo, no
	// boot order change.
	p.BootOrderAfterDeploy = "network-first"
	p.Linux.Account.PasswordSecretID = nil
	in.PasswordHash = ""
	out, ai = userData(t, p, in)
	late = ai["late-commands"].([]any)
	if ai["identity"].(map[string]any)["password"] != "!" || len(late) != 2 || !strings.Contains(late[0].(string), "'sample ALL=(ALL) NOPASSWD:ALL'") ||
		strings.Contains(out, "efibootmgr") {
		t.Errorf("SSH only:\n%s", out)
	}

	// No keys: no SSH server.
	p.Linux.Account.SSHAuthorizedKeys = nil
	_, ai = userData(t, p, in)
	if ssh := ai["ssh"].(map[string]any); ssh["install-server"] != false || len(ssh["authorized-keys"].([]any)) != 0 {
		t.Errorf("no keys: %v", ssh)
	}
}

func TestCheck(t *testing.T) {
	d := Prepare(SampleProfile(), SampleInput(nil))
	for name, c := range map[string]struct {
		doc  string
		want string
	}{
		"header":   {"autoinstall:\n  version: 1\n  identity: {hostname: nd-sample-01, username: sample}\n", "autoinstall_header"},
		"yaml":     {"#cloud-config\nautoinstall: [\n", "autoinstall_yaml"},
		"missing":  {"#cloud-config\nusers: []\n", "autoinstall_missing"},
		"version":  {"#cloud-config\nautoinstall:\n  version: 2\n  interactive-sections: [identity]\n", "autoinstall_version"},
		"identity": {"#cloud-config\nautoinstall:\n  version: 1\n", "autoinstall_identity"},
		"hostname": {"#cloud-config\nautoinstall:\n  version: 1\n  identity: {hostname: other, username: sample}\n", "hostname_dropped"},
	} {
		if fs := codes(Check([]byte(c.doc), d)); !fs[c.want] {
			t.Errorf("%s: %v", name, fs)
		}
	}
	if fs := Check([]byte("#cloud-config\nautoinstall:\n  version: 1\n  interactive-sections: ['*']\n"), d); len(fs) != 0 {
		t.Errorf("interactive: %+v", fs)
	}
}

func TestRenderHostname(t *testing.T) {
	nd := unattend.NameData{Client: unattend.NameClient{MAC: "88:ae:dd:0a:11:22", Serial: "Undefined"}, Variables: map[string]string{"site": "LAB"}}
	pat := func(s string) *string { return &s }
	for _, c := range []struct {
		pattern *string
		want    string
		bad     bool
	}{
		{nil, "linux-0a1122", false},
		{pat("{{ .Variables.site }}-{{ .Client.Serial | last 4 }}"), "lab-1122", false},
		{pat("bad_name"), "", true},
		{pat("-x"), "", true},
		{pat(strings.Repeat("a", 64)), "", true},
	} {
		got, err := RenderHostname(c.pattern, nd)
		if (err != nil) != c.bad || (!c.bad && got != c.want) {
			t.Errorf("%v: %q %v", c.pattern, got, err)
		}
	}
}
