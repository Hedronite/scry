// Package schema embeds the hand-written JSON Schemas, which are the source of
// truth for every Scry document.
package schema

import _ "embed"

// StatusV0 is schema/scry.status.v0.json.
//
//go:embed scry.status.v0.json
var StatusV0 []byte

// VersionV0 is schema/scry.version.v0.json.
//
//go:embed scry.version.v0.json
var VersionV0 []byte
