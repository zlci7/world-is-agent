// Package console carries the built client so the Runtime can hand it to a
// browser from its own binary.
//
// The client is a separate build (see web/), and dist/ is its output. It is not
// committed, so a Runtime built from a fresh checkout reports a missing client
// instead of serving a stale one: an embedded asset that silently lags its
// source is worse than a clear message.
package console

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built client rooted at its web root, so that "index.html"
// is the entry point. It returns nil when the client has not been built, which
// the control plane reports to the browser.
func Assets() fs.FS {
	rooted, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return rooted
}
