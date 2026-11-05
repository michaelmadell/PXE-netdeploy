//go:build !ui

// Package web serves the built frontend. Without the `ui` build tag nothing is
// embedded, so `go build ./...` works before the frontend has been built.
package web

import "net/http"

// Handler explains how to reach the UI in a development build.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("This build has no embedded UI.\n\n" +
			"During development run `just dev` and open http://localhost:5173.\n" +
			"For a release build with the UI embedded, run `just build`.\n"))
	})
}
