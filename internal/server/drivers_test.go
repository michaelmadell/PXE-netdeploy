package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/imports"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
)

const testNIC = "[Version]\r\nSignature=\"$WINDOWS NT$\"\r\nClass=Net\r\nProvider=Test\r\nDriverVer=01/02/2024,1.0.0.0\r\n\r\n" +
	"[Manufacturer]\r\nTest=Models,NTamd64\r\n\r\n[Models.NTamd64]\r\nNIC=Install, PCI\\VEN_8086&DEV_15F3\r\n"

func TestDriversAPI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	content, err := cas.Open(ctx, cas.Options{Root: filepath.Join(dir, "content"), Store: st, Reserve: func() int64 { return 0 }, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	up := uploads.New(uploads.Options{Store: st, Content: content, Logger: quiet})
	runner := jobs.New(jobs.Options{Store: st, Logger: quiet})
	bus := events.New()
	runner.Register(imports.NewDrivers(imports.DriversOptions{Store: st, Content: content, Uploads: up, Events: bus, Logger: quiet}).Kind())
	rctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); <-runner.Done() }()
	runner.Start(rctx)

	spec, _ := api.GetSwagger()
	validate, _ := RequestValidator(spec)
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(New("test", Deps{Store: st, Events: bus, Content: content, Jobs: runner, Uploads: up}), nil,
		api.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	h := Recover(validate(mux))

	newUpload := func(name string, b []byte) string {
		u, _ := st.CreateUpload(ctx, store.NewUpload{Length: int64(len(b)), Filename: &name, HashState: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)})
		p, _ := up.Path(u.ID)
		os.WriteFile(p, b, 0o600)
		sum := sha256.Sum256(b)
		sha := hex.EncodeToString(sum[:])
		st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: int64(len(b)), HashState: []byte("x"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)})
		return u.ID
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("nic/nic.inf")
	w.Write([]byte(testNIC))
	z.Close()

	// Installers are refused before a job is queued.
	if rec, p := call(h, "POST", "/api/v1/driver-packages", `{"uploadId":"`+newUpload("setup.exe", append([]byte("MZ"), make([]byte, 100)...))+`","name":"x"}`); rec.Code != http.StatusBadRequest || p["code"] != "unsupported_archive" {
		t.Errorf("installer: %d %v", rec.Code, p)
	}
	if rec, p := call(h, "POST", "/api/v1/driver-packages", `{"uploadId":"nope","name":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("missing upload: %d %v", rec.Code, p)
	}

	rec, job := call(h, "POST", "/api/v1/driver-packages", `{"uploadId":"`+newUpload("nic.zip", buf.Bytes())+`","name":"NIC"}`)
	if rec.Code != http.StatusAccepted || job["kind"] != "driverPackage.import" {
		t.Fatalf("import: %d %v", rec.Code, job)
	}
	pkgID := job["resourceId"].(string)
	waitJob(t, h, job["id"].(string), "succeeded")

	rec, pkg := call(h, "GET", "/api/v1/driver-packages/"+pkgID, "")
	infs, _ := pkg["infs"].([]any)
	if rec.Code != http.StatusOK || pkg["status"] != "ready" || len(infs) != 1 || rec.Header().Get("ETag") == "" {
		t.Fatalf("get: %d %v", rec.Code, pkg)
	}
	inf := infs[0].(map[string]any)
	if inf["role"] != "network" || inf["signature"] != "unsigned" || inf["enabled"] != true || inf["driverDate"] != "2024-01-02" {
		t.Errorf("inf: %v", inf)
	}
	etag := rec.Header().Get("ETag")

	// Filters.
	for q, n := range map[string]int{"role=network": 1, "role=storage": 0, "class=net": 1, `hardwareId=PCI%5CVEN_8086%26DEV_15F3%26SUBSYS_00008086`: 1, "hardwareId=PCI%5CVEN_8086": 0} {
		if _, page := call(h, "GET", "/api/v1/driver-packages?"+q, ""); len(page["items"].([]any)) != n {
			t.Errorf("list %s: %v", q, page)
		}
	}

	// Disable the INF; an unknown INF id is a 400.
	if rec, p := call(h, "PATCH", "/api/v1/driver-packages/"+pkgID, `{"infs":[{"id":"nope","enabled":false}]}`, "If-Match", etag); rec.Code != http.StatusBadRequest || p["code"] != "unknown_inf" {
		t.Errorf("unknown inf: %d %v", rec.Code, p)
	}
	rec, pkg = call(h, "PATCH", "/api/v1/driver-packages/"+pkgID, `{"name":"Intel NIC","infs":[{"id":"`+inf["id"].(string)+`","enabled":false}]}`, "If-Match", etag)
	if rec.Code != http.StatusOK || pkg["name"] != "Intel NIC" || pkg["infs"].([]any)[0].(map[string]any)["enabled"] != false {
		t.Errorf("patch: %d %v", rec.Code, pkg)
	}
	if rec, _ := call(h, "PATCH", "/api/v1/driver-packages/"+pkgID, `{"name":"y"}`, "If-Match", etag); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("stale patch: %d", rec.Code)
	}

	// Duplicate archive: a finished job pointing at the package.
	if rec, dup := call(h, "POST", "/api/v1/driver-packages", `{"uploadId":"`+newUpload("again.zip", buf.Bytes())+`","name":"Again"}`); rec.Code != http.StatusAccepted || dup["status"] != "succeeded" || dup["resourceId"] != pkgID {
		t.Errorf("duplicate: %d %v", rec.Code, dup)
	}

	// Groups.
	if rec, p := call(h, "POST", "/api/v1/driver-groups", `{"name":"G","driverPackageIds":["nope"]}`); rec.Code != http.StatusUnprocessableEntity || p["code"] != "unknown_driver_package" {
		t.Errorf("group with unknown package: %d %v", rec.Code, p)
	}
	rec, g := call(h, "POST", "/api/v1/driver-groups", `{"name":"Lab NICs","description":"x","driverPackageIds":["`+pkgID+`"]}`)
	if rec.Code != http.StatusCreated || len(g["driverPackageIds"].([]any)) != 1 {
		t.Fatalf("create group: %d %v", rec.Code, g)
	}
	gid := g["id"].(string)
	rec, _ = call(h, "GET", "/api/v1/driver-groups/"+gid, "")
	getag := rec.Header().Get("ETag")
	if rec.Code != http.StatusOK || getag == "" {
		t.Errorf("get group: %d", rec.Code)
	}
	if _, page := call(h, "GET", "/api/v1/driver-groups", ""); len(page["items"].([]any)) != 1 {
		t.Errorf("list groups: %v", page)
	}
	// In a group: the package can't be deleted.
	if rec, p := call(h, "DELETE", "/api/v1/driver-packages/"+pkgID, ""); rec.Code != http.StatusConflict || p["code"] != "package_in_use" {
		t.Errorf("delete in use: %d %v", rec.Code, p)
	}
	if rec, g := call(h, "PUT", "/api/v1/driver-groups/"+gid, `{"name":"Empty","driverPackageIds":[]}`, "If-Match", getag); rec.Code != http.StatusOK || len(g["driverPackageIds"].([]any)) != 0 {
		t.Errorf("replace group: %d %v", rec.Code, g)
	}
	if rec, _ := call(h, "PUT", "/api/v1/driver-groups/"+gid, `{"name":"Stale","driverPackageIds":[]}`, "If-Match", getag); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("stale replace: %d", rec.Code)
	}
	if rec, _ := call(h, "DELETE", "/api/v1/driver-groups/"+gid, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete group: %d", rec.Code)
	}

	if rec, _ := call(h, "DELETE", "/api/v1/driver-packages/"+pkgID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if n, _ := st.ContentBytes(ctx); n != 0 {
		t.Errorf("%d bytes left after delete", n)
	}

	// Capabilities.
	rec, caps := call(h, "GET", "/api/v1/system/capabilities", "")
	if rec.Code != http.StatusOK || caps["version"] != "test" || caps["cabLzx"] == nil || len(caps["builders"].([]any)) != 2 {
		t.Errorf("capabilities: %d %v", rec.Code, caps)
	}
	t.Logf("capabilities: %v", caps)
}
