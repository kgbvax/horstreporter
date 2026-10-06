// Package horstreporter holds the repo-root assets embedded into the
// horstreporter binary (cmd/horstreporter). go:embed can only reach files at
// or below the declaring package's directory, so the embeds live here at the
// module root, next to static/ and cty.dat.
package horstreporter

import "embed"

// StaticFiles is the web UI (static/) served in production; -dev serves the
// same directory from disk instead.
//
//go:embed static
var StaticFiles embed.FS

// CtyData is cty.dat, embedded so DX-cluster country/flag labelling works out
// of the box (no file to deploy). Refreshed whenever the binary is rebuilt;
// -cty-path can override it with a newer file at runtime.
//
//go:embed cty.dat
var CtyData []byte
