package web

import "embed"

// DistFS embeds the built Vue SPA under web/dist.
//
//go:embed all:dist
var DistFS embed.FS
