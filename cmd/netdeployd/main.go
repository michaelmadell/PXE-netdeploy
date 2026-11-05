// Command netdeployd is the deployment server: admin API + UI, and (Phase 1
// spike) DHCP and TFTP enabled by flags. Content listeners land later.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/certs"
	"github.com/michaelmadell/pxe-netdeploy/internal/dhcp"
	"github.com/michaelmadell/pxe-netdeploy/internal/server"
	"github.com/michaelmadell/pxe-netdeploy/internal/tftp"
	"github.com/michaelmadell/pxe-netdeploy/web"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

type options struct {
	adminAddr string
	dataDir   string

	// Phase 1 spike: network boot services, off unless their flags are set.
	dhcpIface          string
	dhcpServerIP       string
	dhcpPool           string
	dhcpRouter         string
	dhcpDNS            string
	dhcpLease          time.Duration
	dhcpPXEClientClass bool
	bootFile           string
	httpBootURL        string

	tftpRoot          string
	tftpAddr          string
	tftpMaxBlockSize  int
	tftpMaxWindowSize int
	tftpTimeout       time.Duration
}

func main() {
	var o options
	flag.StringVar(&o.adminAddr, "admin-addr", "127.0.0.1:8443", "admin API/UI listen address (HTTPS)")
	flag.StringVar(&o.dataDir, "data", "./data", "data directory (database, content store, certificates)")
	logFormat := flag.String("log", "text", "log format: text or json")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn or error")

	flag.StringVar(&o.dhcpIface, "dhcp-iface", "", "serve DHCP (full mode) on this interface, e.g. \"vEthernet (NetDeployLab)\"; empty disables DHCP")
	flag.StringVar(&o.dhcpServerIP, "dhcp-server-ip", "", "this host's address on -dhcp-iface (default: the interface's first IPv4 address)")
	flag.StringVar(&o.dhcpPool, "dhcp-pool", "", "address pool as start-end, e.g. 10.77.0.100-10.77.0.199 (required with -dhcp-iface)")
	flag.StringVar(&o.dhcpRouter, "dhcp-router", "", "default gateway to hand out (optional)")
	flag.StringVar(&o.dhcpDNS, "dhcp-dns", "", "comma-separated DNS servers to hand out (optional)")
	flag.DurationVar(&o.dhcpLease, "dhcp-lease", dhcp.DefaultLeaseTime, "lease time")
	flag.BoolVar(&o.dhcpPXEClientClass, "dhcp-pxeclient-class", false, "add option 60 \"PXEClient\" to PXE replies")
	flag.StringVar(&o.bootFile, "boot-file", "bootmgfw.efi", "boot file for UEFI x64 PXE clients")
	flag.StringVar(&o.httpBootURL, "http-boot-url", "", "boot URL for UEFI x64 HTTP Boot clients (optional)")

	flag.StringVar(&o.tftpRoot, "tftp-root", "", "serve this directory read-only over TFTP; empty disables TFTP")
	flag.StringVar(&o.tftpAddr, "tftp-addr", "0.0.0.0:69", "TFTP listen address")
	flag.IntVar(&o.tftpMaxBlockSize, "tftp-max-blksize", tftp.DefaultMaxBlockSize, "largest TFTP blksize to accept (8..65464); larger client requests are lowered to this")
	flag.IntVar(&o.tftpMaxWindowSize, "tftp-max-windowsize", tftp.DefaultMaxWindowSize, "largest TFTP windowsize to accept (1..4096); larger client requests are lowered to this")
	flag.DurationVar(&o.tftpTimeout, "tftp-timeout", tftp.DefaultTimeout, "TFTP retransmit timeout")
	flag.Parse()

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid -log-level %q\n", *logLevel)
		os.Exit(2)
	}
	hopts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stdout, hopts)
	if *logFormat == "json" {
		h = slog.NewJSONHandler(os.Stdout, hopts)
	}
	slog.SetDefault(slog.New(h))

	if err := run(o); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// service is a long-running listener started by run.
type service struct {
	name  string
	serve func(ctx context.Context) error
}

