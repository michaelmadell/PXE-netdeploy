package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/agentbin"
	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/bootimage"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// The build logic is tested in internal/bootimage; this checks the HTTP
// mapping.
func TestBootImagesAPI(t *testing.T) {
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
	boot := bootimage.New(bootimage.Options{Store: st, Content: content, Logger: quiet,
		Agents: agentbin.ForTest("v9", map[string][]byte{"x64": []byte("MZ")}),
		Detect: func(context.Context) bootimage.Host { return bootimage.Host{} }})
	spec, _ := api.GetSwagger()
	validate, _ := RequestValidator(spec)
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(New("test", Deps{Store: st, Events: events.New(), Content: content, Boot: boot}), nil,
		api.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	h := Recover(validate(mux))

	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	code, body := do("POST", "/boot-images", `{"name":"pe","sourceImageId":"nope","architecture":"x64","serverUrls":["http://10.77.0.1:8080"]}`)
	if code != 422 || body["code"] != "source_image_unavailable" {
		t.Errorf("unknown image: %d %v", code, body)
	}
	code, body = do("POST", "/boot-images", `{"name":"pe","sourceImageId":"x","architecture":"x64","serverUrls":["http://a"],"produceHttpBootIso":true}`)
	if code != 422 || body["code"] != "not_implemented" {
		t.Errorf("iso: %d %v", code, body)
	}
	code, body = do("POST", "/boot-images", `{"name":"pe","sourceImageId":"x","architecture":"x64","serverUrls":["ftp://a"]}`)
	if code != 400 {
		t.Errorf("ftp url: %d %v", code, body)
	}
	if code, _ = do("GET", "/boot-images/nope", ""); code != 404 {
		t.Errorf("get: %d", code)
	}
	if code, _ = do("POST", "/boot-images/nope/build", `{}`); code != 404 {
		t.Errorf("build: %d", code)
	}
	if code, _ = do("DELETE", "/boot-images/nope", ""); code != 404 {
		t.Errorf("delete: %d", code)
	}
	code, body = do("GET", "/boot-images", "")
	if code != 200 || len(body["items"].([]any)) != 0 {
		t.Errorf("list: %d %v", code, body)
	}
	code, body = do("GET", "/system/capabilities", "")
	agent, _ := body["agent"].(map[string]any)
	if code != 200 || agent["version"] != "v9" || len(agent["architectures"].([]any)) != 1 || body["elevated"] != false {
		t.Errorf("capabilities: %d %v", code, body)
	}
}
