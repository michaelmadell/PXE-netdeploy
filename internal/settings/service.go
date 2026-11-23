package settings

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/boot"
	"github.com/michaelmadell/pxe-netdeploy/internal/content"
	"github.com/michaelmadell/pxe-netdeploy/internal/contentca"
	"github.com/michaelmadell/pxe-netdeploy/internal/dhcp"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/ipxe"
	"github.com/michaelmadell/pxe-netdeploy/internal/netsvc"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/tftp"
)

// Options configure a Service.
type Options struct {
	Store   *store.Store
	Network *netsvc.Manager
	Host    Host // nil: SystemHost
	// Booter decides what clients boot (ADR 0024). Nil: nobody is answered.
	Booter boot.Decider
	// AdminOverride, when valid, replaces Settings.admin's address (the
	// -admin-addr recovery flag).
	AdminOverride netip.AddrPort
	// GeneratedAdminCert is used when no custom admin certificate is set.
	GeneratedAdminCert *tls.Certificate
	// ContentCA issues the HTTPS content listeners' certificate (ADR 0028).
	// Nil: interfaces with services.https get no HTTPS listener.
	ContentCA *contentca.Authority
	// IPXE is the signed iPXE chain served to HTTP(S) Boot clients
	// (ADR 0032). Nil or empty: they get no boot answer.
	IPXE *ipxe.Bundle
	// Agent serves /api/v1/agent/ on content listeners (ADR 0009). Nil: 404.
	Agent http.Handler
	// LinuxContent opens a ready Linux image's content for
	// /content/linux/<id>/ (ADR 0034). Nil: 404.
	LinuxContent func(imageID string) (fs.FS, error)
	// LinuxSeed serves NoCloud seeds for /content/seed/ (ADR 0035). Nil: 404.
	LinuxSeed func(r *http.Request, token, file string) ([]byte, int, []any)
	// LinuxReport takes installer reports for /content/report/ (ADR 0036).
	LinuxReport func(r *http.Request, token, kind string) (int, bool, []any)
	// Events receives client sightings and lease changes from DHCP.
	Events *events.Bus
	// CheckBackup validates backup settings against things the settings
	// package can't see (secrets, the data and content directories). Nil:
	// no extra checks.
	CheckBackup func(ctx context.Context, b Backup) []FieldError
	Logger      *slog.Logger
}

// Service is the running configuration: it loads settings at startup,
// answers GET /settings, and applies PUT /settings atomically.
type Service struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex // serialises Replace; guards cur and version
	cur     Config
	version int64

	sessions *boot.Sessions

	contentMu sync.Mutex
	content   map[string]*content.Handler // per interface, reused across applies
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
	return &Service{opts: opts, log: opts.Logger.With("svc", "settings"), sessions: &boot.Sessions{}}
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
	// Start from the defaults so fields added since the document was stored
	// (e.g. storage, Phase 3) get their default values.
	cfg := Defaults()
	if err := json.Unmarshal(st.Doc, &cfg); err != nil {
		return fmt.Errorf("settings: stored document: %w", err)
	}
	s.cur, s.version = cfg, st.Version

	for _, fe := range Validate(cfg, s.opts.Host) {
		s.log.Warn("stored settings don't apply on this host", "field", fe.Field, "problem", fe.Message)
	}
	if err := s.ensureContentCA(cfg); err != nil {
		s.log.Error("HTTPS content listeners not started", "err", err)
	}
	plan := s.plan(cfg)
	failures, err := s.opts.Network.Start(ctx, plan)
	for _, f := range failures {
		s.log.Error("listener not started", "service", f.Service, "interface", f.Interface,
			"proto", f.Protocol, "port", f.Port, "err", f.Message, "owner", f.Owner)
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
	out.Admin.CertificateInfo = s.adminCertificate(cfg)
	s.contentCertificates(cfg, &out)
	return out, tag, err
}

// Replace validates in, binds what it needs, stores it and switches to it.
// Errors: ErrPreconditionFailed, *ValidationError, *netsvc.BindError.
func (s *Service) Replace(ctx context.Context, in api.Settings, ifMatch string) (api.Settings, string, error) {
	next, tag, err := s.replace(ctx, in, ifMatch)
	if err != nil {
		return api.Settings{}, "", err
	}
	// Outside s.mu: issuing the content certificate reads the settings.
	out, err := ToAPI(next, tag, s.guidance(next))
	out.Admin.CertificateInfo = s.adminCertificate(next)
	s.contentCertificates(next, &out)
	return out, tag, err
}

