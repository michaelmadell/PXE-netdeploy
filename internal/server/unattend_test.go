package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

func templateBody(t *testing.T, name, content string, modes []string, required ...string) string {
	t.Helper()
	if required == nil {
		required = []string{}
	}
	b, err := json.Marshal(map[string]any{"name": name, "content": content, "identityModes": modes, "requiredVariables": required})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUnattendTemplates(t *testing.T) {
	f := newAgentFixture(t)
	builtin := unattend.Builtin().Content

	// The built-in templates: listed first, read-only.
	page := decode[api.UnattendTemplatePage](t, f.call("GET", "/unattend-templates", "", ""))
	if len(page.Items) != 2 || *page.Items[0].Id != "builtin" || !*page.Items[0].BuiltIn || page.Items[0].Content != builtin ||
		*page.Items[1].Id != "builtin-autoinstall" || *page.Items[1].Format != "autoinstall" {
		t.Fatalf("list: %+v", page)
	}
	if w := f.call("PUT", "/unattend-templates/builtin", "", templateBody(t, "x", builtin, []string{"none"}), "If-Match", `"1"`); w.Code != 409 || !strings.Contains(w.Body.String(), "built_in") {
		t.Errorf("put builtin: %d %s", w.Code, w.Body)
	}
	if w := f.call("DELETE", "/unattend-templates/builtin", "", ""); w.Code != 409 {
		t.Errorf("delete builtin: %d %s", w.Code, w.Body)
	}

	// Refused on save.
	noJoin := strings.Replace(builtin, "{{- with .Identity.Join}}", "{{- with .Identity.Join}}{{end}}{{- with false}}", 1)
	for code, body := range map[string]string{
		"template_syntax":  templateBody(t, "bad", "{{ .Arch ", []string{"none"}),
		"template_render":  templateBody(t, "bad", "<unattend>{{ .Variables.site }}</unattend>", []string{"none"}),
		"template_invalid": templateBody(t, "bad", noJoin, []string{"ad"}),
	} {
		if w := f.call("POST", "/unattend-templates", "", body); w.Code != 422 || !strings.Contains(w.Body.String(), code) {
			t.Errorf("%s: %d %s", code, w.Code, w.Body)
		}
	}
	// A draft that save would refuse previews with its findings.
	draft, _ := json.Marshal(map[string]any{"identityMode": "ad", "template": map[string]any{"name": "d", "content": noJoin, "identityModes": []string{"ad"}}})
	res := decode[api.UnattendRenderResult](t, f.call("POST", "/unattend-templates/render", "", string(draft)))
	if res.Ok || !hasFinding(res.Findings, "unattend_missing_join") {
		t.Errorf("draft: %+v", res)
	}

	// A custom template: a windowsPE pass (removed for the agent) and a
	// required variable.
	v1 := strings.Replace(builtin, "  <settings pass=\"specialize\">",
		"  <settings pass=\"windowsPE\"><component name=\"Microsoft-Windows-Setup\" processorArchitecture=\"{{.Arch}}\" publicKeyToken=\"31bf3856ad364e35\" language=\"neutral\" versionScope=\"nonSxS\"><UserData><AcceptEula>true</AcceptEula></UserData></component></settings>\n"+
			"  <!-- site {{.Variables.site}} v1 -->\n  <settings pass=\"specialize\">", 1)
	w := f.call("POST", "/unattend-templates", "", templateBody(t, "Lab", v1, []string{"none", "local"}, "site"))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	tp := decode[api.UnattendTemplate](t, w)
	res = decode[api.UnattendRenderResult](t, f.call("POST", "/unattend-templates/"+*tp.Id+"/render", "", `{"identityMode":"local"}`))
	if !res.Ok || !strings.Contains(res.Content, "site sample-site v1") || strings.Contains(res.Content, "windowsPE") ||
		!strings.Contains(res.Content, "<Value>"+unattend.Mask+"</Value>") {
		t.Errorf("render: %+v", res)
	}

	// Profiles: the template must render the profile's mode.
	p, _ := f.st.Profile(f.ctx, f.profile)
	pin := p.ProfileInput
	pin.UnattendTemplateID = tp.Id
	pin.Identity = json.RawMessage(`{"mode":"ad","joinMethod":"odj"}`)
	if _, err := f.st.ReplaceProfile(f.ctx, p.ID, pin, p.Version); err == nil {
		t.Error("ad profile with a none/local template: no error")
	}
	pin.Identity = json.RawMessage(`{"mode":"none"}`)
	if p, err := f.st.ReplaceProfile(f.ctx, p.ID, pin, p.Version); err != nil || p.UnattendTemplateID == nil {
		t.Fatalf("profile: %v", err)
	}
	if w := f.call("DELETE", "/unattend-templates/"+*tp.Id, "", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "template_in_use") {
		t.Errorf("delete in use: %d %s", w.Code, w.Body)
	}
	if w := f.call("PUT", "/unattend-templates/"+*tp.Id, "", templateBody(t, "Lab", v1, []string{"local"}, "site"), "If-Match", *tp.Etag); w.Code != 409 {
		t.Errorf("drop the profile's mode: %d %s", w.Code, w.Body)
	}

	// Arming needs the variable; the client gives it.
	c, err := f.st.CreateClient(f.ctx, store.ClientInput{MAC: "00:15:5d:00:85:09", ProfileID: &f.profile})
	if err != nil {
		t.Fatal(err)
	}
	arm := `{"clientId":"` + c.ID + `","profileId":"` + f.profile + `"}`
	if w := f.call("POST", "/deployments", "", arm); w.Code != 422 || !strings.Contains(w.Body.String(), "missing_variable") {
		t.Fatalf("arm without variable: %d %s", w.Code, w.Body)
	}
	vr := decode[api.ValidationResult](t, f.call("POST", "/profiles/"+f.profile+"/validate", "", ""))
	if !hasFinding(vr.Findings, "missing_variable") {
		t.Errorf("validate: %+v", vr)
	}
	c.Variables = map[string]string{"site": "ldn"}
	if _, err := f.st.ReplaceClient(f.ctx, c.ID, c.ClientInput, c.Version); err != nil {
		t.Fatal(err)
	}
	if vr := decode[api.ValidationResult](t, f.call("POST", "/profiles/"+f.profile+"/validate", "", "")); hasFinding(vr.Findings, "missing_variable") {
		t.Errorf("validate with the variable: %+v", vr)
	}
	res = decode[api.UnattendRenderResult](t, f.call("POST", "/unattend-templates/"+*tp.Id+"/render", "", `{"profileId":"`+f.profile+`","clientId":"`+c.ID+`"}`))
	if !res.Ok || !strings.Contains(res.Content, "site ldn v1") {
		t.Errorf("render for the client: %+v", res)
	}
	w = f.call("POST", "/deployments", "", arm)
	if w.Code != 201 {
		t.Fatalf("arm: %d %s", w.Code, w.Body)
	}
	d := decode[api.Deployment](t, w)
	if d.UnattendTemplate == nil || d.UnattendTemplate.Id != *tp.Id || d.UnattendTemplate.BuiltIn || d.Variables == nil || (*d.Variables)["site"] != "ldn" {
		t.Errorf("deployment: %+v %+v", d.UnattendTemplate, d.Variables)
	}

	// Editing the template doesn't change the armed deployment.
	v2 := strings.Replace(v1, " v1 -->", " v2 -->", 1)
	if w := f.call("PUT", "/unattend-templates/"+*tp.Id, "", templateBody(t, "Lab", v2, []string{"none", "local"}, "site"), "If-Match", *tp.Etag); w.Code != 200 {
		t.Fatalf("replace: %d %s", w.Code, w.Body)
	}
	ci := decode[api.AgentCheckInResult](t, f.call("POST", "/agent/check-in", "", identity))
	w = f.call("GET", "/agent/unattend", ci.Armed.Token, "")
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "site ldn v1") || strings.Contains(body, "windowsPE") {
		t.Errorf("agent unattend: %d\n%s", w.Code, body)
	}
}

func hasFinding(fs []api.ValidationFinding, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}
