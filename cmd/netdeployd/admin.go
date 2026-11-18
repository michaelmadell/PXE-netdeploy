package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"regexp"

	"github.com/michaelmadell/pxe-netdeploy/internal/datalock"
	"github.com/michaelmadell/pxe-netdeploy/internal/server"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// minPassword matches the spec's minLength for account passwords (ADR 0029).
const minPassword = 8

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// adminCommand: netdeployd admin reset-password -user <name> (ADR 0029).
// Offline: the server must be stopped.
func adminCommand(args []string) error {
	if len(args) == 0 || args[0] != "reset-password" {
		return errors.New("usage: netdeployd admin reset-password -data <dir> -user <name>")
	}
	fs := flag.NewFlagSet("admin reset-password", flag.ContinueOnError)
	dataDir := fs.String("data", "./data", "data directory")
	user := fs.String("user", "", "account to reset; created if it doesn't exist")
	passFile := fs.String("password-file", "", "read the new password from this file instead of asking")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !usernameRE.MatchString(*user) {
		return errors.New("admin reset-password: -user must be 1 to 64 letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	lock, err := datalock.Acquire(*dataDir)
	if err != nil {
		return fmt.Errorf("%w (stop the server first)", err)
	}
	defer lock.Release()
	ctx := context.Background()
	st, err := store.Open(ctx, *dataDir, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()

	fmt.Printf("New password for %s (at least %d characters).\n", *user, minPassword)
	pw, err := password(*passFile)
	if err != nil {
		return err
	}
	hash, err := settings.HashPassword(string(pw))
	if err != nil {
		return err
	}
	u, err := st.UserByName(ctx, *user)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if u, err = st.CreateUser(ctx, *user, hash); err != nil {
			return err
		}
		server.RemoveSetupCode(*dataDir)
		fmt.Printf("Created account %s.\n", u.Username)
	case err != nil:
		return err
	default:
		if err := st.SetPassword(ctx, u.ID, hash, ""); err != nil {
			return err
		}
		fmt.Printf("Password set for %s; its sessions have ended.\n", u.Username)
	}
	return nil
}

// password reads it from a file, or asks twice.
func password(file string) ([]byte, error) {
	p, err := askSecret(file, "Password", file == "", minPassword)
	if err != nil {
		return nil, err
	}
	if n := len([]rune(string(p))); n < minPassword || n > 256 {
		return nil, fmt.Errorf("the password must be %d to 256 characters", minPassword)
	}
	return p, nil
}
