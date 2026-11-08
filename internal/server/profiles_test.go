package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

func TestProfilesAPI(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// A ready x64 image with edition 6 and a boot image from it.
	name, sha := "w.iso", strings.Repeat("a", 64)
	u, _ := st.CreateUpload(ctx, store.NewUpload{Length: 10, Filename: &name, HashState: []byte("h"), ExpiresAt: time.Now().Add(time.Hour)})
	st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: 10, HashState: []byte("h"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)})
	im, _, _ := st.CreateImageImport(ctx, store.NewImageImport{Name: "Win11", UploadID: u.ID}, "image.import")
	tree, _ := st.InsertTree(ctx, store.NewTree{Kind: store.TreeImage, Entries: []store.NewTreeEntry{{Path: "a", Fold: "a", SHA256: strings.Repeat("b", 64), Size: 1}}})
	if err := st.CompleteImageImport(ctx, im.ID, store.ImportResult{Kind: "windows", TreeID: tree,
		Details: json.RawMessage(`{"kind":"windows","architecture":"x64","editions":[{"index":6,"name":"Windows 11 Pro"}]}`)}); err != nil {
		t.Fatal(err)
	}
	bi, err := st.CreateBootImage(ctx, store.BootImageInput{Name: "pe", SourceImageID: im.ID, SourceIndex: 1, Architecture: "x64",
		ServerURLs: []string{"http://x"}, Builder: "auto", Bootmgr: "auto", TFTPBlockSize: 16384, TFTPWindowSize: 16})
	if err != nil {
		t.Fatal(err)
	}

	spec, _ := api.GetSwagger()
	validate, _ := RequestValidator(spec)
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(New("test", Deps{Store: st, Events: events.New()}), nil,
		api.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	h := Recover(validate(mux))
	do := func(method, path, body string, hdr ...string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	profile := func(mod string) string {
		return `{"name":"Win11 Pro","bootImageId":"` + bi.ID + `","installImageId":"` + im.ID + `","editionIndex":6,` +
			`"installMethod":"agent","identity":{"mode":"none"}` + mod + `}`
	}

	for want, body := range map[string]string{
		"unknown_boot_image":        strings.Replace(profile(""), bi.ID, "nope", 1),
		"install_image_unavailable": strings.Replace(profile(""), im.ID, "nope", 1),
		"unknown_edition":           strings.Replace(profile(""), `"editionIndex":6`, `"editionIndex":2`, 1),
		"unknown_driver_group":      profile(`,"osDriverGroupIds":["nope"]`),
		"not_implemented":           profile(`,"unattendTemplateId":"t1"`),
	} {
		if code, b := do("POST", "/profiles", body); code != 422 || b["code"] != want {
			t.Errorf("%s: %d %v", want, code, b)
		}
	}
	code, b := do("POST", "/profiles", profile(`,"variables":{"site":"lab"}`))
	if code != 201 || b["bootOrderAfterDeploy"] != "disk-first" || b["identity"].(map[string]any)["mode"] != "none" {
		t.Fatalf("create: %d %v", code, b)
	}
	id, etag := b["id"].(string), b["etag"].(string)
	if code, b := do("GET", "/profiles/"+id, ""); code != 200 || b["variables"].(map[string]any)["site"] != "lab" {
		t.Errorf("get: %d %v", code, b)
	}
	if code, b := do("PUT", "/profiles/"+id, profile(`,"description":"lab"`), "If-Match", etag); code != 200 || b["description"] != "lab" {
		t.Errorf("replace: %d %v", code, b)
	}

	// A client using it: can't delete it; then the boot image can't go either.
	if code, b := do("POST", "/clients", `{"mac":"00:15:5d:00:00:01","profileId":"`+id+`"}`); code != 201 {
		t.Fatalf("client: %d %v", code, b)
	}
	if code, b := do("DELETE", "/profiles/"+id, ""); code != 409 || b["code"] != "profile_in_use" {
		t.Errorf("delete in use: %d %v", code, b)
	}
	if code, b := do("DELETE", "/boot-images/"+bi.ID, ""); code != 409 || b["code"] != "boot_image_in_use" {
		t.Errorf("delete boot image: %d %v", code, b)
	}
	if code, b := do("GET", "/profiles", ""); code != 200 || len(b["items"].([]any)) != 1 {
		t.Errorf("list: %d %v", code, b)
	}
}
