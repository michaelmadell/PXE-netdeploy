package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/michaelmadell/pxe-netdeploy/internal/autoinstall"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

func TestAutoinstallTemplatesAPI(t *testing.T) {
	f := newAgentFixture(t)
	body := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return string(b)
	}
	builtin := autoinstall.Builtin().Content
	for want, b := range map[string]map[string]any{
		"not_for_format":          {"name": "a", "format": "autoinstall", "content": builtin, "identityModes": []string{"none"}},
		"identity_modes_required": {"name": "u", "content": "<unattend/>", "identityModes": []string{}},
		"template_invalid":        {"name": "a", "format": "autoinstall", "content": "#cloud-config\nautoinstall:\n  version: 1\n", "identityModes": []string{}},
		"template_render":         {"name": "a", "format": "autoinstall", "content": "{{ .Nope }}", "identityModes": []string{}},
	} {
		if w := f.call("POST", "/unattend-templates", "", body(b)); w.Code != 422 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s", want, w.Code, w.Body)
		}
	}
	w := f.call("POST", "/unattend-templates", "", body(map[string]any{"name": "Mine", "format": "autoinstall",
		"content": strings.Replace(builtin, "version: 1", "version: 1\n  refresh-installer: {update: false}", 1), "identityModes": []string{}}))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	created := decode[struct{ Id, Etag, Format string }](t, w)
	if created.Format != "autoinstall" {
		t.Errorf("format %q", created.Format)
	}
	w = f.call("PUT", "/unattend-templates/"+created.Id, "", body(map[string]any{"name": "Mine", "content": unattend.Builtin().Content, "identityModes": []string{"none"}}), "If-Match", created.Etag)
	if !strings.Contains(w.Body.String(), "format_changed") {
		t.Errorf("format change: %d %s", w.Code, w.Body)
	}
	// The sample preview renders YAML with the password masked.
	w = f.call("POST", "/unattend-templates/builtin-autoinstall/render", "", "{}")
	res := decode[struct {
		Ok      bool
		Content string
	}](t, w)
	if w.Code != 200 || !res.Ok || !strings.Contains(res.Content, `password: "********"`) {
		t.Errorf("render: %d %s", w.Code, w.Body)
	}
	// A Linux profile can't use an unattend template, nor a Windows one the
	// autoinstall template.
	_, imid := f.addLinux("Ubuntu", "")
	if w := f.call("POST", "/profiles", "", `{"kind":"linux","name":"x","installImageId":"`+imid+`","unattendTemplateId":"builtin"}`); !strings.Contains(w.Body.String(), "template_format_mismatch") {
		t.Errorf("linux + unattend: %d %s", w.Code, w.Body)
	}
	if w := f.call("POST", "/profiles", "", `{"kind":"linux","name":"x","installImageId":"`+imid+`","unattendTemplateId":"`+created.Id+`"}`); w.Code != 201 {
		t.Errorf("linux + autoinstall: %d %s", w.Code, w.Body)
	}
}

// linuxAuto makes the fixture's Ubuntu profile automatic, with a password
// and an SSH key; it returns the profile id and the password.
func (f *agentFixture) linuxAuto(t *testing.T, imid string, extra string) string {
	t.Helper()
	vault := secrets.NewVault(f.st, secrets.NewKeys(t.TempDir()))
	f.srv.vault = vault
	pw, err := vault.Create(f.ctx, "linux admin", "", "password", []byte("S3cret pass"), nil)
	if err != nil {
		t.Fatal(err)
	}
	w := f.call("POST", "/profiles", "", `{"kind":"linux","name":"Ubuntu auto","installImageId":"`+imid+`","linux":{"account":{"username":"admin",`+
		`"passwordSecretId":"`+pw.ID+`","sshAuthorizedKeys":["ssh-ed25519 AAAAC3Nza admin@lab"]}`+extra+`}}`)
	if w.Code != 201 {
		t.Fatalf("profile: %d %s", w.Code, w.Body)
	}
	return decode[struct{ Id string }](t, w).Id
}