func run(o options) error {
	if err := os.MkdirAll(o.dataDir, 0o750); err != nil {
		return err
	}

	tlsCfg, err := certs.LoadOrCreateAdminTLS(o.dataDir)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()

	impl := server.New(version)
	strict := api.NewStrictHandlerWithOptions(impl, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  server.RequestError,
		ResponseErrorHandlerFunc: server.ResponseError,
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseURL:          "/api/v1",
		BaseRouter:       mux,
		ErrorHandlerFunc: server.RequestError, // bad path/query/header params: problem JSON, not plain text
	})
	// Unknown /api paths get a JSON 404 instead of falling through to the SPA.
	mux.HandleFunc("/api/", server.NotFound)
	mux.Handle("/", web.Handler())

	srv := &http.Server{
		Addr:              o.adminAddr,
		Handler:           server.Recover(server.Logging(mux)),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
	}

	services, err := networkServices(o, impl)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var wg sync.WaitGroup
	for _, svc := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.serve(ctx)
			if err != nil {
				impl.SetService(svc.name, api.ServiceHealthStatusError, err.Error())
				cancel(fmt.Errorf("%s: %w", svc.name, err))
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("admin listener started", "addr", "https://"+o.adminAddr, "version", version)
		errCh <- srv.ListenAndServeTLS("", "")
	}()

	var runErr error
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
		cancel(nil)
	case <-ctx.Done():
		runErr = context.Cause(ctx)
		if errors.Is(runErr, context.Canceled) {
			runErr = nil
		}
		slog.Info("shutting down")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = err
	}
	wg.Wait()
	return runErr
}

// networkServices builds the DHCP and TFTP services enabled by flags.
func networkServices(o options, impl *server.Server) ([]service, error) {
	var out []service

	if o.tftpRoot != "" {
		ts, err := tftp.New(tftp.Config{
			Root:          o.tftpRoot,
			MaxBlockSize:  o.tftpMaxBlockSize,
			MaxWindowSize: o.tftpMaxWindowSize,
			Timeout:       o.tftpTimeout,
		})
		if err != nil {
			return nil, err
		}
		impl.SetService("tftp", api.ServiceHealthStatusOk, "spike: serving "+o.tftpRoot+" on "+o.tftpAddr)
		out = append(out, service{"tftp", func(ctx context.Context) error {
			defer ts.Close()
			return ts.ListenAndServe(ctx, o.tftpAddr)
		}})
	}

	if o.dhcpIface != "" {
		cfg, err := dhcpConfig(o)
		if err != nil {
			return nil, err
		}
		ds, err := dhcp.New(cfg)
		if err != nil {
			return nil, err
		}
		if o.tftpRoot == "" {
			slog.Warn("DHCP is enabled without -tftp-root: PXE clients will get a boot file nobody serves")
		}
		impl.SetService("dhcp", api.ServiceHealthStatusOk, "spike: full mode on "+o.dhcpIface+", pool "+o.dhcpPool)
		out = append(out, service{"dhcp", ds.ListenAndServe})
	} else if o.dhcpPool != "" {
		return nil, errors.New("-dhcp-pool needs -dhcp-iface")
	}
	return out, nil
}

func dhcpConfig(o options) (dhcp.Config, error) {
	cfg := dhcp.Config{
		Interface:      o.dhcpIface,
		LeaseTime:      o.dhcpLease,
		BootFileUEFI:   o.bootFile,
		HTTPBootURL:    o.httpBootURL,
		PXEClientClass: o.dhcpPXEClientClass,
	}
	var err error
	if o.dhcpServerIP != "" {
		if cfg.ServerIP, err = parseIPv4("-dhcp-server-ip", o.dhcpServerIP); err != nil {
			return cfg, err
		}
	}
	start, end, ok := strings.Cut(o.dhcpPool, "-")
	if !ok {
		return cfg, fmt.Errorf("-dhcp-pool %q: want start-end, e.g. 10.77.0.100-10.77.0.199", o.dhcpPool)
	}
	if cfg.PoolStart, err = parseIPv4("-dhcp-pool", strings.TrimSpace(start)); err != nil {
		return cfg, err
	}
	if cfg.PoolEnd, err = parseIPv4("-dhcp-pool", strings.TrimSpace(end)); err != nil {
		return cfg, err
	}
	if o.dhcpRouter != "" {
		if cfg.Router, err = parseIPv4("-dhcp-router", o.dhcpRouter); err != nil {
			return cfg, err
		}
	}
	if o.dhcpDNS != "" {
		for _, d := range strings.Split(o.dhcpDNS, ",") {
			ip, err := parseIPv4("-dhcp-dns", strings.TrimSpace(d))
			if err != nil {
				return cfg, err
			}
			cfg.DNS = append(cfg.DNS, ip)
		}
	}
	return cfg, nil
}

func parseIPv4(flagName, s string) (net.IP, error) {
	ip := net.ParseIP(s).To4()
	if ip == nil {
		return nil, fmt.Errorf("%s: %q is not an IPv4 address", flagName, s)
	}
	return ip, nil
}
