package webassets

import "embed"

// FS holds the embedded web assets (templates and static files).
//
//go:embed templates static
var FS embed.FS