func TestLinuxAutomaticProfile(t *testing.T) {
	f := newAgentFixture(t)
	_, imid := f.addLinux("Ubuntu", "")
	for want, linux := range map[string]string{
		"linux_account_required":   `{}`,
		"reserved_account_name":    `{"account":{"username":"root","sshAuthorizedKeys":["ssh-ed25519 AAAA x"]}}`,
		"invalid_ssh_key":          `{"account":{"username":"admin","sshAuthorizedKeys":["nonsense"]}}`,
		"invalid_hostname_pattern": `{"install":"interactive","hostnamePattern":"{{ .Nope"}`,
	} {
		w := f.call("POST", "/profiles", "", `{"kind":"linux","name":"x","installImageId":"`+imid+`","linux":`+linux+`}`)
		if w.Code != 422 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s", want, w.Code, w.Body)
		}
	}
	if w := f.call("POST", "/profiles", "", `{"kind":"linux","name":"x","installImageId":"`+imid+`","linux":{"install":"interactive"}}`); w.Code != 201 {
		t.Errorf("interactive: %d %s", w.Code, w.Body)
	}

	pid := f.linuxAuto(t, imid, `,"hostnamePattern":"LAB-{{ .Client.Serial | last 4 }}"`)
	w := f.call("POST", "/profiles/"+pid+"/validate", "", "")
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("validate: %d %s", w.Code, w.Body)
	}

	// From the menu: the boot commands name the seed.
	w = f.ipxe("unlock", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "password": {"correct horse"}})
	tok := regexp.MustCompile(`param unlockToken (\S+)`).FindStringSubmatch(w.Body.String())[1]
	w = f.ipxe("start", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "unlockToken": {tok}, "profileId": {pid}})
	m := regexp.MustCompile(`cloud-config-url=/dev/null autoinstall ds=nocloud;s=http://10\.77\.0\.1:8080/content/seed/([A-Za-z0-9_-]+)/`).FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("boot script:\n%s", w.Body)
	}
	seed := func(file string) (string, int) {
		body, status, _ := f.srv.LinuxSeed(httptest.NewRequest("GET", "/content/seed/x/"+file, nil), m[1], file)
		return string(body), status
	}

	ud, status := seed("user-data")
	var doc struct {
		Autoinstall struct {
			Identity map[string]string
			SSH      struct {
				AuthorizedKeys []string `yaml:"authorized-keys"`
			} `yaml:"ssh"`
		}
	}
	if err := yaml.Unmarshal([]byte(ud), &doc); err != nil || status != 200 {
		t.Fatalf("user-data %d %v\n%s", status, err, ud)
	}
	id := doc.Autoinstall.Identity
	hash := id["password"]
	salt := strings.Split(hash, "$")
	if len(salt) != 4 || autoinstall.Crypt("S3cret pass", salt[2]) != hash || id["hostname"] != "lab-1122" || id["username"] != "admin" || len(doc.Autoinstall.SSH.AuthorizedKeys) != 1 {
		t.Errorf("user-data:\n%s", ud)
	}
	if md, status := seed("meta-data"); status != 200 || !strings.Contains(md, "instance-id:") {
		t.Errorf("meta-data %d %s", status, md)
	}
	if _, status := seed("network-config"); status != http.StatusNotFound {
		t.Errorf("network-config %d", status)
	}
	if !strings.Contains(ud, "/content/report/"+m[1]+"/events") || !strings.Contains(ud, "/content/report/"+m[1]+"/finished") {
		t.Errorf("user-data lacks the report URLs:\n%s", ud)
	}

	// Reports move the deployment on (ADR 0036).
	c, _ := f.st.ClientByMAC(f.ctx, nucMAC)
	d, _ := f.st.ActiveDeployment(f.ctx, c.ID)
	if d.Stage == nil || *d.Stage != "checked-in" || d.Percent != 5 {
		t.Errorf("after user-data: %v %d", d.Stage, d.Percent)
	}
	report := func(kind, body string) int {
		status, _, _ := f.srv.LinuxReport(httptest.NewRequest("POST", "/content/report/x/"+kind, strings.NewReader(body)), m[1], kind)
		return status
	}
	ev := func(typ, name, level, result string) string {
		b, _ := json.Marshal(map[string]any{"name": name, "description": "d", "event_type": typ, "level": level, "result": result, "origin": "curtin"})
		return string(b)
	}
	report("started", "")
	report("events", ev("start", "subiquity/Install/install/curtin_install/run_curtin_step/cmd-install/stage-extract", "DEBUG", ""))
	report("events", ev("start", "subiquity/Install/install/curtin_install/run_curtin_step/cmd-install/stage-partitioning/builtin", "DEBUG", ""))
	report("events", ev("start", "subiquity/Install/install/postinstall", "INFO", ""))
	d, _ = f.st.Deployment(f.ctx, d.ID)
	if d.Stage == nil || *d.Stage != "finalizing" || d.Percent != 90 || d.Message != "final system configuration" {
		t.Errorf("after events: %v %d %q", d.Stage, d.Percent, d.Message)
	}
	logs, _, _ := f.st.DeploymentLogs(f.ctx, d.ID, 0, 100)
	if len(logs) != 2 || !strings.Contains(logs[0].Message, "started") || !strings.Contains(logs[1].Message, "postinstall") {
		t.Errorf("logs: %+v", logs)
	}
	if report("events", "not json") != http.StatusBadRequest || report("nope", "") != http.StatusNotFound {
		t.Error("bad reports accepted")
	}
	if report("finished", "") != http.StatusNoContent {
		t.Error("finished refused")
	}
	if d, _ = f.st.Deployment(f.ctx, d.ID); d.Status != store.StatusSucceeded {
		t.Errorf("status %s", d.Status)
	}
	// Once the deployment ends, the seed and the reports are gone.
	if _, status := seed("user-data"); status != http.StatusNotFound {
		t.Errorf("seed after the end: %d", status)
	}
	if report("finished", "") != http.StatusNotFound {
		t.Error("report after the end")
	}
}

