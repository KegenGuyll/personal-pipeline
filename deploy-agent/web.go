package main

import (
	"embed"
	"io/fs"
	"net/http"
)

// webFS embeds the static dashboard (no build step). Served at /ui/ by the
// agent; the data endpoints remain token-gated.
//
//go:embed web
var webFS embed.FS

func webHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// The embedded directory is fixed at build time; this cannot fail.
		panic(err)
	}
	return http.FileServerFS(sub)
}
