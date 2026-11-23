package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/ipxe"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

const ubuntuDetails = `{"kind":"linux","family":"ubuntu","architecture":"x64","deployable":true,
 "distribution":"Ubuntu-Server",
 "boot":{"shim":{"sourcePath":"EFI/boot/bootx64.efi","sha256":"` + "%s" + `","signer":"uefi-ca-2011","signers":["uefi-ca-2011","uefi-ca-2023"]},
  "kernel":{"sourcePath":"casper/vmlinuz","sha256":"%s","signer":"unknown"},
  "initrd":{"sourcePath":"casper/initrd","sha256":"%s"}}}`

// addLinux adds a ready Ubuntu image (with its ISO) and a Linux profile;
// it returns the profile and image ids.
func (f *agentFixture) addLinux(name, kernelArgs string) (string, string) {
	f.t.Helper()
	ctx := f.ctx
	stg, _ := f.content.NewStaging("img")
	shim, _, _ := stg.Add("EFI/boot/bootx64.efi", strings.NewReader("shim"))
	kern, _, _ := stg.Add("casper/vmlinuz", strings.NewReader("kernel"))
	initrd, _, _ := stg.Add("casper/initrd", strings.NewReader("initrd"))
	iso, _, _ := stg.Add(".iso-copy", strings.NewReader("ISO "+name))
	fn := "u.iso"
	u, _ := f.st.CreateUpload(ctx, store.NewUpload{Length: 10, Filename: &fn, HashState: []byte("h"), ExpiresAt: time.Now().Add(time.Hour)})
	f.st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: 10, HashState: []byte("h"), SHA256: &iso, ExpiresAt: time.Now().Add(time.Hour)})
	im, _, err := f.st.CreateImageImport(ctx, store.NewImageImport{Name: name, UploadID: u.ID}, "image.import")
	if err != nil {
		f.t.Fatal(err)
	}
	details := json.RawMessage(strings.NewReplacer("\n", "").Replace(sprintf(ubuntuDetails, shim, kern, initrd)))
	if _, err := stg.Commit(ctx, store.TreeImage, func(ctx context.Context, tree string) error {
		return f.st.CompleteImageImport(ctx, im.ID, store.ImportResult{Kind: "linux", TreeID: tree, ISOBlob: &iso, ForceRetainISO: true, Details: details})
	}); err != nil {
		f.t.Fatal(err)
	}
	p, err := f.st.CreateProfile(ctx, store.ProfileInput{Kind: store.ProfileLinux, Name: name, InstallImageID: im.ID, KernelArgs: kernelArgs})
	if err != nil {
		f.t.Fatal(err)
	}
	dir := f.t.TempDir()
	for _, n := range ipxe.Bundled {
		p := filepath.Join(dir, "x64", n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(n), 0o644)
	}
	if f.srv.ipxe, err = ipxe.Load(dir); err != nil {
		f.t.Fatal(err)
	}
	f.srv.agent.httpEndpoints = func() []netip.AddrPort {
		return []netip.AddrPort{netip.MustParseAddrPort("10.77.0.1:8080")}
	}
	return p.ID, im.ID
}

// ipxe posts a form as iPXE's params does, to the server at 10.77.0.1.
func (f *agentFixture) ipxe(path string, form url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agent/ipxe/"+path, strings.NewReader(form.Encode()))
	req.RemoteAddr = "10.77.0.102:50000"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), httpLocalAddr, fakeAddr("10.77.0.1:8080")))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "#!ipxe") {
		f.t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	return w
}

const nucMAC = "88:ae:dd:0a:11:22"

