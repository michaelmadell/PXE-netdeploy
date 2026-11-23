// Command netdeployd is the deployment server: admin API + UI, DHCP and TFTP.
// Everything except the data and content directories and logging comes from
// the stored settings (GET/PUT /api/v1/settings); see -h for the bootstrap flags.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agentbin"
	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/backup"
	"github.com/michaelmadell/pxe-netdeploy/internal/boot"
	"github.com/michaelmadell/pxe-netdeploy/internal/bootimage"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/certs"
	"github.com/michaelmadell/pxe-netdeploy/internal/contentca"
	"github.com/michaelmadell/pxe-netdeploy/internal/datalock"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/imports"
	"github.com/michaelmadell/pxe-netdeploy/internal/ipxe"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/netsvc"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
	"github.com/michaelmadell/pxe-netdeploy/internal/server"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
	"github.com/michaelmadell/pxe-netdeploy/web"
)

// commands are the offline subcommands.
var commands = map[string]func(args []string) error{
	"key":     keyCommand,
	"admin":   adminCommand,
	"backup":  backupCommand,
	"restore": restoreCommand,
	"service": serviceCommand,
}

func backupConfig(b settings.Backup) backup.Config {
	c := backup.Config{Schedule: b.Schedule, Retention: b.Retention}
	if d := b.Destination; d != nil {
		c.Destination = &backup.Destination{Type: d.Type, Path: d.Path, CredentialSecretID: d.CredentialSecretID}
	}
	return c
}

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

type options struct {
	dataDir    string
	contentDir string // empty: <data>/content
	adminAddr  string // empty: use Settings.admin
	agentDir   string // agents when the binary embeds none
	wimlibPath string // empty: wimlib-imagex from PATH
	ipxeDir    string // the signed iPXE chain for HTTP(S) Boot (ADR 0032)
	dev        bool   // relaxed session cookie for `just dev` (ADR 0029)
}

func main() {
	privateFiles()
	// Offline commands: netdeployd <command> [flags]. Without one, serve.
	if len(os.Args) > 1 {
		if cmd, ok := commands[os.Args[1]]; ok {
			if err := cmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}
	var o options
	flag.StringVar(&o.dataDir, "data", "./data", "data directory (database, certificates)")
	flag.StringVar(&o.contentDir, "content", "", "content store directory (imported and built files); default <data>/content")
	flag.StringVar(&o.adminAddr, "admin-addr", "", "recovery override for the admin listener, e.g. 127.0.0.1:8443; normally set in settings")
	flag.StringVar(&o.agentDir, "agent-dir", "dist", "directory with agent-windows-<arch>.exe, used when this binary embeds no agents (built without -tags agent)")
	flag.StringVar(&o.wimlibPath, "wimlib", "", "path to wimlib-imagex (default: found on PATH)")
	flag.StringVar(&o.ipxeDir, "ipxe-dir", "", "directory with <arch>/{ipxe-shim.efi,ipxe.efi,wimboot} for HTTP(S) Boot (default: the one installed with netdeployd, else dist/ipxe)")
	flag.BoolVar(&o.dev, "dev", false, "development only: session cookie without Secure, Origin may match X-Forwarded-Host (the Vite proxy)")
	logFormat := flag.String("log", "text", "log format: text or json")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn or error")
	logFile := flag.String("log-file", "", "write the log to this file instead of standard output (the Windows service does)")
	flag.Parse()
	if o.wimlibPath == "" {
		o.wimlibPath = bundledWimlib()
	}
	if o.ipxeDir == "" {
		o.ipxeDir = bundledIPXE()
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid -log-level %q\n", *logLevel)
		os.Exit(2)
	}
	out := io.Writer(os.Stdout)
	if *logFile != "" {
		f, err := openLog(*logFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer f.Close()
		out = f
	}
	hopts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(out, hopts)
	if *logFormat == "json" {
		h = slog.NewJSONHandler(out, hopts)
	}
	slog.SetDefault(slog.New(h))
	slog.Info("netdeployd starting", "version", version)

	// Under the Windows Service Control Manager: it starts and stops run.
	if handled, err := runService(o); handled {
		if err != nil {
			slog.Error("fatal", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(context.Background(), o); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// openLog appends to path, first moving a file over 64 MiB to path.1.
func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("-log-file: %w", err)
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 64<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("-log-file: %w", err)
	}
	return f, nil
}

// bundledIPXE is the signed iPXE chain the packages install (ADR 0032):
// <program>\ipxe on Windows, /usr/lib/netdeploy/ipxe from the .deb, or
// dist/ipxe from scripts/fetch-ipxe.ps1 in a checkout. "" if none exists.
func bundledIPXE() string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "ipxe"))
	}
	if runtime.GOOS == "linux" {
		dirs = append(dirs, "/usr/lib/netdeploy/ipxe")
	}
	dirs = append(dirs, filepath.Join("dist", "ipxe"))
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return ""
}

