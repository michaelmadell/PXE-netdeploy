package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agent/wire"
	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

type agentFixture struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	srv     *Server
	h       http.Handler
	profile string
	wim     string // install.wim sha
	now     time.Time
}

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	content, err := cas.Open(ctx, cas.Options{Root: filepath.Join(dir, "content"), Store: st, Reserve: func() int64 { return 0 }, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	f := &agentFixture{t: t, ctx: ctx, st: st, now: time.Now()}

	upload := func(sha string) string {
		name := "x"
		u, _ := st.CreateUpload(ctx, store.NewUpload{Length: 10, Filename: &name, HashState: []byte("h"), ExpiresAt: time.Now().Add(time.Hour)})
		st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: 10, HashState: []byte("h"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)})
		return u.ID
	}
	// An image with install.wim.
	im, _, _ := st.CreateImageImport(ctx, store.NewImageImport{Name: "Win11", UploadID: upload(strings.Repeat("a", 64))}, "image.import")
	stg, _ := content.NewStaging("img")
	f.wim, _, _ = stg.Add("sources/install.wim", strings.NewReader("WIM DATA 0123456789"))
	if _, err := stg.Commit(ctx, store.TreeImage, func(ctx context.Context, tree string) error {
		return st.CompleteImageImport(ctx, im.ID, store.ImportResult{Kind: "windows", TreeID: tree,
			Details: json.RawMessage(`{"kind":"windows","architecture":"x64","editions":[{"index":6,"name":"Pro"}]}`)})
	}); err != nil {
		t.Fatal(err)
	}
	// A boot image built with the 2023 boot manager.
	bi, _ := st.CreateBootImage(ctx, store.BootImageInput{Name: "pe", SourceImageID: im.ID, SourceIndex: 1, Architecture: "x64",
		ServerURLs: []string{"http://x"}, Builder: "auto", Bootmgr: "auto", TFTPBlockSize: 16384, TFTPWindowSize: 16})
	tree, _ := st.InsertTree(ctx, store.NewTree{Kind: store.TreeBootImage, Entries: []store.NewTreeEntry{{Path: "bootmgfw.efi", Fold: "bootmgfw.efi", SHA256: strings.Repeat("c", 64), Size: 1}}})
	st.AdoptBootImageBuild(ctx, bi.ID, store.BootBuild{TreeID: tree, InputHash: strings.Repeat("e", 64), Inputs: json.RawMessage(`{}`),
		BuiltWith: "wimlib", SelectedBootManager: json.RawMessage(`{"signer":"windows-uefi-ca-2023"}`)})
	// A driver package in a group, for the OS.
	pkg, _, _ := st.CreateDriverImport(ctx, "nic", upload(strings.Repeat("d", 64)), "driverPackage.import")
	dstg, _ := content.NewStaging("drv")
	dstg.Add("PRO1000/Winx64/e1r.inf", strings.NewReader("[Version]"))
	dstg.Add("PRO1000/Winx64/e1r.sys", strings.NewReader("sys"))
	dstg.Add("readme.txt", strings.NewReader("other"))
	if _, err := dstg.Commit(ctx, store.TreeDriverPackage, func(ctx context.Context, tree string) error {
		return st.CompleteDriverImport(ctx, pkg.ID, tree, nil, []store.NewDriverINF{{Path: "PRO1000/Winx64/e1r.inf", Class: "Net",
			Role: "network", Architectures: []string{"x64"}, TargetOSVersions: []string{}, Signature: "whql", Enabled: true}})
	}); err != nil {
		t.Fatal(err)
	}
	g, _ := st.CreateDriverGroup(ctx, store.DriverGroupInput{Name: "nic", PackageIDs: []string{pkg.ID}})
	p, err := st.CreateProfile(ctx, store.ProfileInput{Name: "Win11 Pro", BootImageID: bi.ID, InstallImageID: im.ID, EditionIndex: 6,
		InstallMethod: "agent", Identity: json.RawMessage(`{"mode":"none"}`), BootOrderAfterDeploy: "network-first",
		OSDriverGroupIDs: []string{g.ID}})
	if err != nil {
		t.Fatal(err)
	}
	f.profile = p.ID

	hash, _ := settings.HashPassword("correct horse")
	f.srv = New("test", Deps{Store: st, Events: events.New(), Content: content})
	f.srv.agent.bootPrompt = func() settings.BootPrompt {
		return settings.BootPrompt{PasswordHash: hash, RequirePassword: true, TimeoutSeconds: 30, ArmedCountdownSeconds: 10, DefaultProfileID: p.ID}
	}
	f.srv.agent.now = func() time.Time { return f.now }
	spec, _ := api.GetSwagger()
	validate, _ := RequestValidator(spec)
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(f.srv, nil, api.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	validated := validate(mux)
	f.h = Recover(WithRequestInfo(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/agent/files/") {
			f.srv.AgentFiles(w, r)
			return
		}
		validated.ServeHTTP(w, r)
	})))
	return f
}

