package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"regexp"

	"github.com/michaelmadell/pxe-netdeploy/internal/datalock"
	"github.com/michaelmadell/pxe-netdeploy/internal/server"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// minPassword matches the spec's minLength for account passwords (ADR 0029).
const minPassword = 8

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const adminUsage = `usage:
  netdeployd admin reset-password -data <dir> -user <name>
  netdeployd admin set-address -data <dir> <ip:port>    (e.g. 0.0.0.0:8443 for all networks)`

// adminCommand: netdeployd admin reset-password (ADR 0029) and set-address
// (ADR 0031). Offline: the server must be stopped.
func adminCommand(args []string) error {
	if len(args) == 0 {
		return errors.New(adminUsage)
	}
	switch args[0] {
	case "reset-password":
		return resetPassword(args[1:])
	case "set-address":
		return setAddress(args[1:])
	}
	return errors.New(adminUsage)
}

// setAddress stores where the admin web UI and API listen.
func setAddress(args []string) error {
	fs := flag.NewFlagSet("admin set-address", flag.ContinueOnError)
	dataDir := fs.String("data", "./data", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(adminUsage)
	}
	addr, err := netip.ParseAddrPort(fs.Arg(0))
	if err != nil || !addr.Addr().Is4() || addr.Port() == 0 {
		return fmt.Errorf("admin set-address: %q: want an IPv4 address and port, e.g. 127.0.0.1:8443 or 0.0.0.0:8443", fs.Arg(0))
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
	if err := settings.SetAdminAddress(ctx, st, addr); err != nil {
		return err
	}
	fmt.Printf("Admin address set to %s; it applies when the server starts.\n", addr)
	return nil
}

func resetPassword(args []string) error {
	fs := flag.NewFlagSet("admin reset-password", flag.ContinueOnError)
	dataDir := fs.String("data", "./data", "data directory")
	user := fs.String("user", "", "account to reset; created if it doesn't exist")
	passFile := fs.String("password-file", "", "read the new password from this file instead of asking")
	if err := fs.Parse(args); err != nil {
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
