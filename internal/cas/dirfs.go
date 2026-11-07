package cas

import (
	"io/fs"
	"os"
	"path"
	"strings"
)

// DirFS serves a plain directory with the same rules as a Tree: paths match
// case-insensitively (an exact match wins) and nothing outside dir can be
// reached. It backs the temporary -tftp-root flag until boot images are
// trees (Phase 4).
func DirFS(dir string) fs.FS { return dirFS(dir) }

type dirFS string

func (d dirFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	clean, err := CleanPath(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	root, err := os.OpenRoot(string(d))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	defer root.Close() // files opened from it stay valid
	rel, err := resolveFold(root, clean)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return root.Open(rel)
}

// resolveFold finds clean under root, matching each component
// case-insensitively when there's no exact match.
func resolveFold(root *os.Root, clean string) (string, error) {
	if _, err := root.Stat(clean); err == nil {
		return clean, nil
	}
	cur := "."
	for _, part := range strings.Split(clean, "/") {
		d, err := root.Open(cur)
		if err != nil {
			return "", fs.ErrNotExist
		}
		entries, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return "", fs.ErrNotExist
		}
		match := ""
		for _, e := range entries {
			if e.Name() == part {
				match = part
				break
			}
			if match == "" && strings.EqualFold(e.Name(), part) {
				match = e.Name()
			}
		}
		if match == "" {
			return "", fs.ErrNotExist
		}
		cur = path.Join(cur, match)
	}
	return cur, nil
}
