package skillet

import _ "embed"

// RegistryJSON is the curated skill index, embedded at build time so the binary
// is fully self-contained (zero runtime dependencies).
//
//go:embed registry.json
var RegistryJSON []byte
