package server

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

func unattendPassword(p string) string {
	u := utf16.Encode([]rune(p + "Password"))
	b := make([]byte, 0, 2*len(u))
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestScheduleCancelAndTimeouts(t *testing.T) {
	f := newAgentFixture(t)
	vault := secrets.NewVault(f.st, secrets.NewKeys(t.TempDir()))
	f.srv.vault = vault
	pw, err := vault.Create(f.ctx, "admin password", "", "password", []byte("S3cret&"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := f.st.Profile(f.ctx, f.profile)
	in := p.ProfileInput
	in.Identity = json.RawMessage(`{"mode":"local","accounts":[{"name":"admin","group":"Administrators","passwordSecretId":"` + pw.ID + `"}]}`)
	in.OSSettings = json.RawMessage(`{"computerNamePattern":"LAB-{{ .Client.Serial | last 4 }}"}`)
	if _, err := f.st.ReplaceProfile(f.ctx, p.ID, in, p.Version); err != nil {
		t.Fatal(err)
	}
	serial := "SN-0042"
	c, err := f.st.CreateClient(f.ctx, store.ClientInput{MAC: "00:15:5d:00:85:09", Serial: &serial})
	if err != nil {
		t.Fatal(err)
	}

	// No profile on the client and none named.
	if w := f.call("POST", "/deployments", "", `{"clientId":"`+c.ID+`"}`); w.Code != 422 || !strings.Contains(w.Body.String(), "no_profile") {
		t.Fatalf("no profile: %d %s", w.Code, w.Body)
	}
	if v := decode[api.ValidationResult](t, f.call("POST", "/profiles/"+f.profile+"/validate", "", "")); !v.Ok {
		t.Fatalf("validate: %+v", v)
	}

	arm := func() api.Deployment {
		t.Helper()
		w := f.call("POST", "/deployments", "", `{"clientId":"`+c.ID+`","profileId":"`+f.profile+`"}`)
		if w.Code != 201 {
			t.Fatalf("arm: %d %s", w.Code, w.Body)
		}
		return decode[api.Deployment](t, w)
	}
	d := arm()
	if d.Status != "pending" || d.ComputerName == nil || *d.ComputerName != "LAB-0042" {
		t.Fatalf("armed: %+v", d)
	}
	if w := f.call("POST", "/deployments", "", `{"clientId":"`+c.ID+`","profileId":"`+f.profile+`"}`); w.Code != 409 {
		t.Errorf("second arm: %d", w.Code)
	}

	// Rotating the secret afterwards doesn't change the deployment.
	x, _ := f.st.Secret(f.ctx, pw.ID)
	if _, err := vault.Replace(f.ctx, pw.ID, []byte("rotated"), nil, x.Version); err != nil {
		t.Fatal(err)
	}
	ci := decode[api.AgentCheckInResult](t, f.call("POST", "/agent/check-in", "", identity))
	if ci.State != "armed" {
		t.Fatalf("check-in: %+v", ci)
	}
	tok := ci.Armed.Token
	plan := decode[api.AgentDeploymentPlan](t, f.call("GET", "/agent/deployment", tok, ""))
	if plan.UnattendUrl == nil || *plan.UnattendUrl != "/api/v1/agent/unattend" || plan.IdentityFiles != nil {
		t.Errorf("plan: %+v", plan)
	}
	w := f.call("GET", "/agent/unattend", tok, "")
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "<ComputerName>LAB-0042</ComputerName>") || !strings.Contains(body, unattendPassword("S3cret&")) {
		t.Fatalf("unattend: %d\n%s", w.Code, body)
	}

	// Cancel while running: progress says so, logs and the acknowledgement
	// are still accepted, then the token is done; the secret copies are gone.
	if w := f.call("POST", "/agent/progress", tok, `{"stage":"partitioning","percent":10}`); w.Code != 204 {
		t.Fatalf("progress: %d", w.Code)
	}
	w = f.call("POST", "/deployments/"+*d.Id+"/cancel", "", "")
	if got := decode[api.Deployment](t, w); w.Code != 200 || got.Status != "cancelled" {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if w := f.call("POST", "/agent/progress", tok, `{"stage":"partitioning","percent":50}`); w.Code != 409 || !strings.Contains(w.Body.String(), "deployment_cancelled") {
		t.Errorf("progress after cancel: %d %s", w.Code, w.Body)
	}
	if w := f.call("GET", "/agent/unattend", tok, ""); w.Code != 401 {
		t.Errorf("unattend after cancel: %d", w.Code)
	}
	if w := f.call("POST", "/agent/logs", tok, `{"entries":[{"at":"2026-11-12T12:00:00Z","level":"info","message":"stopping"}]}`); w.Code != 204 {
		t.Errorf("logs after cancel: %d", w.Code)
	}
	if w := f.call("POST", "/agent/complete", tok, `{"result":"failed","message":"cancelled"}`); w.Code != 204 {
		t.Errorf("acknowledge: %d %s", w.Code, w.Body)
	}
	if w := f.call("POST", "/agent/logs", tok, `{"entries":[]}`); w.Code != 401 {
		t.Errorf("logs after acknowledgement: %d", w.Code)
	}
	if got, _ := f.st.Deployment(f.ctx, *d.Id); got.Status != "cancelled" {
		t.Errorf("status after acknowledgement: %s", got.Status)
	}
	if copies, _ := f.st.DeploymentSecrets(f.ctx, *d.Id); len(copies) != 0 {
		t.Errorf("secret copies kept: %d", len(copies))
	}
	if w := f.call("POST", "/deployments/"+*d.Id+"/cancel", "", ""); w.Code != 200 {
		t.Errorf("cancel again: %d", w.Code)
	}

	// Timeouts: an armed deployment expires, a quiet running one times out.
	d = arm()
	f.now = f.now.Add(73 * time.Hour)
	if err := f.srv.SweepDeployments(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.Deployment(f.ctx, *d.Id); got.Status != "failed" || !strings.HasPrefix(got.Message, "expired") {
		t.Errorf("expired: %s %q", got.Status, got.Message)
	}
	f.now = time.Now()
	d = arm()
	f.call("POST", "/agent/check-in", "", identity)
	f.srv.SweepDeployments(f.ctx)
	if got, _ := f.st.Deployment(f.ctx, *d.Id); got.Status != "running" {
		t.Fatalf("running swept too early: %s %q", got.Status, got.Message)
	}
	f.now = f.now.Add(31 * time.Minute)
	f.srv.SweepDeployments(f.ctx)
	if got, _ := f.st.Deployment(f.ctx, *d.Id); got.Status != "failed" || !strings.HasPrefix(got.Message, "timeout") {
		t.Errorf("timeout: %s %q", got.Status, got.Message)
	}

	// A name that breaks the rules refuses arming.
	bad := "AB_CD"
	c2, _ := f.st.CreateClient(f.ctx, store.ClientInput{MAC: "00:15:5d:00:85:0a", Serial: &bad})
	if w := f.call("POST", "/deployments", "", `{"clientId":"`+c2.ID+`","profileId":"`+f.profile+`"}`); w.Code != 422 ||
		!strings.Contains(w.Body.String(), "invalid_computer_name") {
		t.Errorf("bad name: %d %s", w.Code, w.Body)
	}
	// A missing secret is a validation error and refuses arming.
	in.Identity = json.RawMessage(`{"mode":"local","accounts":[{"name":"admin","group":"Administrators","passwordSecretId":"nope"}]}`)
	p, _ = f.st.Profile(f.ctx, f.profile)
	f.st.ReplaceProfile(f.ctx, p.ID, in, p.Version)
	if v := decode[api.ValidationResult](t, f.call("POST", "/profiles/"+f.profile+"/validate", "", "")); v.Ok {
		t.Errorf("validate with a missing secret: %+v", v)
	}
	if w := f.call("POST", "/deployments", "", `{"clientId":"`+c.ID+`","profileId":"`+f.profile+`"}`); w.Code != 422 ||
		!strings.Contains(w.Body.String(), "profile_invalid") {
		t.Errorf("arm with a missing secret: %d %s", w.Code, w.Body)
	}
}