// call sends a request from 10.77.0.100 and returns the response.
func (f *agentFixture) call(method, path, token, body string, hdr ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.RemoteAddr = "10.77.0.100:50000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	return w
}

// strict decodes into the agent's own wire type, refusing unknown fields:
// the agent's hand-written types must match what the server sends.
func strict[T any](t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	dec.DisallowUnknownFields()
	var v T
	if err := dec.Decode(&v); err != nil {
		t.Errorf("agent wire type %T: %v\n%s", v, err, w.Body)
	}
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return v
}

const identity = `{"macs":["00-15-5D-00-85-09"],"serial":"S1","agentVersion":"test"}`

func TestAgentBootPromptDeployment(t *testing.T) {
	f := newAgentFixture(t)

	// Check-in: locked, and the client exists.
	w := f.call("POST", "/agent/check-in", "", identity)
	ci := decode[api.AgentCheckInResult](t, w)
	strict[wire.CheckInResult](t, w)
	if w.Code != 200 || ci.State != "locked" || ci.Prompt == nil || !ci.Prompt.PasswordConfigured || ci.Prompt.TimeoutSeconds != 30 {
		t.Fatalf("check-in: %d %s", w.Code, w.Body)
	}

	// Unlock: wrong password, then rate-limited, then right after the wait.
	unlock := func(pw string) *httptest.ResponseRecorder {
		return f.call("POST", "/agent/unlock", "", `{"clientId":"`+ci.ClientId+`","password":"`+pw+`"}`)
	}
	if w := f.call("POST", "/agent/unlock", "", `{"clientId":"nope","password":"x"}`); w.Code != 404 {
		t.Errorf("unknown client: %d", w.Code)
	}
	for i := 0; i < freeFailures; i++ {
		if w := unlock("wrong"); w.Code != 401 {
			t.Fatalf("wrong password %d: %d %s", i, w.Code, w.Body)
		}
	}
	unlock("wrong") // the 6th failure starts the back-off
	if w := unlock("correct horse"); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d %v", w.Code, w.Header())
	}
	f.now = f.now.Add(time.Minute)
	w = unlock("correct horse")
	ul := decode[api.AgentUnlockResult](t, w)
	strict[wire.UnlockResult](t, w)
	if w.Code != 200 || len(ul.Profiles) != 1 || ul.DefaultProfileId == nil || *ul.DefaultProfileId != f.profile {
		t.Fatalf("unlock: %d %s", w.Code, w.Body)
	}

	// Start: the session token works once.
	w = f.call("POST", "/agent/deployments", ul.SessionToken, `{"profileId":"`+f.profile+`"}`)
	tok := decode[api.AgentToken](t, w)
	strict[wire.Token](t, w)
	if w.Code != 201 || tok.Token == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if w := f.call("POST", "/agent/deployments", ul.SessionToken, `{"profileId":"`+f.profile+`"}`); w.Code != 401 {
		t.Errorf("session reused: %d", w.Code)
	}

	// The plan.
	w = f.call("GET", "/agent/deployment", tok.Token, "")
	plan := decode[api.AgentDeploymentPlan](t, w)
	strict[wire.Plan](t, w)
	if w.Code != 200 || plan.Image.Sha256 != f.wim || plan.EditionIndex != 6 || plan.BootManager != "windows-uefi-ca-2023" ||
		plan.BootOrderAfterDeploy != "network-first" || plan.OsDrivers == nil || len(*plan.OsDrivers) != 2 || plan.Disk.TargetDiskIndex != nil {
		t.Fatalf("plan: %d %s", w.Code, w.Body)
	}
	paths := map[string]bool{}
	for _, d := range *plan.OsDrivers {
		paths[d.Path] = true
	}
	if !paths["1/e1r.inf"] || !paths["1/e1r.sys"] {
		t.Errorf("driver paths %v", paths)
	}
	if w := f.call("GET", "/agent/deployment", "nope", ""); w.Code != 401 {
		t.Errorf("bad token: %d", w.Code)
	}

	// Files: the plan's, with ranges; nothing else.
	if !strings.HasSuffix(plan.Image.ContentUrl, "?name=install.wim") {
		t.Errorf("image URL %s", plan.Image.ContentUrl)
	}
	imgURL := strings.TrimPrefix(plan.Image.ContentUrl, "/api/v1")
	if w := f.call("GET", imgURL, tok.Token, ""); w.Code != 200 || w.Body.String() != "WIM DATA 0123456789" {
		t.Errorf("file: %d %q", w.Code, w.Body)
	}
	if w := f.call("GET", imgURL, tok.Token, "", "Range", "bytes=9-"); w.Code != 206 || w.Body.String() != "0123456789" {
		t.Errorf("range: %d %q", w.Code, w.Body)
	}
	if w := f.call("GET", imgURL, "", ""); w.Code != 401 {
		t.Errorf("file without token: %d", w.Code)
	}
	if w := f.call("GET", "/agent/files/"+strings.Repeat("c", 64), tok.Token, ""); w.Code != 404 {
		t.Errorf("file outside the plan: %d", w.Code)
	}

	// Progress moves forward only; logs are kept.
	f.call("POST", "/agent/progress", tok.Token, `{"stage":"applying-image","percent":40,"message":"applying"}`)
	if w := f.call("POST", "/agent/progress", tok.Token, `{"stage":"partitioning","percent":100}`); w.Code != 204 {
		t.Errorf("progress: %d", w.Code)
	}
	d, _ := f.st.Deployment(f.ctx, tok.DeploymentId)
	if *d.Stage != "applying-image" || d.Percent != 40 {
		t.Errorf("stage went backwards: %v %d", *d.Stage, d.Percent)
	}
	f.call("POST", "/agent/logs", tok.Token, `{"entries":[{"at":"2026-11-08T12:00:00Z","level":"info","message":"hello"}]}`)
	w = f.call("GET", "/deployments/"+tok.DeploymentId+"/logs", "", "")
	if logs := decode[api.LogEntryPage](t, w); len(logs.Items) != 1 || logs.Items[0].Message != "hello" {
		t.Errorf("logs: %s", w.Body)
	}

	// The client restarts mid-deployment: the deployment fails, its token
	// stops working.
	f.call("POST", "/agent/check-in", "", identity)
	if d, _ := f.st.Deployment(f.ctx, tok.DeploymentId); d.Status != "failed" || !strings.HasPrefix(d.Message, "restarted") {
		t.Errorf("after restart: %s %q", d.Status, d.Message)
	}
	if w := f.call("GET", "/agent/deployment", tok.Token, ""); w.Code != 401 {
		t.Errorf("token after restart: %d", w.Code)
	}

	// Again, to the end: complete is idempotent.
	ul = decode[api.AgentUnlockResult](t, unlock("correct horse"))
	tok = decode[api.AgentToken](t, f.call("POST", "/agent/deployments", ul.SessionToken, `{"profileId":"`+f.profile+`"}`))
	for i := 0; i < 2; i++ {
		if w := f.call("POST", "/agent/complete", tok.Token, `{"result":"succeeded","message":"done"}`); w.Code != 204 {
			t.Fatalf("complete %d: %d %s", i, w.Code, w.Body)
		}
	}
	w = f.call("GET", "/deployments?clientId="+ci.ClientId, "", "")
	page := decode[api.DeploymentPage](t, w)
	if len(page.Items) != 2 || page.Items[0].Status != "succeeded" || page.Items[1].Status != "failed" {
		t.Errorf("deployments: %s", w.Body)
	}
	// A check-in after success doesn't touch it.
	f.call("POST", "/agent/check-in", "", identity)
	if d, _ := f.st.Deployment(f.ctx, tok.DeploymentId); d.Status != "succeeded" {
		t.Errorf("succeeded deployment changed: %s", d.Status)
	}
}