func TestAgentIpxeMenuAndStart(t *testing.T) {
	f := newAgentFixture(t)
	pid, imid := f.addLinux("Ubuntu 26.04", "autoinstall ds=nocloud")

	// Wrong password: a script that goes back to the gate.
	w := f.ipxe("unlock", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "password": {"nope"}})
	if !strings.Contains(w.Body.String(), "wrong password") || !strings.Contains(w.Body.String(), "autoexec.ipxe") {
		t.Errorf("wrong password:\n%s", w.Body)
	}

	// The menu lists only Linux profiles, and carries a token.
	w = f.ipxe("unlock", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "password": {"correct horse"}})
	menu := w.Body.String()
	if !strings.Contains(menu, "item "+pid+" Ubuntu 26.04") || strings.Contains(menu, f.profile) {
		t.Fatalf("menu:\n%s", menu)
	}
	m := regexp.MustCompile(`param unlockToken (\S+)`).FindStringSubmatch(menu)
	if m == nil {
		t.Fatalf("no token:\n%s", menu)
	}
	// arm64 machines get no x64 profiles.
	if w := f.ipxe("unlock", url.Values{"mac": {nucMAC}, "arch": {"arm64"}, "password": {"correct horse"}}); !strings.Contains(w.Body.String(), "no Linux profile") {
		t.Errorf("arm64:\n%s", w.Body)
	}

	// Another machine can't use the token (and burns it).
	if w := f.ipxe("start", url.Values{"mac": {"00:11:22:33:44:55"}, "arch": {"x64"}, "unlockToken": {m[1]}, "profileId": {pid}}); !strings.Contains(w.Body.String(), "expired or was already used") {
		t.Errorf("other MAC:\n%s", w.Body)
	}
	w = f.ipxe("unlock", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "password": {"correct horse"}})
	m = regexp.MustCompile(`param unlockToken (\S+)`).FindStringSubmatch(w.Body.String())
	w = f.ipxe("start", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "unlockToken": {m[1]}, "profileId": {pid}})
	body := w.Body.String()
	base := "/content/linux/" + imid + "/"
	for _, want := range []string{
		"shim " + base + "EFI/boot/bootx64.efi",
		"kernel --name vmlinuz " + base + "casper/vmlinuz initrd=initrd ip=dhcp url=http://10.77.0.1:8080" + base + "image.iso cloud-config-url=/dev/null autoinstall ds=nocloud",
		"initrd --name initrd " + base + "casper/initrd",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("boot script lacks %q:\n%s", want, body)
		}
	}
	c, err := f.st.ClientByMAC(f.ctx, nucMAC)
	if err != nil {
		t.Fatal(err)
	}
	// An interactive install isn't tracked: it ends when it starts (ADR 0036).
	ds, _, err := f.st.ListDeployments(f.ctx, c.ID, 10, "")
	if err != nil || len(ds) != 1 || ds[0].Status != store.StatusSucceeded || ds[0].Origin != store.OriginBootPrompt {
		t.Fatalf("deployment: %+v %v", ds, err)
	}
	// The token is single-use.
	if w := f.ipxe("start", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "unlockToken": {m[1]}, "profileId": {pid}}); !strings.Contains(w.Body.String(), "expired or was already used") {
		t.Errorf("reuse:\n%s", w.Body)
	}

	// WinPE's prompt doesn't offer or start Linux profiles.
	ci := decode[struct{ ClientId string }](t, f.call("POST", "/agent/check-in", "", identity))
	ul := decode[struct {
		SessionToken string
		Profiles     []struct{ Id string }
	}](t, f.call("POST", "/agent/unlock", "", `{"clientId":"`+ci.ClientId+`","password":"correct horse"}`))
	if len(ul.Profiles) != 1 || ul.Profiles[0].Id != f.profile {
		t.Errorf("WinPE profiles: %+v", ul.Profiles)
	}
	if w := f.call("POST", "/agent/deployments", ul.SessionToken, `{"profileId":"`+pid+`"}`); w.Code != 422 {
		t.Errorf("WinPE start of a Linux profile: %d %s", w.Code, w.Body)
	}
}

