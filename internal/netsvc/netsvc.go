// Package netsvc owns netdeployd's network listeners (admin HTTPS, DHCP,
// TFTP) and applies a new Plan atomically: sockets that a plan keeps are
// reused and handed the new configuration, new sockets are bound before old
// ones are released, and if anything fails to bind the running listeners are
// left untouched.
package netsvc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4/server4"

	"github.com/michaelmadell/pxe-netdeploy/internal/dhcp"
	"github.com/michaelmadell/pxe-netdeploy/internal/tftp"
)

// Plan is the set of listeners a configuration needs.
type Plan struct {
	Admin AdminPlan
	DHCP  []DHCPPlan
	TFTP  []TFTPPlan
	HTTP  []HTTPPlan
	// Notes explain services that are configured but not running (e.g. TFTP
	// with no boot content yet), keyed by service name, for health.
	Notes map[string]string
}

// AdminPlan is the admin HTTPS listener.
type AdminPlan struct {
	Addr  netip.AddrPort
	Cert  *tls.Certificate
	Field string // settings field it comes from, for errors
}

// DHCPPlan is one DHCP listener (one per interface).
type DHCPPlan struct {
	Interface string
	Bind      netip.AddrPort
	Config    dhcp.Config
	Field     string // settings field it comes from, for errors
}

func (p DHCPPlan) key() string { return p.Interface + "|" + p.Bind.String() }

// TFTPPlan is one TFTP listener.
type TFTPPlan struct {
	Interface string
	Bind      netip.AddrPort
	Config    tftp.Config
	Field     string // settings field it comes from, for errors
}

func (p TFTPPlan) key() string { return p.Bind.String() }

// HTTPPlan is one content HTTP listener (ADR 0009). Handler is swapped in
// place when the address doesn't change.
type HTTPPlan struct {
	Interface string
	Bind      netip.AddrPort
	Handler   http.Handler
	Field     string // settings field it comes from, for errors
}

func (p HTTPPlan) key() string { return p.Bind.String() }

// BindFailure is a listener that could not be started (api BindFailure).
type BindFailure struct {
	Service   string // admin, dhcp, tftp
	Interface string
	Protocol  string // udp, tcp
	Port      int
	Message   string
	Field     string // from the plan, e.g. "interfaces[1].services.tftp"
}

// BindError reports every listener a Plan could not start.
type BindError struct{ Failures []BindFailure }

func (e *BindError) Error() string {
	msgs := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		msgs[i] = fmt.Sprintf("%s %s/%d on %s: %s", f.Service, f.Protocol, f.Port, f.Interface, f.Message)
	}
	return "netsvc: " + strings.Join(msgs, "; ")
}

// Status is one service's state, for health.
type Status struct {
	Running  []string // listener descriptions, e.g. "lab0 10.77.0.1:69"
	Note     string
	Failures []BindFailure
}

// Options configure a Manager. Zero values use the real network.
type Options struct {
	Logger *slog.Logger
	// AdminGrace is how long an old admin listener stays open after the
	// admin address moves, so the response to the PUT that moved it is
	// delivered. Zero means 5 s.
	AdminGrace time.Duration
	// ListenDHCP opens a DHCP socket. Default: server4.NewIPv4UDPConn
	// (SO_BINDTODEVICE on Linux).
	ListenDHCP func(iface string, addr netip.AddrPort) (net.PacketConn, error)
	// ListenUDP and ListenTCP open TFTP and admin sockets.
	ListenUDP func(addr netip.AddrPort) (net.PacketConn, error)
	ListenTCP func(addr netip.AddrPort) (net.Listener, error)
}

// Manager runs the listeners. Create it with New, start it with Start, then
// change it with Apply. Safe for concurrent use; applies are serialised.
type Manager struct {
	opts    Options
	log     *slog.Logger
	handler http.Handler

	mu     sync.Mutex
	ctx    context.Context // from Start; listeners stop when it ends
	admin  *adminUnit
	dhcp   map[string]*dhcpUnit
	tftp   map[string]*tftpUnit
	http   map[string]*httpUnit
	status map[string]Status
	done   chan struct{} // closed when everything has stopped
}

