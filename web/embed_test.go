//go:build ui

package web

import (
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerCaching(t *testing.T) {
	sub, _ := fs.Sub(dist, "dist")
	var asset string
	fs.WalkDir(sub, "assets", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && asset == "" && strings.HasSuffix(p, ".js") {
			asset = p
		}
		return nil
	})
	if asset == "" {
		t.Skip("no built assets")
	}
	h := Handler()
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		return w
	}
	for p, want := range map[string]struct {
		code  int
		cache string
		html  bool
	}{
		"/":                      {200, "no-cache", true},
		"/clients/x":             {200, "no-cache", true},
		"/" + asset:              {200, "public, max-age=31536000, immutable", false},
		"/assets/Gone-abc123.js": {404, "no-store", false},
	} {
		w := get(p)
		if w.Code != want.code || w.Header().Get("Cache-Control") != want.cache || strings.Contains(w.Body.String(), "<!doctype html") != want.html {
			t.Errorf("%s: %d %q %.60q", p, w.Code, w.Header().Get("Cache-Control"), w.Body.String())
		}
	}
}