func (s *Service) replace(ctx context.Context, in api.Settings, ifMatch string) (Config, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !etagMatches(ifMatch, s.version) {
		return Config{}, "", ErrPreconditionFailed
	}
	next, ferrs := FromAPI(in, s.cur)
	ferrs = append(ferrs, Validate(next, s.opts.Host)...)
	if s.opts.CheckBackup != nil {
		ferrs = append(ferrs, s.opts.CheckBackup(ctx, next.Backup)...)
	}
	if id := next.BootPrompt.DefaultProfileID; id != "" && s.opts.Store != nil {
		if ok, err := s.opts.Store.ProfileExists(ctx, id); err != nil {
			return Config{}, "", err
		} else if !ok {
			ferrs = append(ferrs, FieldError{Field: "bootPrompt.defaultProfileId", Message: "no profile " + id})
		}
	}
	if len(ferrs) == 0 {
		if err := s.ensureContentCA(next); err != nil {
			ferrs = append(ferrs, FieldError{Field: "contentHttps", Message: err.Error()})
		}
	}
	if len(ferrs) > 0 {
		return Config{}, "", &ValidationError{Fields: dedupe(ferrs)}
	}
	doc, err := json.Marshal(next)
	if err != nil {
		return Config{}, "", fmt.Errorf("settings: encode: %w", err)
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
		return Config{}, "", err
	}
	s.cur, s.version = next, version
	s.log.Info("settings applied", "version", version)
	if !next.BootPrompt.RequirePassword {
		s.log.Warn("boot prompt password is DISABLED (debug mode): anyone at a network-booted machine can re-image it")
	}
	return next, etag(version), nil
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
		Root:          s.defaultRoot(),
		MaxBlockSize:  cfg.TFTP.MaxBlockSize,
		MaxWindowSize: cfg.TFTP.MaxWindowSize,
		Timeout:       time.Duration(cfg.TFTP.TimeoutSeconds) * time.Second,
		Logger:        s.opts.Logger,
	}
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

		if in.Services.HTTP {
			p.HTTP = append(p.HTTP, netsvc.HTTPPlan{
				Interface: in.Name, Bind: netip.AddrPortFrom(ip, uint16(in.Services.HTTPPort)),
				Handler: s.contentHandler(in.Name), Field: path + ".services.httpPort",
			})
		}
		if in.Services.HTTPS {
			if s.opts.ContentCA != nil && s.opts.ContentCA.CA() != nil {
				p.HTTP = append(p.HTTP, netsvc.HTTPPlan{
					Interface: in.Name, Bind: netip.AddrPortFrom(ip, uint16(in.Services.HTTPSPort)),
					Handler: s.contentHandler(in.Name), GetCertificate: s.opts.ContentCA.GetCertificate,
					Field: path + ".services.httpsPort",
				})
			} else {
				p.Notes["content"] = "HTTPS is on but there is no content CA (see the startup log)"
			}
		}
		if in.Services.TFTP {
			c := tftpCfg
			c.ClientRoot = s.clientRoot(in.Name)
			p.TFTP = append(p.TFTP, netsvc.TFTPPlan{
				Interface: in.Name, Bind: netip.AddrPortFrom(ip, 69), Config: c, Field: path + ".services.tftp",
			})
		}
		// Linux binds 0.0.0.0 per device (SO_BINDTODEVICE) so broadcasts
		// arrive; Windows binds the interface address, which receives them.
		bindIP := netip.IPv4Unspecified()
		if runtime.GOOS == "windows" {
			bindIP = ip
		}
		switch {
		case in.DHCP.Mode == DHCPFull && in.DHCP.Pool != nil:
			p.DHCP = append(p.DHCP, netsvc.DHCPPlan{
				Interface: in.Name, Bind: netip.AddrPortFrom(bindIP, 67),
				Config: s.dhcpConfig(cfg, in, primary), Field: path + ".dhcp",
			})
		case in.DHCP.Mode == DHCPProxy:
			ports := []uint16{dhcp.ProxyPort}
			if in.DHCP.ListenOn67 {
				ports = []uint16{67, dhcp.ProxyPort}
			}
			for _, port := range ports {
				p.DHCP = append(p.DHCP, netsvc.DHCPPlan{
					Interface: in.Name, Bind: netip.AddrPortFrom(bindIP, port),
					Config: s.dhcpConfig(cfg, in, primary), Field: path + ".dhcp",
				})
			}
		}
	}
	return p
}