// New returns a Manager serving handler on the admin listener.
func New(handler http.Handler, opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.AdminGrace == 0 {
		opts.AdminGrace = 5 * time.Second
	}
	if opts.ListenDHCP == nil {
		opts.ListenDHCP = func(iface string, addr netip.AddrPort) (net.PacketConn, error) {
			return server4.NewIPv4UDPConn(iface, net.UDPAddrFromAddrPort(addr))
		}
	}
	if opts.ListenUDP == nil {
		opts.ListenUDP = func(addr netip.AddrPort) (net.PacketConn, error) {
			return net.ListenUDP("udp4", net.UDPAddrFromAddrPort(addr))
		}
	}
	if opts.ListenTCP == nil {
		opts.ListenTCP = func(addr netip.AddrPort) (net.Listener, error) {
			return net.Listen("tcp", addr.String())
		}
	}
	return &Manager{
		opts:    opts,
		log:     opts.Logger.With("svc", "netsvc"),
		handler: handler,
		dhcp:    map[string]*dhcpUnit{},
		tftp:    map[string]*tftpUnit{},
		http:    map[string]*httpUnit{},
		status:  map[string]Status{},
		done:    make(chan struct{}),
	}
}

// Done is closed once the Start context has ended and every listener has
// stopped (the admin server is shut down gracefully, up to 10 s).
func (m *Manager) Done() <-chan struct{} { return m.done }

// Start brings up plan as far as it can (startup: a port in use must not stop
// the server, so the admin UI can be used to fix it). Failures are reported
// in Status and returned. Only an admin listener failure is an error, since
// nothing could then be fixed.
func (m *Manager) Start(ctx context.Context, plan Plan) ([]BindFailure, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx != nil {
		return nil, errors.New("netsvc: already started")
	}
	m.ctx = ctx

	p, err := m.prepare(plan)
	if err != nil {
		return nil, err
	}
	var adminErr error
	for _, f := range p.failures {
		if f.Service == "admin" {
			adminErr = fmt.Errorf("netsvc: admin listener on %s: %s", plan.Admin.Addr, f.Message)
		}
	}
	if adminErr != nil {
		p.discard()
		return p.failures, adminErr
	}
	m.commit(p, plan)
	context.AfterFunc(ctx, m.stopAll)
	return p.failures, nil
}

// Apply switches to plan atomically. commit runs after every new socket is
// bound and before anything changes; if it fails (e.g. the settings store
// refused a stale version), or any socket fails to bind (*BindError), the
// running listeners are untouched.
func (m *Manager) Apply(plan Plan, commit func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return errors.New("netsvc: not started")
	}
	if m.ctx.Err() != nil {
		return m.ctx.Err()
	}
	p, err := m.prepare(plan)
	if err != nil {
		return err
	}
	if len(p.failures) > 0 {
		p.discard()
		return &BindError{Failures: p.failures}
	}
	if commit != nil {
		if err := commit(); err != nil {
			p.discard()
			return err
		}
	}
	m.commit(p, plan)
	return nil
}

// Status returns each service's state.
func (m *Manager) Status() map[string]Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.status)
}

// AdminAddr is the admin listener's bound address (useful with port 0).
func (m *Manager) AdminAddr() net.Addr {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.admin == nil || m.admin.ln == nil {
		return nil
	}
	return m.admin.ln.Addr()
}

// prepared holds what prepare opened for a plan, before commit.
type prepared struct {
	adminLn   net.Listener // new admin listener, if the address changed
	dhcpNew   map[string]*dhcpUnit
	dhcpSwap  map[string]*dhcp.Server // kept units: their next server
	tftpNew   map[string]*tftpUnit
	tftpSwap  map[string]*tftp.Server
	httpNew   map[string]*httpUnit
	failures  []BindFailure
	opened    []func()
	tftpFresh []*tftp.Server // servers created for swaps, closed by discard
}

