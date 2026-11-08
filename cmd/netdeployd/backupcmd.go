package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/backup"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/datalock"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

func isUNC(p string) bool { return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") }

// openTarget opens a local path or UNC path, asking for the share password.
func openTarget(ctx context.Context, p, user, passFile string) (backup.Dest, error) {
	if !isUNC(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		return backup.OpenDest(ctx, backup.TypeLocal, abs, nil)
	}
	if user == "" {
		return nil, errors.New("a UNC path needs -user (DOMAIN\\user or user@domain)")
	}
	fmt.Fprintf(os.Stderr, "Password for %s on %s\n", user, p)
	pass, err := passphrase(passFile, false)
	if err != nil {
		return nil, err
	}
	return backup.OpenDest(ctx, backup.TypeSMB, p, &backup.Credential{Username: user, Password: string(pass)})
}

func contentDirOf(data, content string) string {
	if content != "" {
		return content
	}
	return filepath.Join(data, "content")
}

// backupCommand: netdeployd backup -to <path|UNC> (server stopped).
func backupCommand(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	data := fs.String("data", "./data", "data directory")
	content := fs.String("content", "", "content directory (default <data>/content)")
	to := fs.String("to", "", `destination folder or UNC path (\\server\share\folder)`)
	user := fs.String("user", "", "share user for a UNC destination")
	passFile := fs.String("password-file", "", "read the share password from this file instead of asking")
	retention := fs.Int("retention", 0, "snapshots to keep at the destination; 0 keeps all")
	fs.Parse(args)
	if *to == "" {
		return errors.New("backup: -to is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	lock, err := datalock.Acquire(*data)
	if err != nil {
		return fmt.Errorf("%w (with the server running, use POST /api/v1/system/backups instead)", err)
	}
	defer lock.Release()
	dest, err := openTarget(ctx, *to, *user, *passFile)
	if err != nil {
		return err
	}
	defer dest.Close()
	st, err := store.Open(ctx, *data, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cs, err := cas.Open(ctx, cas.Options{Root: contentDirOf(*data, *content), Store: st, Logger: quiet})
	if err != nil {
		return err
	}
	fp := ""
	if r, err := secrets.Load(*data); err == nil {
		fp = r.Fingerprint()
	}
	last := -1
	res, err := backup.Run(ctx, backup.Source{Store: st, Content: cs, DataDir: *data, ServerVersion: version, KeyFingerprint: fp}, dest,
		backup.Options{Trigger: backup.TriggerOffline, Retention: *retention, Logger: quiet, Progress: func(p int, msg string) {
			if p/10 != last/10 {
				fmt.Printf("%3d%%  %s\n", p, msg)
				last = p
			}
		}})
	if err != nil {
		return err
	}
	fmt.Printf("Backup %s written to %s: %d files (%d copied, %s), %d old snapshots removed.\n",
		res.Manifest.ID, dest, res.Manifest.BlobCount, res.CopiedBlobs, humanSize(res.CopiedBytes), len(res.PrunedSnapshots))
	return nil
}

// restoreCommand: netdeployd restore -from <backup folder|snapshot> (server stopped).
func restoreCommand(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	data := fs.String("data", "./data", "data directory to restore into")
	content := fs.String("content", "", "content directory (default <data>/content)")
	from := fs.String("from", "", `backup folder or UNC path; or a snapshot inside it (...\snapshots\<id>)`)
	snapshot := fs.String("snapshot", "", "snapshot id (default: the newest)")
	user := fs.String("user", "", "share user for a UNC source")
	passFile := fs.String("password-file", "", "read the share password from this file instead of asking")
	force := fs.Bool("force", false, "replace an existing database (it is moved aside, not deleted)")
	keyFile := fs.String("key", "", "secrets key export (netdeployd key export) to install")
	keyPassFile := fs.String("key-passphrase-file", "", "read the key export's passphrase from this file instead of asking")
	fs.Parse(args)
	if *from == "" {
		return errors.New("restore: -from is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// A path ending in snapshots/<id> names the snapshot.
	base, snap := *from, *snapshot
	norm := strings.TrimRight(strings.ReplaceAll(*from, `\`, "/"), "/")
	if dir, id := path.Split(norm); path.Base(strings.TrimRight(dir, "/")) == backup.SnapshotsDir && snap == "" {
		snap = id
		base = path.Dir(strings.TrimRight(dir, "/"))
		if isUNC(*from) {
			base = "//" + strings.TrimLeft(base, "/")
		} else {
			base = filepath.FromSlash(base)
		}
	}

	lock, err := datalock.Acquire(*data)
	if err != nil {
		return err
	}
	defer lock.Release()
	src, err := openTarget(ctx, base, *user, *passFile)
	if err != nil {
		return err
	}
	defer src.Close()
	cdir := contentDirOf(*data, *content)
	m, err := backup.Restore(ctx, src, backup.RestoreOptions{DataDir: *data, ContentDir: cdir, Snapshot: snap, Force: *force,
		Log: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }})
	if err != nil {
		return err
	}

	// Bring the schema up to date and drop uploads (their files aren't in
	// backups).
	st, err := store.Open(ctx, *data, store.Options{})
	if err != nil {
		return err
	}
	if err := st.ClearUploads(ctx); err != nil {
		st.Close()
		return err
	}
	n, _ := st.CountSecrets(ctx)
	st.Close()

	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			return err
		}
		pass, err := passphrase(*keyPassFile, false)
		if err != nil {
			return err
		}
		r, err := secrets.Import(b, pass)
		if err != nil {
			return err
		}
		if m.SecretsKeyFingerprint != "" && r.Fingerprint() != m.SecretsKeyFingerprint {
			fmt.Printf("Note: this key (%s) isn't the one the backup was made with (%s).\n", r.Fingerprint()[:16], m.SecretsKeyFingerprint[:16])
		}
		if err := installKey(ctx, *data, r, true); err != nil {
			return err
		}
	} else if n > 0 {
		cur, err := secrets.Load(*data)
		switch {
		case err == nil && cur.Fingerprint() == m.SecretsKeyFingerprint:
			fmt.Printf("%d secrets: readable with the key already in %s.\n", n, *data)
		default:
			fmt.Printf("%d secrets can't be read without the key they were sealed with (%s).\n"+
				"Run: netdeployd key import -data %s -in <export file>, or re-enter them.\n", n, short(m.SecretsKeyFingerprint), *data)
		}
	}
	fmt.Println("Restore complete. Start the server to use it.")
	return nil
}

func short(fp string) string {
	if len(fp) > 16 {
		return fp[:16]
	}
	return fp
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
