// Package web is the HTTP+WS frontend for cmt-top.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// SPA returns the bundled SPA filesystem rooted at dist/.
func SPA() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
