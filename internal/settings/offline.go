package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// SetAdminAddress stores the admin listener's address with the server
// stopped (netdeployd admin set-address; installers use it, ADR 0031). On a
// data directory the server has never started, it stores the defaults with
// this address.
func SetAdminAddress(ctx context.Context, st *store.Store, addr netip.AddrPort) error {
	if !addr.Addr().Is4() || addr.Port() == 0 {
		return errors.New("admin address must be an IPv4 address and a port, e.g. 0.0.0.0:8443")
	}
	cur, err := st.Settings(ctx)
	if errors.Is(err, store.ErrNotFound) {
		cfg := Defaults()
		cfg.Admin = Admin{BindAddress: addr.Addr(), Port: int(addr.Port())}
		doc, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		_, err = st.InitSettings(ctx, doc)
		return err
	}
	if err != nil {
		return err
	}
	cfg := Defaults()
	if err := json.Unmarshal(cur.Doc, &cfg); err != nil {
		return fmt.Errorf("settings: stored document: %w", err)
	}
	cfg.Admin.BindAddress, cfg.Admin.Port = addr.Addr(), int(addr.Port())
	doc, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = st.ReplaceSettings(ctx, doc, cur.Version)
	return err
}
