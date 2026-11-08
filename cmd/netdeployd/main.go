// Command netdeployd is the deployment server: admin API + UI, DHCP and TFTP.
// Everything except the data and content directories and logging comes from
// the stored settings (GET/PUT /api/v1/settings); see -h for the bootstrap flags.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/certs"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/imports"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/netsvc"
	"github.com/michaelmadell/pxe-netdeploy/internal/server"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
	"github.com/michaelmadell/pxe-netdeploy/web"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

type options struct {
	dataDir    string
	contentDir string // empty: <data>/content
	adminAddr  string // empty: use Settings.admin
	dev        settings.Dev
}

func main() {
	var o options
	flag.StringVar(&o.dataDir, "data", "./data", "data directory (database, certificates)")
	flag.StringVar(&o.contentDir, "content", "", "content store directory (imported and built files); default <data>/content")
	flag.StringVar(&o.adminAddr, "admin-addr", "", "recovery override for the admin listener, e.g. 127.0.0.1:8443; normally set in settings")
	logFormat := flag.String("log", "text", "log format: text or json")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn or error")

	// Temporary until boot images provide boot content (Phase 2 step 4).
	flag.StringVar(&o.dev.TFTPRoot, "tftp-root", "", "TEMPORARY: directory served read-only over TFTP on interfaces with services.tftp")
	flag.StringVar(&o.dev.BootFile, "boot-file", "bootmgfw.efi", "TEMPORARY: DHCP boot file for UEFI x64 PXE clients")
	flag.StringVar(&o.dev.HTTPBootURL, "http-boot-url", "", "TEMPORARY: DHCP boot URL for UEFI x64 HTTP Boot clients")
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

func run(o options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var override netip.AddrPort
	if o.adminAddr != "" {
		var err error
		if override, err = netip.ParseAddrPort(o.adminAddr); err != nil {
			return fmt.Errorf("-admin-addr %q: want ip:port, e.g. 127.0.0.1:8443", o.adminAddr)
		}
	}

	st, err := store.Open(ctx, o.dataDir, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()

	generated, err := certs.LoadOrCreateAdminTLS(o.dataDir)
	if err != nil {
		return err
	}

	// The admin handler needs the Server, which needs the Manager, which
	// serves the handler: build the handler last and forward to it.
	bus := events.New()

	var handler http.Handler
	network := netsvc.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}), netsvc.Options{})
	svc := settings.NewService(settings.Options{
		Store:              st,
		Network:            network,
		Dev:                o.dev,
		AdminOverride:      override,
		GeneratedAdminCert: &generated.Certificates[0],
		Events:             bus,
		// The agent API is the same API handler; the content listener only
		// forwards /api/v1/agent/ to it.
		Agent: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }),
	})
	contentDir := o.contentDir
	if contentDir == "" {
		contentDir = filepath.Join(o.dataDir, "content")
	}
	content, err := cas.Open(ctx, cas.Options{
		Root:  contentDir,
		Store: st,
		Reserve: func() int64 {
			cfg, _ := svc.Current()
			return cfg.Storage.ReserveBytes
		},
	})
	if err != nil {
		return err
	}
	up := uploads.New(uploads.Options{
		Store:   st,
		Content: content,
		Expiry: func() time.Duration {
			cfg, _ := svc.Current()
			return time.Duration(cfg.Storage.UploadExpiryHours) * time.Hour
		},
	})
	runner := jobs.New(jobs.Options{Store: st, Events: bus})
	runner.Register(imports.NewImages(imports.ImagesOptions{Store: st, Content: content, Uploads: up, Events: bus}).Kind())
	impl := server.New(version, server.Deps{Settings: svc, Network: network, Store: st, Events: bus, Content: content, Jobs: runner})
	if handler, err = adminHandler(impl, st, up); err != nil {
		return err
	}

	go server.PurgeIdempotency(ctx, st, time.Hour)
	if err := up.Start(ctx); err != nil {
		return err
	}
	if err := runner.Start(ctx); err != nil {
		return err
	}
	if err := svc.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	slog.Info("shutting down")
	<-network.Done()
	<-runner.Done() // running jobs end as interrupted
	return nil
}

func adminHandler(impl *server.Server, st *store.Store, up *uploads.Manager) (http.Handler, error) {
	spec, err := api.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("load embedded spec: %w", err)
	}
	validate, err := server.RequestValidator(spec)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
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
	// Validation runs first, so only valid requests are kept for replay.
	validated := validate(server.Idempotency(st)(mux))
	// tus uploads bypass validation and idempotency: the validator reads
	// whole bodies, and tus isn't JSON (ADR 0017).
	routed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == uploads.Prefix || strings.HasPrefix(r.URL.Path, uploads.Prefix+"/") {
			up.ServeHTTP(w, r)
			return
		}
		validated.ServeHTTP(w, r)
	})
	return server.Recover(server.Logging(routed)), nil
}
