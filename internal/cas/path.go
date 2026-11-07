package cas

import (
	"errors"
	"strings"
)

// ErrBadPath: a path that can't be stored or looked up.
var ErrBadPath = errors.New("cas: invalid path")

// CleanPath normalises a path from media or a client: '\' becomes '/',
// empty and "." components are dropped, and "..", ':' (drive letters, NTFS
// streams) and NUL are refused.
func CleanPath(p string) (string, error) {
	parts := strings.Split(strings.ReplaceAll(p, `\`, "/"), "/")
	out := parts[:0]
	for _, part := range parts {
		switch {
		case part == "" || part == ".":
			continue
		case part == "..", strings.ContainsAny(part, ":\x00"):
			return "", ErrBadPath
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return "", ErrBadPath
	}
	return strings.Join(out, "/"), nil
}

// Fold is the case-insensitive lookup key for a cleaned path. Only ASCII is
// folded, matching SQLite's lower() in the tree_entries CHECK.
func Fold(p string) string {
	b := []byte(p)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
