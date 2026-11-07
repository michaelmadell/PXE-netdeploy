package cas

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// lookupTimeout bounds a manifest lookup (fs.FS has no context).
const lookupTimeout = 5 * time.Second

// Tree returns tree id as a read-only fs.FS. Open matches paths
// case-insensitively (ASCII) and accepts only files: there are no directory
// entries, so Open(".") and directories return fs.ErrNotExist. Opened files
// are *File (an *os.File on the blob, so io.ReaderAt and io.Seeker work).
func (s *Store) Tree(id string) fs.FS { return treeFS{s: s, id: id} }

type treeFS struct {
	s  *Store
	id string
}

func (t treeFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	clean, err := CleanPath(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	e, err := t.s.db.LookupTreeEntry(ctx, t.id, Fold(clean))
	if errors.Is(err, store.ErrNotFound) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	f, err := os.Open(t.s.blobPath(e.SHA256))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return &File{File: f, path: e.Path, sha: e.SHA256}, nil
}

// File is a file opened from a tree. Stat reports the tree's file name
// rather than the blob's.
type File struct {
	*os.File
	path string
	sha  string
}

// SHA256 is the file's hash (usable as an HTTP ETag).
func (f *File) SHA256() string { return f.sha }

// Path is the file's path in the tree, in its stored case.
func (f *File) Path() string { return f.path }

// Stat implements fs.File.
func (f *File) Stat() (fs.FileInfo, error) {
	fi, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return fileInfo{FileInfo: fi, name: path.Base(f.path)}, nil
}

type fileInfo struct {
	fs.FileInfo
	name string
}

func (fi fileInfo) Name() string { return fi.name }