// bundledWimlib is the wimlib the Windows installer ships next to the
// server (<program>\wimlib\wimlib-imagex.exe, ADR 0031), if it's there;
// otherwise "" (look on PATH).
func bundledWimlib() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	name := "wimlib-imagex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), "wimlib", name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func run(parent context.Context, o options) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var override netip.AddrPort
	if o.adminAddr != "" {
		var err error
		if override, err = netip.ParseAddrPort(o.adminAddr); err != nil {
			return fmt.Errorf("-admin-addr %q: want ip:port, e.g. 127.0.0.1:8443", o.adminAddr)
		}
	}

	lock, err := datalock.Acquire(o.dataDir)
	if err != nil {
		return err
	}
	defer lock.Release()

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

	contentDir := o.contentDir
	if contentDir == "" {
		contentDir = filepath.Join(o.dataDir, "content")
	}

	// What clients boot (ADR 0024): its content store and settings are
	// filled in below, before anything starts.
	booter := &boot.Profiles{Store: st}
	var svc *settings.Service
	booter.DefaultProfileID = func() string {
		cfg, _ := svc.Current()
		return cfg.BootPrompt.DefaultProfileID
	}

	// The content CA (ADR 0028): its key is sealed with the secrets key.
	keys := secrets.NewKeys(o.dataDir)
	ca := contentca.New(contentca.Options{
		DataDir: o.dataDir,
		Ring:    keys.Ring,
		Names:   func() contentca.Names { return contentNames(svc, st) },
	})
	if err := ca.Load(); err != nil {
		return err
	}

	chain, err := ipxe.Load(o.ipxeDir)
	if err != nil {
		return err
	}
	if archs := chain.Archs(); len(archs) > 0 {
		slog.Info("HTTP Boot chain loaded", "dir", chain.Path, "architectures", strings.Join(archs, ","))
	} else {
		slog.Warn("HTTP Boot clients won't be answered", "reason", chain.Reason)
	}

	var handler http.Handler
	network := netsvc.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}), netsvc.Options{})
	svc = settings.NewService(settings.Options{
		Store:              st,
		Network:            network,
		Booter:             booter,
		AdminOverride:      override,
		GeneratedAdminCert: &generated.Certificates[0],
		ContentCA:          ca,
		IPXE:               chain,
		Events:             bus,
		// The agent API is the same API handler; the content listener only
		// forwards /api/v1/agent/ to it.
		Agent:        http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }),
		LinuxContent: booter.LinuxContent,
		CheckBackup: func(ctx context.Context, b settings.Backup) []settings.FieldError {
			if b.Destination == nil {
				return nil
			}
			d := backup.Destination{Type: b.Destination.Type, Path: b.Destination.Path, CredentialSecretID: b.Destination.CredentialSecretID}
			if f, msg := backup.CheckDestination(ctx, st, o.dataDir, contentDir, d); f != "" {
				return []settings.FieldError{{Field: "backup.destination." + f, Message: msg}}
			}
			return nil
		},
	})
	// Before cas.Open empties staging: discard DISM mounts left by builds
	// interrupted by a crash (ADR 0022).
	bootimage.CleanupMounts(ctx, contentDir, slog.Default())
	content, err := cas.Open(ctx, cas.Options{
		Root:  contentDir,
		Store: st,
		Reserve: func() int64 {
			cfg, _ := svc.Current()
			return cfg.Storage.ReserveBytes
		},
		Pinned: svc.Sessions().TreeIDs, // trees clients are booting from
	})
	if err != nil {
		return err
	}
	booter.Tree = content.Tree
	booter.BlobPath = content.BlobPath
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
	runner.Register(imports.NewDrivers(imports.DriversOptions{Store: st, Content: content, Uploads: up, Events: bus}).Kind())
	vault := secrets.NewVault(st, keys)
	backups := backup.NewService(backup.ServiceOptions{
		Store: st, Content: content, Vault: vault, Jobs: runner, DataDir: o.dataDir, ServerVersion: version,
		Config: func() backup.Config { cfg, _ := svc.Current(); return backupConfig(cfg.Backup) },
	})
	runner.Register(backups.Kind())
	agents, err := agentbin.Load(version, o.agentDir)
	if err != nil {
		return err
	}
	slog.Info("agents", "version", agents.Version, "source", agents.Source, "architectures", agents.Architectures(), "modified", agents.Modified)
	boot := bootimage.New(bootimage.Options{Store: st, Content: content, Agents: agents, Events: bus, WimlibPath: o.wimlibPath,
		ContentCA: func() ([]byte, string) { return ca.PEM(), ca.Fingerprint() }})
	runner.Register(boot.Kind())
	// The first account (ADR 0029): a setup code while there is none.
	var setup *server.SetupCode
	if n, err := st.CountUsers(ctx); err != nil {
		return err
	} else if n == 0 {
		if setup, err = server.NewSetupCode(o.dataDir); err != nil {
			return fmt.Errorf("setup code: %w", err)
		}
		slog.Warn("no admin account yet: open the web UI and create one with this setup code",
			"setup_code", setup.Display(), "file", setup.Path())
	} else {
		server.RemoveSetupCode(o.dataDir)
	}
	if o.dev {
		slog.Warn("-dev: the session cookie isn't Secure; for development only")
	}
	impl := server.New(version, server.Deps{Settings: svc, Network: network, Store: st, Events: bus, Content: content, Jobs: runner, Uploads: up,
		Vault: vault, Backup: backups, Boot: boot, CA: ca, IPXE: chain, Setup: setup, Dev: o.dev, DataDir: o.dataDir})
	if handler, err = adminHandler(impl, st, up); err != nil {
		return err
	}

	go server.PurgeIdempotency(ctx, st, time.Hour)
	go server.PurgeSessions(ctx, st, time.Hour)
	if err := up.Start(ctx); err != nil {
		return err
	}
	if err := runner.Start(ctx); err != nil {
		return err
	}
	if err := svc.Start(ctx); err != nil {
		return err
	}
	backups.Start(ctx)                // after settings are loaded: a missed backup is queued now
	go impl.RunDeploymentSweeper(ctx) // deployment timeouts (ADR 0026)
	<-ctx.Done()
	slog.Info("shutting down")
	<-network.Done()
	<-runner.Done() // running jobs end as interrupted
	return nil
}

// contentNames is what the HTTPS content certificate covers (ADR 0028):
// the settings' HTTPS addresses and host name, and every host named in a
// boot image's https:// server URLs.
func contentNames(svc *settings.Service, st *store.Store) contentca.Names {
	n := svc.ContentNames()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	urls, err := st.BootImageServerURLs(ctx)
	if err != nil {
		slog.Warn("content certificate: boot image server URLs", "err", err)
	}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" {
			continue
		}
		if a, err := netip.ParseAddr(u.Hostname()); err == nil {
			n.IPs = append(n.IPs, a)
		} else {
			n.DNS = append(n.DNS, u.Hostname())
		}
	}
	return n
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
		// Agent downloads are streamed with Range, outside the strict
		// handler (ADR 0025).
		if strings.HasPrefix(r.URL.Path, "/api/v1/agent/files/") {
			impl.AgentFiles(w, r)
			return
		}
		validated.ServeHTTP(w, r)
	})
	auth, err := impl.Authenticate(spec)
	if err != nil {
		return nil, err
	}
	return server.Recover(server.Logging(server.WithRequestInfo(auth(routed)))), nil
}
