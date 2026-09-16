//go:build !webui

package web

import (
	"embed"
	"io/fs"
)

//go:embed stub.html
var stubFS embed.FS

// SPA provides an intentional stub for builds without the webui build tag.
func SPA() fs.FS { return stubFS }