func (p *prepared) discard() {
	for _, c := range p.opened {
		c()
	}
	for _, s := range p.tftpFresh {
		s.Close()
	}
}

// prepare builds servers and binds every socket plan needs that isn't open
// yet. It changes nothing that is running.
func (m *Manager) prepare(plan Plan) (*prepared, error) {
	p := &prepared{
		dhcpNew: map[string]*dhcpUnit{}, dhcpSwap: map[string]*dhcp.Server{},
		tftpNew: map[string]*tftpUnit{}, tftpSwap: map[string]*tftp.Server{},
		httpNew: map[string]*httpUnit{},
	}
	fail := func(svc, iface, proto string, port uint16, field string, err error) {
		p.failures = append(p.failures, BindFailure{
			Service: svc, Interface: iface, Protocol: proto, Port: int(port), Message: err.Error(), Field: field,
		})
	}

	if m.admin == nil || m.admin.addr != plan.Admin.Addr {
		ln, err := m.opts.ListenTCP(plan.Admin.Addr)
		if err != nil {
			fail("admin", plan.Admin.Addr.Addr().String(), "tcp", plan.Admin.Addr.Port(), plan.Admin.Field, err)
		} else {
			p.adminLn = ln
			p.opened = append(p.opened, func() { ln.Close() })
		}
	}
	if plan.Admin.Cert == nil {
		return nil, errors.New("netsvc: admin plan has no certificate")
	}

	for _, dp := range plan.DHCP {
		k := dp.key()
		if dp.Config.Logger == nil {
			dp.Config.Logger = m.opts.Logger
		}
		srv, err := dhcp.New(dp.Config)
		if err != nil {
			fail("dhcp", dp.Interface, "udp", dp.Bind.Port(), dp.Field, err)
			continue
		}
		if _, ok := m.dhcp[k]; ok {
			p.dhcpSwap[k] = srv
			continue
		}
		conn, err := m.opts.ListenDHCP(dp.Interface, dp.Bind)
		if err != nil {
			fail("dhcp", dp.Interface, "udp", dp.Bind.Port(), dp.Field, err)
			continue
		}
		p.opened = append(p.opened, func() { conn.Close() })
		u := &dhcpUnit{desc: dp.Interface + " " + dp.Bind.String(), conn: conn}
		u.cur.Store(srv)
		p.dhcpNew[k] = u
	}

	for _, tp := range plan.TFTP {
		k := tp.key()
		if tp.Config.Logger == nil {
			tp.Config.Logger = m.opts.Logger
		}
		srv, err := tftp.New(tp.Config)
		if err != nil {
			fail("tftp", tp.Interface, "udp", tp.Bind.Port(), tp.Field, err)
			continue
		}
		p.tftpFresh = append(p.tftpFresh, srv)
		if _, ok := m.tftp[k]; ok {
			p.tftpSwap[k] = srv
			continue
		}
		conn, err := m.opts.ListenUDP(tp.Bind)
		if err != nil {
			fail("tftp", tp.Interface, "udp", tp.Bind.Port(), tp.Field, err)
			srv.Close()
			p.tftpFresh = p.tftpFresh[:len(p.tftpFresh)-1]
			continue
		}
		p.opened = append(p.opened, func() { conn.Close() })
		u := &tftpUnit{desc: tp.Interface + " " + tp.Bind.String(), conn: conn}
		u.cur.Store(srv)
		p.tftpNew[k] = u
	}

	for _, hp := range plan.HTTP {
		k := hp.key()
		if _, ok := m.http[k]; ok {
			continue // handler swapped in commit
		}
		ln, err := m.opts.ListenTCP(hp.Bind)
		if err != nil {
			fail("content", hp.Interface, "tcp", hp.Bind.Port(), hp.Field, err)
			continue
		}
		p.opened = append(p.opened, func() { ln.Close() })
		p.httpNew[k] = newHTTPUnit(hp.Interface+" http://"+hp.Bind.String(), ln, m.log)
	}
	return p, nil
}