func (s *Service) dhcpConfig(cfg Config, in Interface, primary netip.Prefix) dhcp.Config {
	b := primary.Addr().As4()
	mask := net.CIDRMask(primary.Bits(), 32)
	c := dhcp.Config{
		Mode:        dhcp.ModeFull,
		Interface:   in.Name,
		ServerIP:    b[:],
		Netmask:     mask,
		Archs:       &dhcp.Archs{BIOS: cfg.Architectures.BIOS, UEFIX64: cfg.Architectures.UEFIX64, UEFIArm64: cfg.Architectures.UEFIArm64, HTTPBoot: cfg.Architectures.HTTPBoot},
		MACPrefixes: in.MACPrefixes,
		Booter:      s.booter(),
		Store:       s.opts.Store,
		Sessions:    s.Sessions(),
		Events:      s.opts.Events,
		Logger:      s.opts.Logger,
	}
	c.HTTPBootBase, c.HTTPBootOff = s.httpBootBase(in, primary.Addr())
	c.HTTPBootArch = s.opts.IPXE.Has
	if in.DHCP.Mode == DHCPProxy {
		c.Mode = dhcp.ModeProxy
		return c
	}
	pool := in.DHCP.Pool
	m := pool.SubnetMask.As4()
	c.Netmask = m[:]
	c.PoolStart, c.PoolEnd = as4(pool.Start), as4(pool.End)
	c.LeaseTime = time.Duration(pool.LeaseSeconds) * time.Second
	c.PingCheck = pool.PingCheck
	c.DomainName = pool.DomainName
	if pool.Router.IsValid() {
		c.Router = as4(pool.Router)
	}
	for _, d := range pool.DNSServers {
		c.DNS = append(c.DNS, as4(d))
	}
	return c
}

// httpBootBase is where HTTP Boot clients on in fetch the iPXE chain
// (ADR 0032): its HTTPS content listener when it has one, else its HTTP
// one. "" and the reason when there's neither or no iPXE set.
func (s *Service) httpBootBase(in Interface, ip netip.Addr) (string, string) {
	if len(s.opts.IPXE.Archs()) == 0 {
		why := "no iPXE set"
		if s.opts.IPXE != nil && s.opts.IPXE.Reason != "" {
			why = s.opts.IPXE.Reason
		}
		return "", "HTTP Boot unavailable: " + why
	}
	host := func(port, def int) string {
		if port == def {
			return ip.String()
		}
		return netip.AddrPortFrom(ip, uint16(port)).String()
	}
	switch {
	case in.Services.HTTPS && s.opts.ContentCA != nil && s.opts.ContentCA.CA() != nil:
		return "https://" + host(in.Services.HTTPSPort, 443) + content.BootPrefix, ""
	case in.Services.HTTP:
		return "http://" + host(in.Services.HTTPPort, 80) + content.BootPrefix, ""
	}
	return "", "HTTP Boot unavailable: the interface serves no content over HTTP or HTTPS"
}

// contentHandler returns iface's content handler, creating it once and
// reusing it across settings changes.
func (s *Service) contentHandler(iface string) *content.Handler {
	s.contentMu.Lock()
	defer s.contentMu.Unlock()
	if s.content == nil {
		s.content = map[string]*content.Handler{}
	}
	h, ok := s.content[iface]
	if !ok {
		cfg := content.Config{
			DefaultRoot: s.defaultRoot(),
			ClientRoot:  s.clientRoot(iface),
			Script:      s.ipxeScript(iface),
			Linux:       s.opts.LinuxContent,
			Seed:        s.opts.LinuxSeed,
			Report:      s.opts.LinuxReport,
			Agent:       s.opts.Agent,
			Logger:      s.opts.Logger,
		}
		if s.opts.IPXE != nil {
			cfg.Shared, cfg.SharedDir = s.opts.IPXE, ipxe.Dir
		}
		h = content.New(cfg)
		s.content[iface] = h
	}
	return h
}

// booter decides what clients boot (ADR 0024).
func (s *Service) booter() boot.Decider {
	if s.opts.Booter == nil {
		return noBooter{}
	}
	return s.opts.Booter
}

type noBooter struct{}

func (noBooter) Boot(context.Context, boot.Request) (boot.Answer, bool, string) {
	return boot.Answer{}, false, "no boot decisions configured"
}

