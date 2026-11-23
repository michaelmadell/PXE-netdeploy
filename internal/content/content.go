// Package content is the per-interface content HTTP listener's handler
// (ADR 0009): boot files for the requesting client and the agent API,
// nothing else. The admin API is never reachable here.
package content

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"time"
)

// BootPrefix is where a client's boot content is served.
const BootPrefix = "/content/boot/"

// AgentPrefix is the agent API, forwarded to the API handler.
const AgentPrefix = "/api/v1/agent/"

// LinuxPrefix serves Linux images by id (ADR 0034): /content/linux/<id>/<path>.
const LinuxPrefix = "/content/linux/"

// ReportPrefix takes automatic Linux installs' reports (ADR 0036):
// POST /content/report/<token>/<kind>.
const ReportPrefix = "/content/report/"

// SeedPrefix serves automatic Linux installs' NoCloud seeds (ADR 0035):
// /content/seed/<token>/<file>.
const SeedPrefix = "/content/seed/"

// Config configures a Handler.
type Config struct {
	// DefaultRoot serves clients ClientRoot doesn't place (nil or no
	// ClientRoot). Nil: no boot content (404). Its Open matches paths
	// case-insensitively and returns files that implement io.ReadSeeker
	// (cas.Tree and cas.DirFS do).
	DefaultRoot fs.FS
	// ClientRoot picks a client's content and log attributes, as for TFTP
	// (settings.Service.clientRoot).
	ClientRoot func(client netip.Addr) (root fs.FS, attrs []any)
	// Shared serves paths under SharedDir ("ipxe/...") for every client,
	// ahead of the client's own content: the signed iPXE chain HTTP(S) Boot
	// clients start with (ADR 0032). Nil: those paths fall through to the
	// client's content. Its files must implement io.ReadSeeker.
	Shared    fs.FS
	SharedDir string
	// Script generates <SharedDir>/<arch>/autoexec.ipxe for the requesting
	// client (ADR 0034). ok=false falls through to Shared's static script.
	Script func(r *http.Request, client netip.Addr, arch string) (body []byte, attrs []any, ok bool)
	// Linux opens a ready Linux image's content (its tree and image.iso)
	// for LinuxPrefix. Nil: 404. fs.ErrNotExist: no such image.
	Linux func(imageID string) (fs.FS, error)
	// Seed serves a NoCloud seed file for a deployment token: the body, the
	// HTTP status and log attributes. Nil: 404.
	Seed func(r *http.Request, token, file string) (body []byte, status int, attrs []any)
	// Report takes an installer report for a deployment token: the HTTP
	// status, whether a success is logged at debug level only, and log
	// attributes. Nil: 404.
	Report func(r *http.Request, token, kind string) (status int, quiet bool, attrs []any)
	// Agent handles AgentPrefix. Nil: 404.
	Agent  http.Handler
	Logger *slog.Logger
}

// Handler serves one interface's content listener.
type Handler struct {
	cfg Config
	log *slog.Logger
}

// New returns a Handler.
func New(cfg Config) *Handler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Handler{cfg: cfg, log: cfg.Logger.With("svc", "content")}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, AgentPrefix) && h.cfg.Agent != nil:
		h.cfg.Agent.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, BootPrefix):
		h.serveBoot(w, r)
	case strings.HasPrefix(r.URL.Path, LinuxPrefix) && h.cfg.Linux != nil:
		h.serveLinux(w, r)
	case strings.HasPrefix(r.URL.Path, SeedPrefix) && h.cfg.Seed != nil:
		h.serveSeed(w, r)
	case strings.HasPrefix(r.URL.Path, ReportPrefix) && h.cfg.Report != nil:
		h.serveReport(w, r)
	default:
		notFound(w, "Nothing is served at "+r.URL.Path+" on the content listener.")
	}
}

func (h *Handler) serveBoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET and HEAD.")
		return
	}
	start := time.Now()
	client := clientAddr(r)
	root, attrs := h.cfg.DefaultRoot, []any{}
	if h.cfg.ClientRoot != nil {
		var r fs.FS
		r, attrs = h.cfg.ClientRoot(client)
		if r != nil {
			root = r
		}
	}
	log := h.log.With(append([]any{"client", r.RemoteAddr, "path", r.URL.Path}, attrs...)...)

	rel, ok := cleanPath(strings.TrimPrefix(r.URL.Path, BootPrefix))
	if !ok {
		log.Warn("content request refused", "reason", "bad path")
		notFound(w, "No such file.")
		return
	}
	if h.cfg.Shared != nil && h.cfg.SharedDir != "" && strings.HasPrefix(strings.ToLower(rel), strings.ToLower(h.cfg.SharedDir)+"/") {
		root = h.cfg.Shared
		// The script is generated per client (ADR 0034).
		if parts := strings.Split(rel, "/"); h.cfg.Script != nil && len(parts) == 3 && strings.EqualFold(parts[2], "autoexec.ipxe") {
			if body, sattrs, ok := h.cfg.Script(r, client, parts[1]); ok {
				log.Info("content served", append([]any{"method", r.Method, "status", http.StatusOK, "generated", true, "bytes", len(body)}, sattrs...)...)
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				http.ServeContent(w, r, "autoexec.ipxe", time.Now(), strings.NewReader(string(body)))
				return
			}
		}
	}
	if root == nil {
		log.Warn("content request refused", "reason", "no boot content configured")
		notFound(w, "No boot content is configured.")
		return
	}
	h.serveFile(w, r, log, root, rel, start)
}

