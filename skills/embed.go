// Package skills bundles the agent instructions shipped with this CLI version.
package skills

import "embed"

// Files contains the workbox skill and its self-contained references.
//
//go:embed workbox
var Files embed.FS
