//go:build ui

// Package web serves the built frontend. Built with `-tags ui` (see `just build`)
// the contents of web/dist are embedded in the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the SPA, falling back to index.html for client-side routes.
//
// index.html is never cached, so a browser picks up a new build's asset
// names at once; the hashed files under assets/ are cached for good, and a
// missing one is a 404 rather than index.html (a page from an older build
// then fails its import cleanly, and the UI reloads itself).
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		_, err := fs.Stat(sub, p)
		switch {
		case p != "" && err == nil && strings.HasPrefix(p, "assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case p != "" && err == nil:
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasPrefix(p, "assets/"):
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		default: // index.html, for "/" and client-side routes
			w.Header().Set("Cache-Control", "no-cache")
			if p != "" {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}
