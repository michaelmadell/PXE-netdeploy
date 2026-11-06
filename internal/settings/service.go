package settings

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/dhcp"
	"github.com/michaelmadell/pxe-netdeploy/internal/netsvc"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/tftp"
)

// Dev holds temporary command-line options for boot content, until boot
// images provide it (Phase 2 step 4).
type Dev struct {
	TFTPRoot    string // served read-only on every interface with services.tftp
	BootFile    string // DHCP boot file for UEFI x64 PXE clients
	HTTPBootURL string // DHCP boot URL for UEFI x64 HTTP Boot clients
}

// Options configure a Service.
type Options struct {
	Store   *store.Store
	Network *netsvc.Manager
	Host    Host // nil: SystemHost
	Dev     Dev
	// AdminOverride, when valid, replaces Settings.admin's address (the
	// -admin-addr recovery flag).
	AdminOverride netip.AddrPort
	// GeneratedAdminCert is used when no custom admin certificate is set.
	GeneratedAdminCert *tls.Certificate
	Logger             *slog.Logger
}

// Service is the running configuration: it loads settings at startup,
// answers GET /settings, and applies PUT /settings atomically.
type Service struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex // serialises Replace; guards cur and version
	cur     Config
	version int64
}

// Errors from Replace.
var (
	// ErrPreconditionFailed: If-Match doesn't match the current ETag.
	ErrPreconditionFailed = errors.New("settings: etag does not match")
)

// ValidationError lists every field that can't be applied (422).
type ValidationError struct{ Fields []FieldError }

func (e *ValidationError) Error() string {
	msgs := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		msgs[i] = f.String()
	}
	return "settings: " + strings.Join(msgs, "; ")
}

// NewService returns a Service; call Start before anything else.
func NewService(opts Options) *Service {
	if opts.Host == nil {
		opts.Host = SystemHost{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Service{opts: opts, log: opts.Logger.With("svc", "settings")}
}

// Start loads the stored settings (storing the defaults on first run) and
// starts the listeners. Settings that no longer validate (an interface was
// removed, a port is now taken) don't stop the server: the affected
// listeners are left off and reported in health, so they can be fixed from
// the admin UI. Only an admin listener failure is returned.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.opts.Store.Settings(ctx); errors.Is(err, store.ErrNotFound) {
		doc, err := json.Marshal(Defaults())
		if err != nil {
			return err
		}
		if _, err := s.opts.Store.InitSettings(ctx, doc); err != nil {
			return err
		}
		s.log.Info("stored default settings (first run)")
	} else if err != nil {
		return err
	}
	st, err := s.opts.Store.Settings(ctx)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(st.Doc, &cfg); err != nil {
		return fmt.Errorf("settings: stored document: %w", err)
	}
	s.cur, s.version = cfg, st.Version

	for _, fe := range Validate(cfg, s.opts.Host) {
		s.log.Warn("stored settings don't apply on this host", "field", fe.Field, "problem", fe.Message)
	}
	plan := s.plan(cfg)
	failures, err := s.opts.Network.Start(ctx, plan)
	for _, f := range failures {
		s.log.Error("listener not started", "service", f.Service, "interface", f.Interface,
			"proto", f.Protocol, "port", f.Port, "err", f.Message)
	}
	if err != nil && s.opts.AdminOverride.IsValid() {
		return err
	}
	if err != nil {
		return fmt.Errorf("%w (start with -admin-addr to override the stored admin address)", err)
	}
	if !cfg.BootPrompt.RequirePassword {
		s.log.Warn("boot prompt password is DISABLED (debug mode): anyone at a network-booted machine can re-image it")
	}
	return nil
}

// Current returns the running configuration and its ETag.
func (s *Service) Current() (Config, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, etag(s.version)
}

// AdminOverridden reports whether -admin-addr replaced the stored address.
func (s *Service) AdminOverridden() bool { return s.opts.AdminOverride.IsValid() }

// Get renders the running configuration for GET /settings.
func (s *Service) Get() (api.Settings, string, error) {
	cfg, tag := s.Current()
	out, err := ToAPI(cfg, tag, s.guidance(cfg))
	return out, tag, err
}

// Replace validates in, binds what it needs, stores it and switches to it.
// Errors: ErrPreconditionFailed, *ValidationError, *netsvc.BindError.
func (s *Service) Replace(ctx context.Context, in api.Settings, ifMatch string) (api.Settings, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !etagMatches(ifMatch, s.version) {
		return api.Settings{}, "", ErrPreconditionFailed
	}
	next, ferrs := FromAPI(in, s.cur)
	ferrs = append(ferrs, Validate(next, s.opts.Host)...)
	if len(ferrs) > 0 {
		return api.Settings{}, "", &ValidationError{Fields: dedupe(ferrs)}
	}
	doc, err := json.Marshal(next)
	if err != nil {
		return api.Settings{}, "", fmt.Errorf("settings: encode: %w", err)
	}
	plan := s.plan(next)
	var version int64
	err = s.opts.Network.Apply(plan, func() error {
		v, err := s.opts.Store.ReplaceSettings(ctx, doc, s.version)
		if errors.Is(err, store.ErrVersionMismatch) {
			return ErrPreconditionFailed // only this Service writes settings; defensive
		}
		version = v
		return err
	})
	if err != nil {
		return api.Settings{}, "", err
	}
	s.cur, s.version = next, version
	s.log.Info("settings applied", "version", version)
	if !next.BootPrompt.RequirePassword {
		s.log.Warn("boot prompt password is DISABLED (debug mode): anyone at a network-booted machine can re-image it")
	}
	tag := etag(version)
	out, err := ToAPI(next, tag, s.guidance(next))
	return out, tag, err
}

