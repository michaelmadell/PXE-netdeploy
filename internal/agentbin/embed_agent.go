//go:build agent

package agentbin

import (
	"embed"
	"io/fs"
)

//go:embed bin/*.exe
var bin embed.FS

var embedded = func() fs.FS {
	sub, err := fs.Sub(bin, "bin")
	if err != nil {
		panic(err)
	}
	return sub
}()