func TestAgentArmedCheckIn(t *testing.T) {
	f := newAgentFixture(t)
	c, err := f.st.CreateClient(f.ctx, store.ClientInput{MAC: "00:15:5d:00:85:09"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := f.st.Profile(f.ctx, f.profile)
	snapshot, _ := profileSnapshot(p)
	d, err := f.st.CreateDeployment(f.ctx, store.NewDeployment{ClientID: c.ID, ProfileID: f.profile, ProfileSnapshot: snapshot, Origin: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	w := f.call("POST", "/agent/check-in", "", identity)
	ci := decode[api.AgentCheckInResult](t, w)
	strict[wire.CheckInResult](t, w)
	if ci.State != "armed" || ci.Armed == nil || ci.Armed.DeploymentId != d.ID || ci.Armed.ProfileName != "Win11 Pro" || ci.Armed.CountdownSeconds != 10 {
		t.Fatalf("armed check-in: %d %s", w.Code, w.Body)
	}
	if w := f.call("GET", "/agent/deployment", ci.Armed.Token, ""); w.Code != 200 {
		t.Errorf("plan: %d", w.Code)
	}
	if d, _ := f.st.Deployment(f.ctx, d.ID); d.Status != "running" {
		t.Errorf("not consumed: %s", d.Status)
	}
}
