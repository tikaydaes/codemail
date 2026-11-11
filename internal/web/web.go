// Package web holds the web app, embedded in the binary. It is plain
// HTML, CSS and JavaScript modules with no build step.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// Files is the web app's file tree, rooted at its index.html.
func Files() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
