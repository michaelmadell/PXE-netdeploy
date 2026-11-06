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
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BootPrefix is where a client's boot content is served.
const BootPrefix = "/content/boot/"

// AgentPrefix is the agent API, forwarded to the API handler.
const AgentPrefix = "/api/v1/agent/"

// Config configures a Handler.
type Config struct {
	// DefaultRoot serves clients ClientRoot doesn't place ("" or no
	// ClientRoot). Empty: no boot content (404).
	DefaultRoot string
	// ClientRoot picks a client's content directory and log attributes, as
	// for TFTP (settings.Service.clientRoot).
	ClientRoot func(client netip.Addr) (dir string, attrs []any)
	// Agent handles AgentPrefix. Nil: 404.
	Agent  http.Handler
	Logger *slog.Logger
}

// Handler serves one interface's content listener.
type Handler struct {
	cfg Config
	log *slog.Logger

	mu    sync.Mutex
	roots map[string]*os.Root
}

// New returns a Handler.
func New(cfg Config) *Handler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Handler{cfg: cfg, log: cfg.Logger.With("svc", "content"), roots: map[string]*os.Root{}}
}

// Close releases opened content directories.
func (h *Handler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var errs []error
	for _, r := range h.roots {
		errs = append(errs, r.Close())
	}
	h.roots = map[string]*os.Root{}
	return errors.Join(errs...)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, AgentPrefix) && h.cfg.Agent != nil:
		h.cfg.Agent.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, BootPrefix):
		h.serveBoot(w, r)
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
	dir, attrs := h.cfg.DefaultRoot, []any{}
	if h.cfg.ClientRoot != nil {
		var d string
		d, attrs = h.cfg.ClientRoot(client)
		if d != "" {
			dir = d
		}
	}
	log := h.log.With(append([]any{"client", r.RemoteAddr, "path", r.URL.Path}, attrs...)...)

	rel, ok := cleanPath(strings.TrimPrefix(r.URL.Path, BootPrefix))
	if !ok {
		log.Warn("content request refused", "reason", "bad path")
		notFound(w, "No such file.")
		return
	}
	if dir == "" {
		log.Warn("content request refused", "reason", "no boot content configured")
		notFound(w, "No boot content is configured.")
		return
	}
	root, err := h.root(dir)
	if err != nil {
		log.Error("content directory unavailable", "dir", dir, "err", err)
		problem(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	f, err := root.Open(rel)
	if err != nil {
		level := slog.LevelWarn
		if errors.Is(err, fs.ErrNotExist) {
			level = slog.LevelInfo
		}
		log.Log(r.Context(), level, "content request refused", "reason", "not found")
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

	rec := &counter{ResponseWriter: w, status: http.StatusOK}
	http.ServeContent(rec, r, fi.Name(), fi.ModTime(), f)
	elapsed := time.Since(start)
	mbps := 0.0
	if s := elapsed.Seconds(); s > 0 {
		mbps = float64(rec.n) / s / 1e6
	}
	log.Info("content served", "method", r.Method, "status", rec.status, "range", r.Header.Get("Range"),
		"size", fi.Size(), "bytes", rec.n, "duration_ms", elapsed.Milliseconds(),
		"mb_per_s", strconv.FormatFloat(mbps, 'f', 1, 64))
}

// root returns the opened directory, caching it.
func (h *Handler) root(dir string) (*os.Root, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r, ok := h.roots[dir]; ok {
		return r, nil
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	h.roots[dir] = r
	return r, nil
}

// cleanPath turns the URL path after BootPrefix into a path inside the
// content root. "..", empty and drive or stream names (":") are refused;
// os.Root also refuses escapes, so this is belt and braces.
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
