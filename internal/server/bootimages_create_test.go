package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agentbin"
	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/bootimage"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Creating a boot image from a real-shaped imported image (lab, 2026-11-08).
func TestCreateBootImage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	name, sha := "w.iso", strings.Repeat("a", 64)
	u, _ := st.CreateUpload(ctx, store.NewUpload{Length: 10, Filename: &name, HashState: []byte("h"), ExpiresAt: time.Now().Add(time.Hour)})
	st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: 10, HashState: []byte("h"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)})
	im, _, _ := st.CreateImageImport(ctx, store.NewImageImport{Name: "Win11", UploadID: u.ID}, "image.import")
	blob := strings.Repeat("b", 64)
	tree, _ := st.InsertTree(ctx, store.NewTree{Kind: store.TreeImage, Entries: []store.NewTreeEntry{{Path: "sources/boot.wim", Fold: "sources/boot.wim", SHA256: blob, Size: 1}}})
	x64 := "x64"
	if err := st.CompleteImageImport(ctx, im.ID, store.ImportResult{Kind: "windows", TreeID: tree,
		Details: json.RawMessage(`{"kind":"windows","architecture":"x64","installImageFormat":"wim","editions":[{"index":6,"name":"Pro"}],
			"bootImageIndexes":[{"index":1,"name":"Microsoft Windows PE (amd64)"},{"index":2,"name":"Microsoft Windows Setup (amd64)"}],"bootManagers":[]}`),
		BootManagers: []store.BootManager{{SourcePath: "boot.wim:1/Windows/Boot/EFI_EX/bootmgfw_EX.efi", SHA256: blob, Signer: "windows-uefi-ca-2023", Architecture: &x64}}}); err != nil {
		t.Fatal(err)
	}
	boot := bootimage.New(bootimage.Options{Store: st, Agents: agentbin.ForTest("dev", map[string][]byte{"x64": []byte("MZ")}),
		Detect: func(context.Context) bootimage.Host { return bootimage.Host{} }})
	spec, _ := api.GetSwagger()
	validate, _ := RequestValidator(spec)
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(New("test", Deps{Store: st, Events: events.New(), Boot: boot}), nil,
		api.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	h := Recover(validate(mux))

	body := `{"name":"WinPE x64","sourceImageId":"` + im.ID + `","architecture":"x64","serverUrls":["http://10.77.0.1:8080"]}`
	req := httptest.NewRequest("POST", "/api/v1/boot-images", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
}
