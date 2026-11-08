package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/michaelmadell/pxe-netdeploy/internal/datalock"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
	"github.com/michaelmadell/pxe-netdeploy/internal/server"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// keyCommand: netdeployd key export|import (ADR 0021).
func keyCommand(args []string) error {
	if len(args) == 0 || (args[0] != "export" && args[0] != "import") {
		return errors.New("usage: netdeployd key export -data DIR -out FILE | netdeployd key import -data DIR -in FILE [-force]")
	}
	fs := flag.NewFlagSet("key "+args[0], flag.ExitOnError)
	dataDir := fs.String("data", "./data", "data directory")
	file := fs.String("out", "", "export: file to write (must not exist)")
	in := fs.String("in", "", "import: export file to read")
	force := fs.Bool("force", false, "import: replace a different key even though secrets are sealed under it")
	passFile := fs.String("passphrase-file", "", "read the passphrase from this file instead of asking (automation)")
	fs.Parse(args[1:])
	ctx := context.Background()

	if args[0] == "export" {
		if *file == "" {
			return errors.New("key export: -out is required")
		}
		r, err := secrets.Load(*dataDir)
		if errors.Is(err, secrets.ErrNoKey) {
			return errors.New("key export: there is no secrets key yet (no secret has been stored)")
		}
		if err != nil {
			return err
		}
		pass, err := passphrase(*passFile, true)
		if err != nil {
			return err
		}
		b, err := r.Export(pass)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("key export: %w", err)
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		// Record it (clears the secrets_key_not_exported warning). The
		// database can be shared with a running server (WAL).
		st, err := store.Open(ctx, *dataDir, store.Options{})
		if err != nil {
			return err
		}
		defer st.Close()
		if err := st.SetState(ctx, server.StateKeyExport, server.KeyExport{Fingerprint: r.Fingerprint(), At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
			return err
		}
		fmt.Printf("Exported the secrets key (fingerprint %s) to %s.\nKeep it and its passphrase somewhere other than this server.\n", r.Fingerprint()[:16], *file)
		return nil
	}

	// import
	if *in == "" {
		return errors.New("key import: -in is required")
	}
	lock, err := datalock.Acquire(*dataDir)
	if err != nil {
		return err
	}
	defer lock.Release()
	b, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	pass, err := passphrase(*passFile, false)
	if err != nil {
		return err
	}
	r, err := secrets.Import(b, pass)
	if err != nil {
		return err
	}
	return installKey(ctx, *dataDir, r, *force)
}

// installKey saves an imported key, refusing to replace a different key that
// still seals secrets unless forced. Shared with restore.
func installKey(ctx context.Context, dataDir string, r *secrets.Ring, force bool) error {
	st, err := store.Open(ctx, dataDir, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if cur, err := secrets.Load(dataDir); err == nil && cur.Fingerprint() != r.Fingerprint() && !force {
		if n := countSealedBy(ctx, st, cur.Fingerprint()); n > 0 {
			return fmt.Errorf("key import: %d secrets are sealed under the current key (%s); -force replaces it and makes them unreadable", n, cur.Fingerprint()[:16])
		}
	}
	if err := r.Save(dataDir, true); err != nil {
		return err
	}
	// Whoever imports a key holds an export of it.
	if err := st.SetState(ctx, server.StateKeyExport, server.KeyExport{Fingerprint: r.Fingerprint(), At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	ok, bad := 0, 0
	vault := secrets.NewVault(st, secrets.NewKeys(dataDir))
	list, _, _ := st.ListSecrets(ctx, "", 1000, "")
	for _, x := range list {
		if _, _, err := vault.Open(ctx, x.ID); err != nil {
			bad++
			fmt.Printf("  can't decrypt %q (%s): re-enter it\n", x.Name, x.Kind)
		} else {
			ok++
		}
	}
	fmt.Printf("Installed the secrets key (fingerprint %s): %d secrets readable, %d not.\n", r.Fingerprint()[:16], ok, bad)
	return nil
}

func countSealedBy(ctx context.Context, st *store.Store, fp string) int {
	n := 0
	list, _, _ := st.ListSecrets(ctx, "", 1000, "")
	for _, x := range list {
		if x.KeyFingerprint == fp {
			n++
		}
	}
	return n
}

// passphrase reads it from a file, or asks (twice when confirming).
func passphrase(file string, confirm bool) ([]byte, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		return []byte(strings.TrimRight(string(b), "\r\n")), err
	}
	ask := func(prompt string) ([]byte, error) {
		fmt.Fprint(os.Stderr, prompt)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return b, err
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return []byte(strings.TrimRight(line, "\r\n")), err
	}
	p, err := ask("Passphrase: ")
	if err != nil {
		return nil, err
	}
	if confirm {
		if len([]rune(string(p))) < secrets.MinPassphrase {
			return nil, fmt.Errorf("the passphrase must be at least %d characters", secrets.MinPassphrase)
		}
		p2, err := ask("Again: ")
		if err != nil {
			return nil, err
		}
		if string(p) != string(p2) {
			return nil, errors.New("the passphrases don't match")
		}
	}
	return p, nil
}