func TestAgentIpxeArmed(t *testing.T) {
	f := newAgentFixture(t)
	pid, _ := f.addLinux("Ubuntu", "")

	// Nothing armed.
	if w := f.ipxe("start", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "armed": {"true"}}); !strings.Contains(w.Body.String(), "no deployment is armed") {
		t.Errorf("unarmed:\n%s", w.Body)
	}
	c, _ := f.st.ClientByMAC(f.ctx, nucMAC)
	p, _ := f.st.Profile(f.ctx, pid)
	nd, prob, err := f.srv.prepareDeployment(f.ctx, c, p, store.OriginScheduled)
	if err != nil || prob != nil {
		t.Fatalf("prepare: %v %+v", err, prob)
	}
	d, err := f.st.CreateDeployment(f.ctx, nd)
	if err != nil {
		t.Fatal(err)
	}
	// WinPE doesn't consume a Linux deployment.
	f.call("POST", "/agent/check-in", "", `{"macs":["`+nucMAC+`"]}`)
	if d2, _ := f.st.Deployment(f.ctx, d.ID); d2.Status != store.StatusPending {
		t.Fatalf("WinPE consumed it: %s", d2.Status)
	}
	w := f.ipxe("start", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "armed": {"true"}})
	if !strings.Contains(w.Body.String(), "url=http://10.77.0.1:8080/content/linux/") {
		t.Fatalf("armed boot:\n%s", w.Body)
	}
	if d2, _ := f.st.Deployment(f.ctx, d.ID); d2.Status != store.StatusSucceeded || d2.StartedAt == nil {
		t.Errorf("status %s", d2.Status)
	}
	// Consumed: a second boot isn't armed.
	if w := f.ipxe("start", url.Values{"mac": {nucMAC}, "arch": {"x64"}, "armed": {"true"}}); !strings.Contains(w.Body.String(), "no deployment is armed") {
		t.Errorf("second boot:\n%s", w.Body)
	}
}

var httpLocalAddr = http.LocalAddrContextKey

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

func TestLinuxProfilesAPI(t *testing.T) {
	f := newAgentFixture(t)
	_, imid := f.addLinux("Ubuntu", "")
	winImage := f.winImage(t)
	type res struct {
		Code     string
		Kind     string
		Id       string
		Etag     string
		Findings []struct{ Code, Severity string }
	}
	post := func(body string) (int, res) {
		w := f.call("POST", "/profiles", "", body)
		var r res
		json.Unmarshal(w.Body.Bytes(), &r)
		return w.Code, r
	}
	linux := `{"kind":"linux","name":"U","installImageId":"` + imid + `","kernelArgs":"quiet"`
	for want, body := range map[string]string{
		"not_for_kind":              linux + `,"editionIndex":1}`,
		"install_image_unavailable": strings.Replace(linux, imid, winImage, 1) + `}`,
		"required_for_kind":         `{"name":"W","installImageId":"` + winImage + `"}`,
	} {
		if code, r := post(body); code != 422 || r.Code != want {
			t.Errorf("%s: %d %+v", want, code, r)
		}
	}
	code, r := post(linux + `}`)
	if code != 201 || r.Kind != "linux" {
		t.Fatalf("create: %d %+v", code, r)
	}
	// kind can't change on replace.
	wp, _ := f.st.Profile(f.ctx, f.profile)
	w := f.call("PUT", "/profiles/"+r.Id, "", `{"name":"U","installImageId":"`+winImage+`","editionIndex":6,"bootImageId":"`+wp.BootImageID+`","installMethod":"agent","identity":{"mode":"none"}}`, "If-Match", r.Etag)
	if !strings.Contains(w.Body.String(), "kind_changed") {
		t.Errorf("kind change: %d %s", w.Code, w.Body)
	}
	// Validation: the dual-signed shim draws no shim warning; HTTP Boot only.
	w = f.call("POST", "/profiles/"+r.Id+"/validate", "", "")
	var v res
	json.Unmarshal(w.Body.Bytes(), &v)
	codes := map[string]string{}
	for _, fd := range v.Findings {
		codes[fd.Code] = fd.Severity
	}
	if w.Code != 200 || codes["http_boot_only"] != "info" || codes["shim_2011_only"] != "" || codes["http_boot_unavailable"] != "" {
		t.Errorf("validate: %d %s", w.Code, w.Body)
	}
}

// winImage is the fixture's Windows image.
func (f *agentFixture) winImage(t *testing.T) string {
	p, err := f.st.Profile(f.ctx, f.profile)
	if err != nil {
		t.Fatal(err)
	}
	return p.InstallImageID
}
