package boot

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// ISOName is where a Linux image's kept ISO is served in its content
// (ADR 0034): /content/linux/<id>/image.iso.
const ISOName = "image.iso"

// LinuxContent opens a ready Linux image's content: its tree, and its kept
// ISO as ISOName. fs.ErrNotExist when there's no such image.
func (p *Profiles) LinuxContent(id string) (fs.FS, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	im, err := p.Store.Image(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fs.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	if im.Status != store.ImageReady || im.Kind == nil || *im.Kind != "linux" || im.TreeID == nil || p.Tree == nil {
		return nil, fs.ErrNotExist
	}
	return linuxFS{tree: p.Tree(*im.TreeID), iso: im.ISOBlob, blobPath: p.BlobPath}, nil
}

type linuxFS struct {
	tree     fs.FS
	iso      *string
	blobPath func(sha string) (string, error)
}

func (l linuxFS) Open(name string) (fs.File, error) {
	if strings.EqualFold(name, ISOName) {
		if l.iso == nil || l.blobPath == nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		p, err := l.blobPath(*l.iso)
		if err != nil {
			return nil, fmt.Errorf("image ISO: %w", err)
		}
		return os.Open(p)
	}
	return l.tree.Open(name)
}
