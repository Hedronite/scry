// Package vigil holds the proof that scry.status/v0 maps into the scry{} block
// of Vigil's vigil.summary/v0 (docs/vigil.md).
//
// There is no production code here on purpose. Scry ships the writer
// (status --out), the mapping document and the golden pairs under
// testdata/vigil; the reader that builds scry{} lives in Vigil. The tests hold
// a small reference mapper and check it, the goldens and the document against a
// vendored copy of Vigil's schema.
package vigil
