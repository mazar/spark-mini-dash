// Package web embeds the dashboard UI (no build step, no dependencies) and
// serves it from the spark-dash binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html css js
var assets embed.FS

// Handler serves the embedded UI files.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, ".")
	if err != nil {
		panic(err) // embed layout is fixed at build time
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(sub))
	return mux
}
