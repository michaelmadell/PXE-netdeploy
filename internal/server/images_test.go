package server

import (
	"compress/gzip"
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

func TestImagesAPI(t *testing.T) {
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
	runner.Register(imports.NewImages(imports.ImagesOptions{Store: st, Content: content, Uploads: up, Logger: quiet}).Kind())
	rctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); <-runner.Done() }()
	runner.Start(rctx)

	spec, _ := api.GetSwagger()
	validate, _ := RequestValidator(spec)
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(New("test", Deps{Store: st, Events: events.New(), Content: content, Jobs: runner}), nil,
		api.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	h := Recover(validate(mux))

	// The UDF fixture as a complete upload.
	f, _ := os.Open(filepath.Join("..", "media", "udf", "testdata", "udf102.iso.gz"))
	z, _ := gzip.NewReader(f)
	iso, _ := io.ReadAll(z)
	f.Close()
	newUpload := func() string {
		name := "fixture.iso"
		u, _ := st.CreateUpload(ctx, store.NewUpload{Length: int64(len(iso)), Filename: &name, HashState: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)})
		p, _ := up.Path(u.ID)
		os.WriteFile(p, iso, 0o600)
		sum := sha256.Sum256(iso)
		sha := hex.EncodeToString(sum[:])
		st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: int64(len(iso)), HashState: []byte("x"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)})
		return u.ID
	}

	if rec, p := call(h, "POST", "/api/v1/images", `{"uploadId":"nope","name":"x"}`); rec.Code != http.StatusNotFound || p["code"] != "not_found" {
		t.Errorf("missing upload: %d %v", rec.Code, p)
	}
	inc, _ := st.CreateUpload(ctx, store.NewUpload{Length: 5, HashState: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)})
	if rec, p := call(h, "POST", "/api/v1/images", `{"uploadId":"`+inc.ID+`","name":"x"}`); rec.Code != http.StatusConflict || p["code"] != "upload_incomplete" {
		t.Errorf("incomplete upload: %d %v", rec.Code, p)
	}

	rec, job := call(h, "POST", "/api/v1/images", `{"uploadId":"`+newUpload()+`","name":"Fixture","retainIso":true}`)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Location") != "/api/v1/jobs/"+job["id"].(string) || job["kind"] != "image.import" {
		t.Fatalf("import: %d %v %v", rec.Code, rec.Header(), job)
	}
	imageID := job["resourceId"].(string)
	waitJob(t, h, job["id"].(string), "succeeded")

	rec, img := call(h, "GET", "/api/v1/images/"+imageID, "")
	if rec.Code != http.StatusOK || img["status"] != "ready" || img["kind"] != "other" || img["volumeLabel"] != "NDTEST_UDF102" ||
		img["sourceFilename"] != "fixture.iso" || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d %v %v", rec.Code, rec.Header(), img)
	}
	if _, page := call(h, "GET", "/api/v1/images?kind=other", ""); len(page["items"].([]any)) != 1 {
		t.Errorf("list: %v", page)
	}
	if rec, _ := call(h, "PATCH", "/api/v1/images/"+imageID, `{"name":"Renamed"}`, "If-Match", `"1"`); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("stale patch: %d", rec.Code)
	}
	if rec, img := call(h, "PATCH", "/api/v1/images/"+imageID, `{"name":"Renamed","notes":"lab media"}`, "If-Match", `"2"`); rec.Code != http.StatusOK || img["name"] != "Renamed" {
		t.Errorf("patch: %d %v", rec.Code, img)
	}

	// The same ISO again: a job that's already done, pointing at the image.
	rec, dup := call(h, "POST", "/api/v1/images", `{"uploadId":"`+newUpload()+`","name":"Again"}`)
	if rec.Code != http.StatusAccepted || dup["status"] != "succeeded" || dup["resourceId"] != imageID {
		t.Errorf("duplicate: %d %v", rec.Code, dup)
	}

	if rec, _ := call(h, "DELETE", "/api/v1/images/"+imageID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec, _ := call(h, "GET", "/api/v1/images/"+imageID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d", rec.Code)
	}
}

func waitJob(t *testing.T, h http.Handler, id, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, j := call(h, "GET", "/api/v1/jobs/"+id, "")
		if j["status"] == want {
			return
		}
		if s := j["status"]; s == "failed" || s == "cancelled" || time.Now().After(deadline) {
			t.Fatalf("job %s: %v", id, j)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
