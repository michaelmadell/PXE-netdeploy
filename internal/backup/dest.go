package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudsoda/go-smb2"
)

// Destination types.
const (
	TypeLocal = "local"
	TypeSMB   = "smb"
)

// Entry is a directory entry at a destination.
type Entry struct {
	Name string
	Dir  bool
	Size int64
}

// Dest is where backups are written: a folder, local or on a share. Paths
// are '/'-separated and relative to the destination's base. Missing paths
// give errors matching fs.ErrNotExist.
type Dest interface {
	String() string
	MkdirAll(p string) error
	ReadDir(p string) ([]Entry, error)
	Create(p string) (io.WriteCloser, error) // truncates
	Open(p string) (io.ReadCloser, error)
	Rename(from, to string) error // replaces an existing file
	Remove(p string) error
	RemoveAll(p string) error
	Close() error
}

// Credential logs on to a share. Username is `user`, `DOMAIN\user` or
// `user@domain`.
type Credential struct{ Username, Password string }

// OpenDest opens a destination. path is absolute for local, UNC for smb.
func OpenDest(ctx context.Context, typ, p string, cred *Credential) (Dest, error) {
	switch typ {
	case TypeLocal:
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("backup: %q is not an absolute path", p)
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, fmt.Errorf("backup: destination %s: %w", p, err)
		}
		return localDest(p), nil
	case TypeSMB:
		if cred == nil {
			return nil, errors.New("backup: an SMB destination needs a credential")
		}
		return openSMB(ctx, p, *cred)
	}
	return nil, fmt.Errorf("backup: unknown destination type %q", typ)
}

// ---------------------------------------------------------------- local

type localDest string

func (d localDest) String() string           { return string(d) }
func (d localDest) abs(p string) string      { return filepath.Join(string(d), filepath.FromSlash(p)) }
func (d localDest) MkdirAll(p string) error  { return os.MkdirAll(d.abs(p), 0o700) }
func (d localDest) Remove(p string) error    { return os.Remove(d.abs(p)) }
func (d localDest) RemoveAll(p string) error { return os.RemoveAll(d.abs(p)) }
func (d localDest) Close() error             { return nil }
func (d localDest) Rename(from, to string) error {
	return os.Rename(d.abs(from), d.abs(to))
}
func (d localDest) Create(p string) (io.WriteCloser, error) {
	return os.OpenFile(d.abs(p), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
}
func (d localDest) Open(p string) (io.ReadCloser, error) { return os.Open(d.abs(p)) }
func (d localDest) ReadDir(p string) ([]Entry, error) {
	es, err := os.ReadDir(d.abs(p))
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(es))
	for _, e := range es {
		var size int64
		if !e.IsDir() {
			if fi, err := e.Info(); err == nil {
				size = fi.Size()
			}
		}
		out = append(out, Entry{Name: e.Name(), Dir: e.IsDir(), Size: size})
	}
	return out, nil
}

// ---------------------------------------------------------------- SMB

// UNC is a parsed \\server\share\dir path.
type UNC struct{ Server, Share, Dir string }

// ParseUNC accepts \\server\share[\dir...] or //server/share[/dir...].
func ParseUNC(p string) (UNC, error) {
	s := strings.ReplaceAll(p, `\`, "/")
	if !strings.HasPrefix(s, "//") {
		return UNC{}, fmt.Errorf(`%q is not a UNC path (\\server\share\folder)`, p)
	}
	parts := strings.Split(strings.Trim(s[2:], "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return UNC{}, fmt.Errorf(`%q needs a server and a share (\\server\share\folder)`, p)
	}
	for _, x := range parts {
		if x == "" || x == "." || x == ".." {
			return UNC{}, fmt.Errorf("%q has an empty or relative component", p)
		}
	}
	return UNC{Server: parts[0], Share: parts[1], Dir: strings.Join(parts[2:], "/")}, nil
}

func (u UNC) String() string {
	s := `\\` + u.Server + `\` + u.Share
	if u.Dir != "" {
		s += `\` + strings.ReplaceAll(u.Dir, "/", `\`)
	}
	return s
}

type smbDest struct {
	unc     UNC
	session *smb2.Session
	share   *smb2.Share
}

func openSMB(ctx context.Context, p string, cred Credential) (Dest, error) {
	u, err := ParseUNC(p)
	if err != nil {
		return nil, err
	}
	ini := &smb2.NTLMInitiator{User: cred.Username, Password: cred.Password}
	if dom, user, ok := strings.Cut(cred.Username, `\`); ok {
		ini.Domain, ini.User = dom, user
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	host := u.Server
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "445")
	}
	sess, err := (&smb2.Dialer{Initiator: ini}).Dial(dctx, host)
	if err != nil {
		return nil, fmt.Errorf("backup: connect to %s as %s: %w", u.Server, cred.Username, err)
	}
	sh, err := sess.Mount(u.Share)
	if err != nil {
		sess.Logoff()
		return nil, fmt.Errorf("backup: open share %s: %w", u, err)
	}
	d := &smbDest{unc: u, session: sess, share: sh}
	if u.Dir != "" {
		if err := sh.MkdirAll(u.Dir, 0o755); err != nil {
			d.Close()
			return nil, fmt.Errorf("backup: create %s: %w", u, err)
		}
	}
	return d, nil
}

func (d *smbDest) String() string { return d.unc.String() }
func (d *smbDest) abs(p string) string {
	return strings.Trim(path.Join(d.unc.Dir, p), "/")
}
func (d *smbDest) MkdirAll(p string) error { return d.share.MkdirAll(d.abs(p), 0o755) }
func (d *smbDest) Remove(p string) error   { return d.share.Remove(d.abs(p)) }
func (d *smbDest) RemoveAll(p string) error {
	err := d.share.RemoveAll(d.abs(p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
func (d *smbDest) Rename(from, to string) error { return d.share.Rename(d.abs(from), d.abs(to)) }
func (d *smbDest) Create(p string) (io.WriteCloser, error) {
	return d.share.Create(d.abs(p))
}
func (d *smbDest) Open(p string) (io.ReadCloser, error) { return d.share.Open(d.abs(p)) }
func (d *smbDest) ReadDir(p string) ([]Entry, error) {
	fis, err := d.share.ReadDir(d.abs(p))
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(fis))
	for _, fi := range fis {
		out = append(out, Entry{Name: fi.Name(), Dir: fi.IsDir(), Size: fi.Size()})
	}
	return out, nil
}
func (d *smbDest) Close() error {
	d.share.Umount()
	return d.session.Logoff()
}