// startAuto starts a profile from the iPXE menu and returns the
// deployment and its seed token.
func (f *agentFixture) startAuto(t *testing.T, pid string) (store.Deployment, string) {
	t.Helper()
	w := f.ipxe("unlock", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "password": {"correct horse"}})
	tok := regexp.MustCompile(`param unlockToken (\S+)`).FindStringSubmatch(w.Body.String())[1]
	w = f.ipxe("start", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "unlockToken": {tok}, "profileId": {pid}})
	m := regexp.MustCompile(`/content/seed/([A-Za-z0-9_-]+)/`).FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("boot script:\n%s", w.Body)
	}
	c, _ := f.st.ClientByMAC(f.ctx, nucMAC)
	d, err := f.st.ActiveDeployment(f.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d, m[1]
}

func TestInstallerFailure(t *testing.T) {
	for name, c := range map[string]struct{ kind, body string }{
		"webhook":        {"events", `{"name":"subiquity/Install/install/curtin_install","event_type":"finish","result":"FAIL","level":"DEBUG","description":"installing system"}`},
		"error-commands": {"failed", "boom"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAgentFixture(t)
			_, imid := f.addLinux("Ubuntu", "")
			d, tok := f.startAuto(t, f.linuxAuto(t, imid, ""))
			// A failed step that isn't the install doesn't fail it.
			f.srv.LinuxReport(httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"subiquity/Drivers/_list_drivers","event_type":"finish","result":"FAIL","level":"DEBUG"}`)), tok, "events")
			if d2, _ := f.st.Deployment(f.ctx, d.ID); d2.Status != store.StatusRunning {
				t.Fatalf("failed by a side step: %s", d2.Status)
			}
			f.srv.LinuxReport(httptest.NewRequest("POST", "/x", strings.NewReader(c.body)), tok, c.kind)
			if d2, _ := f.st.Deployment(f.ctx, d.ID); d2.Status != store.StatusFailed {
				t.Errorf("status %s %q", d2.Status, d2.Message)
			}
		})
	}
}
