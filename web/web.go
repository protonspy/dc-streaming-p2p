// Package web carries the browser assets into the binary: the SDK and the
// demonstration page that exercises it.
//
// They are embedded rather than served from disk so that a deployment is one file,
// and so that the page and the server it talks to can never be different versions
// of each other.
package web

import (
	"embed"
	"io/fs"
)

//go:embed demo sdk
var files embed.FS

// FS is the asset tree, rooted so that a request for /demo/index.html finds
// demo/index.html — which is what lets the page import the SDK by the same
// relative path a developer sees on disk.
func FS() fs.FS {
	return files
}
