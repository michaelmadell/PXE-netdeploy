package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// apiHandler is the admin API as main wires it (validation, idempotency,
// generated routing) on a real store.
func apiHandler(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	validate, err := RequestValidator(spec)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(New("test", Deps{Store: st, Events: events.New()}), nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError,
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	return Recover(validate(Idempotency(st)(mux))), st
}

func call(h http.Handler, method, path, body string, headers ...string) (*httptest.ResponseRecorder, map[string]any) {
	hm := map[string]string{}
	for i := 0; i+1 < len(headers); i += 2 {
		hm[headers[i]] = headers[i+1]
	}
	rec := do(h, method, path, body, hm)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

func TestClientsCRUD(t *testing.T) {
	h, _ := apiHandler(t)

	rec, c := call(h, "POST", "/api/v1/clients", `{"mac":"00-15-5D-00-00-01","hostname":"lab-01","reservedIp":"10.77.0.50"}`)
	if rec.Code != http.StatusCreated || c["mac"] != "00:15:5d:00:00:01" || c["source"] != "manual" || c["etag"] != `"1"` {
		t.Fatalf("create: %d %v", rec.Code, c)
	}
	id := c["id"].(string)

	if rec, p := call(h, "POST", "/api/v1/clients", `{"mac":"00:15:5d:00:00:01"}`); rec.Code != http.StatusConflict || p["code"] != "mac_taken" {
		t.Errorf("duplicate mac: %d %v", rec.Code, p)
	}
	if rec, p := call(h, "POST", "/api/v1/clients", `{"mac":"00:15:5d:00:00:02","reservedIp":"10.77.0.50"}`); rec.Code != http.StatusConflict || p["code"] != "reserved_ip_taken" {
		t.Errorf("duplicate reservation: %d %v", rec.Code, p)
	}
	if rec, p := call(h, "POST", "/api/v1/clients", `{"mac":"00:15:5d:00:00:03","profileId":"p1"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "profileId") {
		t.Errorf("unknown profileId: %d %v", rec.Code, p)
	}
	if rec, _ := call(h, "POST", "/api/v1/clients", `{"mac":"00:15:5d:00:00:04","reservedIp":"10.77.0"}`); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Errorf("bad reservedIp: %d", rec.Code)
	}

	if rec, got := call(h, "GET", "/api/v1/clients/"+id, ""); rec.Code != http.StatusOK || got["hostname"] != "lab-01" {
		t.Fatalf("get: %d %v", rec.Code, got)
	}
	if rec, _ := call(h, "GET", "/api/v1/clients/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get missing: %d", rec.Code)
	}

	put := `{"mac":"00:15:5d:00:00:01","hostname":"lab-01b"}`
	if rec, _ := call(h, "PUT", "/api/v1/clients/"+id, put, "If-Match", `"7"`); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: %d", rec.Code)
	}
	rec, got := call(h, "PUT", "/api/v1/clients/"+id, put, "If-Match", `"1"`)
	if rec.Code != http.StatusOK || got["hostname"] != "lab-01b" || got["etag"] != `"2"` || got["reservedIp"] != nil {
		t.Fatalf("replace: %d %v", rec.Code, got)
	}

	rec, page := call(h, "GET", "/api/v1/clients?source=manual", "")
	if rec.Code != http.StatusOK || len(page["items"].([]any)) != 1 {
		t.Fatalf("list: %d %v", rec.Code, page)
	}

	if rec, _ := call(h, "DELETE", "/api/v1/clients/"+id, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, _ := call(h, "DELETE", "/api/v1/clients/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete again: %d", rec.Code)
	}
}

func TestIdempotencyKey(t *testing.T) {
	h, st := apiHandler(t)
	body := `{"mac":"00:15:5d:00:00:01"}`

	first, c1 := call(h, "POST", "/api/v1/clients", body, "Idempotency-Key", "k1")
	again, c2 := call(h, "POST", "/api/v1/clients", body, "Idempotency-Key", "k1")
	if first.Code != http.StatusCreated || again.Code != http.StatusCreated || c1["id"] != c2["id"] {
		t.Fatalf("replay: %d %v / %d %v", first.Code, c1, again.Code, c2)
	}
	if again.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("replay not marked")
	}
	if rec, p := call(h, "POST", "/api/v1/clients", `{"mac":"00:15:5d:00:00:02"}`, "Idempotency-Key", "k1"); rec.Code != http.StatusUnprocessableEntity || p["code"] != "idempotency_key_reused" {
		t.Errorf("key reused with another body: %d %v", rec.Code, p)
	}
	// Without a key, the same body is a second create (and conflicts).
	if rec, _ := call(h, "POST", "/api/v1/clients", body); rec.Code != http.StatusConflict {
		t.Errorf("no key: %d", rec.Code)
	}

	// Concurrent requests with one key create one client.
	var wg sync.WaitGroup
	ids := make([]any, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c := call(h, "POST", "/api/v1/clients", `{"mac":"00:15:5d:00:00:09"}`, "Idempotency-Key", "k2")
			ids[i] = c["id"]
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id == nil || id != ids[0] {
			t.Fatalf("concurrent requests got different results: %v", ids)
		}
	}
	cs, _, err := st.ListClients(context.Background(), store.ClientFilter{MAC: "00:15:5d:00:00:09"})
	if err != nil || len(cs) != 1 {
		t.Fatalf("clients with that MAC: %d, %v", len(cs), err)
	}
}