// plan turns cfg into listeners. Interfaces that don't resolve on this host
// are skipped (Validate reports why).
func (s *Service) plan(cfg Config) netsvc.Plan {
	p := netsvc.Plan{Notes: map[string]string{}}

	adminAddr := netip.AddrPortFrom(cfg.Admin.BindAddress, uint16(cfg.Admin.Port))
	if s.opts.AdminOverride.IsValid() {
		adminAddr = s.opts.AdminOverride
	}
	p.Admin = netsvc.AdminPlan{Addr: adminAddr, Cert: s.opts.GeneratedAdminCert, Field: "admin"}
	if c := cfg.Admin.Certificate; c.Custom() {
		if pair, err := tls.X509KeyPair([]byte(c.CertificatePEM), []byte(c.PrivateKeyPEM)); err == nil {
			p.Admin.Cert = &pair
		} else {
			s.log.Error("stored admin certificate is invalid; using the generated one", "err", err)
		}
	}

	tftpCfg := tftp.Config{
		Root:          s.opts.Dev.TFTPRoot,
		MaxBlockSize:  cfg.TFTP.MaxBlockSize,
		MaxWindowSize: cfg.TFTP.MaxWindowSize,
		Timeout:       time.Duration(cfg.TFTP.TimeoutSeconds) * time.Second,
		Logger:        s.opts.Logger,
	}
	wantTFTP := false
	for i, in := range cfg.Interfaces {
		hi, err := s.opts.Host.Interface(in.Name)
		if err != nil {
			continue
		}
		primary, ok := hi.Primary()
		if !ok {
			continue
		}
		ip := primary.Addr()
		path := fmt.Sprintf("interfaces[%d]", i)

		if in.Services.TFTP {
			wantTFTP = true
			if s.opts.Dev.TFTPRoot != "" {
				p.TFTP = append(p.TFTP, netsvc.TFTPPlan{
					Interface: in.Name, Bind: netip.AddrPortFrom(ip, 69), Config: tftpCfg, Field: path + ".services.tftp",
				})
			}
		}
		if in.DHCP.Mode == DHCPFull && in.DHCP.Pool != nil {
			bind := netip.AddrPortFrom(netip.IPv4Unspecified(), 67)
			if runtime.GOOS == "windows" {
				bind = netip.AddrPortFrom(ip, 67)
			}
			p.DHCP = append(p.DHCP, netsvc.DHCPPlan{
				Interface: in.Name, Bind: bind, Config: s.dhcpConfig(in, primary), Field: path + ".dhcp",
			})
		}
	}
	if wantTFTP && s.opts.Dev.TFTPRoot == "" {
		p.Notes["tftp"] = "enabled on interfaces but no boot content yet: start with -tftp-root until boot images arrive (Phase 2 step 4)"
	}
	return p
}

func (s *Service) dhcpConfig(in Interface, primary netip.Prefix) dhcp.Config {
	pool := in.DHCP.Pool
	b := primary.Addr().As4()
	m := pool.SubnetMask.As4()
	c := dhcp.Config{
		Interface:    in.Name,
		ServerIP:     b[:],
		Netmask:      m[:],
		PoolStart:    as4(pool.Start),
		PoolEnd:      as4(pool.End),
		LeaseTime:    time.Duration(pool.LeaseSeconds) * time.Second,
		BootFileUEFI: s.opts.Dev.BootFile,
		HTTPBootURL:  s.opts.Dev.HTTPBootURL,
		Logger:       s.opts.Logger,
	}
	if pool.Router.IsValid() {
		c.Router = as4(pool.Router)
	}
	for _, d := range pool.DNSServers {
		c.DNS = append(c.DNS, as4(d))
	}
	return c
}

func as4(a netip.Addr) []byte {
	b := a.As4()
	return b[:]
}

// guidance computes externalDhcpGuidance for interfaces in `off` mode.
func (s *Service) guidance(cfg Config) map[string][]Guidance {
	out := map[string][]Guidance{}
	for _, in := range cfg.Interfaces {
		if in.DHCP.Mode != DHCPOff {
			continue
		}
		server := in.AdvertisedAddress
		if !server.IsValid() {
			hi, err := s.opts.Host.Interface(in.Name)
			if err != nil {
				continue
			}
			p, ok := hi.Primary()
			if !ok {
				continue
			}
			server = p.Addr()
		}
		var g []Guidance
		if cfg.Architectures.UEFIX64 && s.opts.Dev.BootFile != "" {
			g = append(g, Guidance{Arch: api.ClientArchUefiX64, NextServer: server.String(), BootFile: s.opts.Dev.BootFile, VendorClass: "PXEClient"})
		}
		if cfg.Architectures.HTTPBoot && s.opts.Dev.HTTPBootURL != "" {
			g = append(g, Guidance{Arch: api.ClientArchUefiX64Http, NextServer: server.String(), BootFile: s.opts.Dev.HTTPBootURL, VendorClass: "HTTPClient"})
		}
		out[in.Name] = g
	}
	return out
}

func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// etagMatches implements If-Match for a single strong ETag ("*" matches any).
func etagMatches(header string, version int64) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || t == etag(version) {
			return true
		}
	}
	return false
}

func dedupe(in []FieldError) []FieldError {
	seen := map[FieldError]bool{}
	out := in[:0]
	for _, f := range in {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
