package panel

import "embed"

// Static holds the page: index.html is the shell's template, the rest is served
// under /static/.
//
//go:embed static
var Static embed.FS