// serveLinux serves /content/linux/<imageId>/<path> (ADR 0034): the same
// files to every client, whatever its address (the installer's kernel can
// get another address than iPXE had).
func (h *Handler) serveLinux(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET and HEAD.")
		return
	}
	start := time.Now()
	log := h.log.With("client", r.RemoteAddr, "path", r.URL.Path)
	id, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, LinuxPrefix), "/")
	rel, ok := cleanPath(rest)
	if !ok || id == "" || strings.ContainsAny(id, "/\\:.") {
		log.Warn("content request refused", "reason", "bad path")
		notFound(w, "No such file.")
		return
	}
	root, err := h.cfg.Linux(id)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Error("linux image open failed", "err", err)
			problem(w, http.StatusInternalServerError, "internal_error", "")
			return
		}
		log.Info("content request refused", "reason", "no such Linux image")
		notFound(w, "No such Linux image.")
		return
	}
	h.serveFile(w, r, log.With("linux_image", id), root, rel, start)
}

// serveSeed serves /content/seed/<token>/<file>. The token isn't logged.
func (h *Handler) serveSeed(w http.ResponseWriter, r *http.Request) {
	log := h.log.With("client", r.RemoteAddr, "path", SeedPrefix+"…")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET and HEAD.")
		return
	}
	token, file, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, SeedPrefix), "/")
	if !ok || token == "" || strings.ContainsAny(file, "/\\") {
		log.Warn("content request refused", "reason", "bad path")
		notFound(w, "No such file.")
		return
	}
	body, status, attrs := h.cfg.Seed(r, token, file)
	log = log.With(attrs...)
	if status != http.StatusOK {
		log.Info("content request refused", "status", status)
		if status == http.StatusNotFound {
			notFound(w, "No such file.")
		} else {
			problem(w, status, "internal_error", "")
		}
		return
	}
	log.Info("content served", "method", r.Method, "status", status, "bytes", len(body))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, file, time.Now(), strings.NewReader(string(body)))
}

// serveReport takes POST /content/report/<token>/<kind>. The token isn't
// logged.
func (h *Handler) serveReport(w http.ResponseWriter, r *http.Request) {
	log := h.log.With("client", r.RemoteAddr, "path", ReportPrefix+"…")
	token, kind, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, ReportPrefix), "/")
	if !ok || token == "" || kind == "" || strings.Contains(kind, "/") {
		log.Warn("report refused", "reason", "bad path")
		notFound(w, "No such report.")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST.")
		return
	}
	status, quiet, attrs := h.cfg.Report(r, token, kind)
	log = log.With(attrs...)
	switch {
	case status < 300 && quiet:
		log.Debug("report taken", "status", status)
	case status < 300:
		log.Info("report taken", "status", status)
	default:
		log.Info("report refused", "status", status)
	}
	if status < 300 {
		w.WriteHeader(status)
		return
	}
	problem(w, status, "report_refused", http.StatusText(status))
}

// serveFile serves rel from root.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, log *slog.Logger, root fs.FS, rel string, start time.Time) {
	f, err := root.Open(rel)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrInvalid) {
			log.Error("content open failed", "err", err)
			problem(w, http.StatusInternalServerError, "internal_error", "")
			return
		}
		log.Info("content request refused", "reason", "not found")
		notFound(w, "No such file.")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		log.Info("content request refused", "reason", "not a file")
		notFound(w, "No such file.")
		return
	}

	rs, ok := f.(io.ReadSeeker)
	if !ok {
		log.Error("content file not seekable", "type", fmt.Sprintf("%T", f))
		problem(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	if h, ok := f.(interface{ SHA256() string }); ok {
		// Stored files never change, so their hash is a strong validator.
		w.Header().Set("ETag", `"`+h.SHA256()+`"`)
	}
	rec := &counter{ResponseWriter: w, status: http.StatusOK}
	http.ServeContent(rec, r, fi.Name(), fi.ModTime(), rs)
	elapsed := time.Since(start)
	mbps := 0.0
	if s := elapsed.Seconds(); s > 0 {
		mbps = float64(rec.n) / s / 1e6
	}
	log.Info("content served", "method", r.Method, "status", rec.status, "range", r.Header.Get("Range"),
		"size", fi.Size(), "bytes", rec.n, "duration_ms", elapsed.Milliseconds(),
		"mb_per_s", strconv.FormatFloat(mbps, 'f', 1, 64))
}

// cleanPath turns the URL path after BootPrefix into a path inside the
// content root. "..", empty and drive or stream names (":") are refused.
func cleanPath(p string) (string, bool) {
	if p == "" || strings.ContainsAny(p, "\\:\x00") {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", false
		}
	}
	c := path.Clean(p)
	if c == "." || strings.HasPrefix(c, "/") {
		return "", false
	}
	return c, true
}

func clientAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// counter records the status and counts body bytes.
type counter struct {
	http.ResponseWriter
	status int
	n      int64
}

func (c *counter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *counter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

// ReadFrom keeps the sendfile path (the ResponseWriter's own ReadFrom, used
// by io.Copy) while counting bytes.
func (c *counter) ReadFrom(r io.Reader) (int64, error) {
	n, err := io.Copy(c.ResponseWriter, r)
	c.n += n
	return n, err
}

var _ io.ReaderFrom = (*counter)(nil)

func notFound(w http.ResponseWriter, detail string) {
	problem(w, http.StatusNotFound, "not_found", detail)
}

func problem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"type":"about:blank","title":%q,"status":%d,"code":%q,"detail":%q}`+"\n",
		http.StatusText(status), status, code, detail)
}