// commit switches to a prepared plan. Nothing here can fail.
func (m *Manager) commit(p *prepared, plan Plan) {
	// Admin: swap the certificate, then move the listener if needed.
	if m.admin == nil {
		m.admin = newAdminUnit(m.ctx, m.handler, m.log)
	}
	m.admin.cert.Store(plan.Admin.Cert)
	if p.adminLn != nil {
		old := m.admin.ln
		m.admin.serve(p.adminLn, plan.Admin.Addr)
		if old != nil {
			m.log.Info("admin listener moved; closing the old one after grace",
				"old", old.Addr().String(), "new", p.adminLn.Addr().String(), "grace", m.opts.AdminGrace.String())
			time.AfterFunc(m.opts.AdminGrace, func() { old.Close() })
		}
	}

	// DHCP: keep, swap, add, remove.
	wantDHCP := map[string]bool{}
	for _, dp := range plan.DHCP {
		wantDHCP[dp.key()] = true
	}
	for k, srv := range p.dhcpSwap {
		u := m.dhcp[k]
		if srv.TakeLeases(u.cur.Load()) {
			m.log.Debug("dhcp leases carried over", "listener", u.desc)
		}
		u.cur.Store(srv)
		m.log.Info("dhcp listener reconfigured", "listener", u.desc)
	}
	for k, u := range m.dhcp {
		if !wantDHCP[k] {
			u.stop()
			delete(m.dhcp, k)
			m.log.Info("dhcp listener stopped", "listener", u.desc)
		}
	}
	for k, u := range p.dhcpNew {
		u.start(m.ctx)
		m.dhcp[k] = u
		m.log.Info("dhcp listener started", "listener", u.desc)
	}

	// TFTP: same, retiring replaced servers once their transfers finish.
	wantTFTP := map[string]bool{}
	for _, tp := range plan.TFTP {
		wantTFTP[tp.key()] = true
	}
	for k, srv := range p.tftpSwap {
		u := m.tftp[k]
		old := u.cur.Swap(srv)
		go old.Retire()
		m.log.Info("tftp listener reconfigured", "listener", u.desc)
	}
	for k, u := range m.tftp {
		if !wantTFTP[k] {
			u.stop()
			delete(m.tftp, k)
			m.log.Info("tftp listener stopped", "listener", u.desc)
		}
	}
	for k, u := range p.tftpNew {
		u.start(m.ctx)
		m.tftp[k] = u
		m.log.Info("tftp listener started", "listener", u.desc)
	}

	// Content HTTP: swap handlers on kept listeners, start new, stop removed.
	wantHTTP := map[string]http.Handler{}
	for _, hp := range plan.HTTP {
		wantHTTP[hp.key()] = hp.Handler
	}
	for k, u := range m.http {
		if h, ok := wantHTTP[k]; ok {
			u.handler.Store(&h)
			continue
		}
		u.stop()
		delete(m.http, k)
		m.log.Info("content listener stopped", "listener", u.desc)
	}
	for k, u := range p.httpNew {
		h := wantHTTP[k]
		u.handler.Store(&h)
		u.start()
		m.http[k] = u
		m.log.Info("content listener started", "listener", u.desc)
	}

	// Status.
	byService := map[string][]BindFailure{}
	for _, f := range p.failures {
		byService[f.Service] = append(byService[f.Service], f)
	}
	m.status = map[string]Status{
		"admin":   {Running: []string{m.admin.addr.String()}, Failures: byService["admin"]},
		"dhcp":    {Running: descs(m.dhcp), Note: plan.Notes["dhcp"], Failures: byService["dhcp"]},
		"tftp":    {Running: descs(m.tftp), Note: plan.Notes["tftp"], Failures: byService["tftp"]},
		"content": {Running: descs(m.http), Note: plan.Notes["content"], Failures: byService["content"]},
	}
}

type described interface{ description() string }