func (noBooter) Default(context.Context, boot.Arch) (boot.Answer, bool, string) {
	return boot.Answer{}, false, "no boot decisions configured"
}

// defaultRoot is the content for clients nothing tells apart (DHCP off, no
// session or lease): the default profile's boot image, looked up per file.
func (s *Service) defaultRoot() fs.FS { return defaultFS{s} }

type defaultFS struct{ s *Service }

func (d defaultFS) Open(name string) (fs.File, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ans, ok, _ := d.s.booter().Default(ctx, boot.ArchUEFIX64)
	if !ok || ans.Root == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return ans.Root.Open(name)
}

// clientRoot tells TFTP on iface which content serves a client IP (ADR
// 0006): the boot session DHCP recorded, else (full mode, after a restart)
// the client's lease, else the default content. With DHCP off nothing tells
// clients apart, so they all get the default.
func (s *Service) clientRoot(iface string) func(netip.Addr) (fs.FS, []any) {
	return func(ip netip.Addr) (fs.FS, []any) {
		if ans, attrs, ok := s.clientAnswer(iface, ip); ok {
			return ans.Root, attrs
		}
		return nil, []any{"boot_session", "none"} // the default content
	}
}

// clientAnswer is what a client IP was answered: its DHCP session, else
// (full mode, after a restart) the decision for its lease's MAC.
func (s *Service) clientAnswer(iface string, ip netip.Addr) (boot.Answer, []any, bool) {
	if sess, ok := s.sessions.Lookup(iface, ip); ok {
		return sess.Answer, append([]any{"mac", sess.MAC, "boot_session", "dhcp"}, sess.Answer.LogAttrs()...), true
	}
	if s.opts.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if l, err := s.opts.Store.LeaseByIP(ctx, iface, ip); err == nil {
			arch := boot.ArchUEFIX64
			if l.Arch != nil {
				arch = boot.Arch(*l.Arch)
			}
			if ans, ok, _ := s.booter().Boot(ctx, boot.Request{Interface: iface, MAC: l.MAC, Arch: arch}); ok {
				return ans, append([]any{"mac", l.MAC, "boot_session", "lease"}, ans.LogAttrs()...), true
			}
		}
	}
	return boot.Answer{}, nil, false
}

// ipxeScript generates an HTTP(S) Boot client's autoexec.ipxe (ADR 0034):
// the gate for a Linux profile; ok=false (the static ADR 0032 script) for a
// Windows one. The decision is the client's session or lease, else the
// default profile (a URL typed into the firmware, or DHCP off).
func (s *Service) ipxeScript(iface string) func(*http.Request, netip.Addr, string) ([]byte, []any, bool) {
	return func(r *http.Request, ip netip.Addr, arch string) ([]byte, []any, bool) {
		ans, attrs, ok := s.clientAnswer(iface, ip)
		if !ok {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			httpArch := boot.ArchUEFIX64HTTP
			if arch == "arm64" {
				httpArch = boot.ArchUEFIArm64HTTP
			}
			if ans, ok, _ = s.booter().Default(ctx, httpArch); !ok {
				return nil, nil, false
			}
			attrs = append([]any{"boot_session", "none"}, ans.LogAttrs()...)
		}
		if ans.Linux == nil {
			return nil, nil, false
		}
		cfg, _ := s.Current()
		bp := cfg.BootPrompt
		body := ipxe.LinuxGate(ipxe.GateOptions{
			Arch: arch, Armed: ans.Linux.Armed, ArmedCountdown: bp.ArmedCountdownSeconds, PromptCountdown: bp.TimeoutSeconds,
			NoPassword: !bp.RequirePassword, PasswordSet: bp.PasswordHash != "",
		})
		return []byte(body), append(attrs, "script", "linux gate"), true
	}
}

// Sessions is the boot session table shared by every DHCP listener (and
// TFTP, from Phase 2 step 4).
func (s *Service) Sessions() *boot.Sessions { return s.sessions }

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
		if cfg.Architectures.UEFIX64 {
			g = append(g, Guidance{Arch: api.ClientArchUefiX64, NextServer: server.String(), BootFile: boot.PXEBootFile, VendorClass: "PXEClient"})
		}
		if cfg.Architectures.UEFIArm64 {
			g = append(g, Guidance{Arch: api.ClientArchUefiArm64, NextServer: server.String(), BootFile: boot.PXEBootFile, VendorClass: "PXEClient"})
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
