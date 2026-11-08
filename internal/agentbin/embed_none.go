//go:build !agent

package agentbin

import "io/fs"

// embedded is nil without the `agent` build tag: agents come from a directory.
var embedded fs.FS