func descs[U described](units map[string]U) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, u.description())
	}
	slices.Sort(out)
	return out
}

// stopAll runs when the Start context ends.
func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer close(m.done)
	for _, u := range m.dhcp {
		u.wait()
	}
	for _, u := range m.tftp {
		u.wait()
	}
	for _, u := range m.http {
		u.stop()
	}
	if m.admin != nil {
		m.admin.shutdown()
	}
}

// ---- units

type dhcpUnit struct {
	desc string
	conn net.PacketConn
	cur  atomic.Pointer[dhcp.Server]

	cancel context.CancelFunc
	done   chan struct{}
}

func (u *dhcpUnit) description() string { return u.desc }

func (u *dhcpUnit) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	u.cancel, u.done = cancel, make(chan struct{})
	go func() {
		defer close(u.done)
		_ = dhcp.ServeSwappable(ctx, u.conn, &u.cur)
	}()
}

func (u *dhcpUnit) stop() { u.cancel(); <-u.done }
func (u *dhcpUnit) wait() { <-u.done }

type tftpUnit struct {
	desc string
	conn net.PacketConn
	cur  atomic.Pointer[tftp.Server]

	cancel context.CancelFunc
	done   chan struct{}
}

func (u *tftpUnit) description() string { return u.desc }

func (u *tftpUnit) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	u.cancel, u.done = cancel, make(chan struct{})
	go func() {
		defer close(u.done)
		_ = tftp.ServeSwappable(ctx, u.conn, &u.cur)
		u.cur.Load().Close()
	}()
}

func (u *tftpUnit) stop() { u.cancel(); <-u.done }
func (u *tftpUnit) wait() { <-u.done }

// adminUnit is one http.Server that can move between listeners.
type adminUnit struct {
	srv  *http.Server
	cert atomic.Pointer[tls.Certificate]
	ln   net.Listener
	addr netip.AddrPort
	log  *slog.Logger
}

func newAdminUnit(ctx context.Context, h http.Handler, log *slog.Logger) *adminUnit {
	u := &adminUnit{log: log}
	u.srv = &http.Server{
		Handler: h,
		// Requests end when the server stops, so long-lived ones (the
		// /events stream) don't hold up the graceful shutdown.
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				return u.cert.Load(), nil
			},
		},
	}
	return u
}

func (u *adminUnit) serve(ln net.Listener, addr netip.AddrPort) {
	u.ln, u.addr = ln, addr
	u.log.Info("admin listener started", "addr", "https://"+ln.Addr().String())
	go func() {
		err := u.srv.ServeTLS(ln, "", "")
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			u.log.Error("admin listener stopped", "addr", ln.Addr().String(), "err", err)
		}
	}()
}

func (u *adminUnit) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := u.srv.Shutdown(ctx); err != nil {
		u.log.Warn("admin shutdown", "err", err)
	}
}

// httpUnit is one content HTTP listener whose handler can be swapped.
type httpUnit struct {
	desc    string
	ln      net.Listener
	srv     *http.Server
	handler atomic.Pointer[http.Handler]
	log     *slog.Logger
	done    chan struct{}
}

func newHTTPUnit(desc string, ln net.Listener, log *slog.Logger) *httpUnit {
	u := &httpUnit{desc: desc, ln: ln, log: log, done: make(chan struct{})}
	u.srv = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			(*u.handler.Load()).ServeHTTP(w, r)
		}),
		// No write timeout: a WIM can take minutes. Slow or idle clients
		// can't hold connections open indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	return u
}

func (u *httpUnit) description() string { return u.desc }

func (u *httpUnit) start() {
	go func() {
		defer close(u.done)
		err := u.srv.Serve(u.ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			u.log.Error("content listener stopped", "listener", u.desc, "err", err)
		}
	}()
}

// stop lets running downloads finish for up to 10 s, then closes them.
func (u *httpUnit) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := u.srv.Shutdown(ctx); err != nil {
		u.srv.Close()
	}
	<-u.done
}
