package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
)

func TestSecretsAPI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	content, _ := cas.Open(ctx, cas.Options{Root: filepath.Join(dir, "content"), Store: st, Reserve: func() int64 { return 0 }, Logger: quiet})
	up := uploads.New(uploads.Options{Store: st, Content: content, Logger: quiet})
	vault := secrets.NewVault(st, secrets.NewKeys(dir))
	srv := New("test", Deps{Store: st, Events: events.New(), Uploads: up, Vault: vault})
	spec, _ := api.GetSwagger()
	validate, _ := RequestValidator(spec)
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(srv, nil, api.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	h := Recover(validate(mux))

	warned := func() bool {
		for _, w := range srv.secretsWarnings(ctx) {
			if w.Code == "secrets_key_not_exported" {
				return true
			}
		}
		return false
	}
	if warned() {
		t.Error("warning with no secrets")
	}

	rec, s := call(h, "POST", "/api/v1/secrets", `{"name":"NAS","kind":"share-credential","credential":{"username":"NAS01\\backup","password":"p@ss"}}`)
	if rec.Code != http.StatusCreated || s["hint"] != `NAS01\backup` || s["etag"] != `"1"` {
		t.Fatalf("create: %d %v", rec.Code, s)
	}
	id := s["id"].(string)
	if strings.Contains(rec.Body.String(), "p@ss") {
		t.Fatal("password in response")
	}
	// The value opens with the key now in the data directory.
	c, err := vault.Credential(ctx, id, "share-credential")
	if err != nil || c.Username != `NAS01\backup` || c.Password != "p@ss" {
		t.Fatalf("credential: %+v %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(dir, secrets.FileName)); err != nil {
		t.Errorf("key file: %v", err)
	}
	if !warned() {
		t.Error("no secrets_key_not_exported warning")
	}
	r, _ := vault.Keys().Ring(false)
	st.SetState(ctx, StateKeyExport, KeyExport{Fingerprint: r.Fingerprint()})
	if warned() {
		t.Error("warning after export")
	}

	for body, want := range map[string]int{
		`{"name":"x","kind":"password"}`: 400, // no value
		`{"name":"x","kind":"password","value":"a","credential":{"username":"u","password":"p"}}`: 400, // two
		`{"name":"x","kind":"product-key","value":"nope"}`:                                        400,
		`{"name":"x","kind":"product-key","value":"abcde-12345-abcde-12345-abcde"}`:               201,
		`{"name":"x","kind":"share-credential","credential":{"username":"u","password":""}}`:      400,
		`{"name":"x","kind":"odj-blob","uploadId":"nope"}`:                                        400,
	} {
		if rec, p := call(h, "POST", "/api/v1/secrets", body); rec.Code != want {
			t.Errorf("%s: %d %v", body, rec.Code, p)
		}
	}

	// File-based: the upload is consumed.
	blob := []byte(`{"CloudAssignedTenantId":"x"}`)
	name := "AutopilotConfigurationFile.json"
	u, _ := st.CreateUpload(ctx, store.NewUpload{Length: int64(len(blob)), Filename: &name, HashState: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)})
	p, _ := up.Path(u.ID)
	os.WriteFile(p, blob, 0o600)
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: int64(len(blob)), HashState: []byte("x"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)})
	rec, f := call(h, "POST", "/api/v1/secrets", `{"name":"AP","kind":"autopilot-json","uploadId":"`+u.ID+`"}`)
	if rec.Code != http.StatusCreated || f["hint"] != name {
		t.Fatalf("file secret: %d %v", rec.Code, f)
	}
	if _, err := st.Upload(ctx, u.ID); err == nil {
		t.Error("upload row kept")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("upload file kept")
	}
	if _, v, _ := vault.Open(ctx, f["id"].(string)); string(v) != string(blob) {
		t.Errorf("file value: %q", v)
	}

	if _, page := call(h, "GET", "/api/v1/secrets?kind=share-credential", ""); len(page["items"].([]any)) != 1 {
		t.Errorf("list: %v", page)
	}
	if rec, s := call(h, "PATCH", "/api/v1/secrets/"+id, `{"name":"NAS backup"}`, "If-Match", `"1"`); rec.Code != 200 || s["name"] != "NAS backup" {
		t.Errorf("patch: %d %v", rec.Code, s)
	}
	if rec, _ := call(h, "PUT", "/api/v1/secrets/"+id+"/value", `{"credential":{"username":"svc","password":"new"}}`, "If-Match", `"1"`); rec.Code != 412 {
		t.Errorf("stale rotate: %d", rec.Code)
	}
	if rec, s := call(h, "PUT", "/api/v1/secrets/"+id+"/value", `{"credential":{"username":"svc","password":"new"}}`, "If-Match", `"2"`); rec.Code != 200 || s["hint"] != "svc" {
		t.Errorf("rotate: %d %v", rec.Code, s)
	}
	if c, _ := vault.Credential(ctx, id, "share-credential"); c.Password != "new" {
		t.Errorf("rotated value: %+v", c)
	}

	// In use by the backup settings.
	st.InitSettings(ctx, []byte(`{"backup":{"destination":{"type":"smb","path":"\\\\nas\\b","credentialSecretId":"`+id+`"}}}`))
	if rec, p := call(h, "DELETE", "/api/v1/secrets/"+id, ""); rec.Code != 409 || p["code"] != "secret_in_use" || !strings.Contains(p["detail"].(string), "backup") {
		t.Errorf("delete in use: %d %v", rec.Code, p)
	}
}
