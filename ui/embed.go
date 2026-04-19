// Package uiembed provides the embedded UI static assets for the syd binary.
// The ui/dist directory is produced by "npm run build" in the ui/ directory.
package uiembed

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist/*
var assets embed.FS

// Handler returns an http.Handler that serves the embedded UI assets.
// All paths that don't match a static file are served index.html (SPA fallback).
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		panic("uiembed: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to serve the file directly. If it doesn't exist, serve index.html
		// (SPA client-side routing fallback).
		path := r.URL.Path
		if path == "/" {
			r.URL.Path = "/index.html"
			fileServer.ServeHTTP(w, r)
			return
		}

		// Check if file exists
		f, err := sub.Open(path[1:]) // strip leading /
		if err != nil {
			// SPA fallback: serve index.html for unknown paths
			r.URL.Path = "/index.html"
			fileServer.ServeHTTP(w, r)
			return
		}
		f.Close()
		fileServer.ServeHTTP(w, r)
	})
}
