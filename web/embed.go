// Package web embeds the built single-page app (web/dist).
package web

import "embed"

// Dist holds the Vite build output. Run `make web` (or `make web-stub`) first.
//
//go:embed all:dist
var Dist embed.FS
