// Package web embeds the HTML templates and static assets that are served by
// the CronMon HTTP server. All files under templates/ and static/ are bundled
// into the binary at compile time via Go's embed package so that no extra
// files need to be deployed alongside the binary.
package web

import "embed"

// FS contains all templates and static assets embedded into the binary.
//
//go:embed templates static
var FS embed.FS
