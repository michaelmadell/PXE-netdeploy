// Command netdeployd is the deployment server: admin API + UI today,
// DHCP / TFTP / content listeners as later phases land.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/certs"
	"github.com/michaelmadell/pxe-netdeploy/internal/server"
	"github.com/michaelmadell/pxe-netdeploy/web"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	adminAddr := flag.String("admin-addr", "127.0.0.1:8443", "admin API/UI listen address (HTTPS)")
	dataDir := flag.String("data", "./data", "data directory (database, content store, certificates)")
	logFormat := flag.String("log", "text", "log format: text or json")
	flag.Parse()

	var h slog.Handler = slog.NewTextHandler(os.Stdout, nil)
	if *logFormat == "json" {
		h = slog.NewJSONHandler(os.Stdout, nil)
	}
	slog.SetDefault(slog.New(h))

	if err := run(*adminAddr, *dataDir); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(adminAddr, dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return err
	}

	tlsCfg, err := certs.LoadOrCreateAdminTLS(dataDir)
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
		Addr:              adminAddr,
		Handler:           server.Recover(server.Logging(mux)),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("admin listener started", "addr", "https://"+adminAddr, "version", version)
		errCh <- srv.ListenAndServeTLS("", "")
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
