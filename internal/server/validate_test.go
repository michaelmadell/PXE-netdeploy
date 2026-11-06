package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
)

func validated(t *testing.T) http.Handler {
	t.Helper()
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	v, err := RequestValidator(spec)
	if err != nil {
		t.Fatal(err)
	}
	return v(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
}

func do(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestValidatorReportsEachField(t *testing.T) {
	h := validated(t)
	body := `{"admin":{"bindAddress":"127.0.0.1","port":8443},
		"interfaces":[{"name":"lab0","dhcp":{"mode":"bogus"},"services":{}}],
		"tftp":{"maxBlockSize":1}}`
	rec := do(h, http.MethodPut, "/api/v1/settings", body, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var p problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, e := range p.Errors {
		fields[e.Field] = true
		if strings.Contains(e.Message, "Schema:") {
			t.Errorf("message leaks the schema dump: %q", e.Message)
		}
	}
	for _, want := range []string{"interfaces[0].dhcp.mode", "tftp.maxBlockSize"} {
		if !fields[want] {
			t.Errorf("no error for %s in %+v", want, p.Errors)
		}
	}
}

func TestValidatorIgnoresReadOnlyFields(t *testing.T) {
	h := validated(t)
	// A GET response sent back unchanged: etag, hasPassword and
	// externalDhcpGuidance are read-only and must not be rejected.
	body := `{"etag":"\"1\"","admin":{"bindAddress":"127.0.0.1","port":8443},
		"interfaces":[{"name":"lab0","dhcp":{"mode":"off"},"services":{},
			"externalDhcpGuidance":[{"arch":"uefi-x64","nextServer":"10.0.0.1","bootFile":"b","vendorClass":"PXEClient"}]}],
		"bootPrompt":{"hasPassword":true}}`
	if rec := do(h, http.MethodPut, "/api/v1/settings", body, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestValidatorNamesMissingHeader(t *testing.T) {
	rec := do(validated(t), http.MethodPut, "/api/v1/settings", `{"admin":{"bindAddress":"127.0.0.1","port":8443},"interfaces":[]}`, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "header If-Match") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestValidatorPassesUnknownRoutes(t *testing.T) {
	h := validated(t)
	for _, path := range []string{"/api/v1/nope", "/index.html"} {
		if rec := do(h, http.MethodGet, path, "", nil); rec.Code != http.StatusNoContent {
			t.Errorf("%s: status %d, want pass-through", path, rec.Code)
		}
	}
}
