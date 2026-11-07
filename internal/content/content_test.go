package content

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
)

// hashed stands in for a content store tree: its files report a hash.
type hashed struct{ fs.FS }

type hashedFile struct{ *os.File }

func (hashedFile) SHA256() string { return "abc123" }

func (h hashed) Open(name string) (fs.File, error) {
	f, err := h.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return hashedFile{f.(*os.File)}, nil
}

func TestStoredFilesGetETag(t *testing.T) {
	h, _, _ := handler(t, nil)
	w := get(h, http.MethodGet, BootPrefix+"boot/bcd", "10.77.0.140")
	if w.Code != http.StatusOK || w.Body.String() != "client-bcd" || w.Header().Get("ETag") != `"abc123"` {
		t.Fatalf("got %d %q etag %q", w.Code, w.Body.String(), w.Header().Get("ETag"))
	}
	// Default content is a plain directory: no hash, no ETag.
	if w := get(h, http.MethodGet, BootPrefix+"Boot/BCD", "10.77.0.9"); w.Header().Get("ETag") != "" {
		t.Errorf("unexpected ETag %q", w.Header().Get("ETag"))
	}
}

func write(t *testing.T, dir, rel, data string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func handler(t *testing.T, agent http.Handler) (*Handler, string, string) {
	t.Helper()
	def, other := t.TempDir(), t.TempDir()
	write(t, def, "Boot/boot.wim", "0123456789")
	write(t, def, "Boot/BCD", "default-bcd")
	write(t, other, "Boot/BCD", "client-bcd")
	h := New(Config{
		DefaultRoot: cas.DirFS(def),
		ClientRoot: func(ip netip.Addr) (fs.FS, []any) {
			if ip == netip.MustParseAddr("10.77.0.140") {
				return hashed{cas.DirFS(other)}, []any{"boot_session", "dhcp"}
			}
			return nil, []any{"boot_session", "none"}
		},
		Agent:  agent,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return h, def, other
}

func get(h http.Handler, method, path, from string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = from + ":50000"
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestServesClientContent(t *testing.T) {
	h, _, _ := handler(t, nil)
	if w := get(h, "GET", "/content/boot/Boot/BCD", "10.77.0.140"); w.Code != 200 || w.Body.String() != "client-bcd" {
		t.Errorf("client with a session: %d %q", w.Code, w.Body)
	}
	if w := get(h, "GET", "/content/boot/Boot/BCD", "10.77.0.150"); w.Code != 200 || w.Body.String() != "default-bcd" {
		t.Errorf("client without a session: %d %q", w.Code, w.Body)
	}
}

func TestRangeAndHead(t *testing.T) {
	h, _, _ := handler(t, nil)
	w := get(h, "GET", "/content/boot/Boot/boot.wim", "10.77.0.150", "Range", "bytes=2-5")
	if w.Code != http.StatusPartialContent || w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Errorf("range: %d %q %q", w.Code, w.Body, w.Header().Get("Content-Range"))
	}
	w = get(h, "HEAD", "/content/boot/Boot/boot.wim", "10.77.0.150")
	if w.Code != 200 || w.Header().Get("Content-Length") != "10" || w.Body.Len() != 0 || w.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("head: %d len=%q body=%d ranges=%q", w.Code, w.Header().Get("Content-Length"), w.Body.Len(), w.Header().Get("Accept-Ranges"))
	}
}

func TestRefusals(t *testing.T) {
	h, _, _ := handler(t, nil)
	for _, p := range []string{
		"/content/boot/", "/content/boot/Boot", "/content/boot/nope", "/content/boot/../etc/passwd",
		"/content/boot/Boot/..%2F..%2Fx", `/content/boot/Boot\BCD`, "/content/boot/C:/x",
	} {
		if w := get(h, "GET", p, "10.77.0.150"); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
	if w := get(h, "POST", "/content/boot/Boot/BCD", "10.77.0.150"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
}

// ADR 0009: no admin operation is reachable on the content listener; agent
// operations reach the agent handler.
func TestOnlyAgentAPIIsReachable(t *testing.T) {
	agent := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h, _, _ := handler(t, agent)
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for p, item := range spec.Paths.Map() {
		url := "/api/v1" + strings.NewReplacer("{", "", "}", "").Replace(p)
		for method := range item.Operations() {
			w := get(h, method, url, "10.77.0.150")
			isAgent := strings.HasPrefix(p, "/agent/")
			switch {
			case isAgent && w.Code != http.StatusTeapot:
				t.Errorf("%s %s: %d, want the agent handler", method, url, w.Code)
			case !isAgent && w.Code != http.StatusNotFound:
				t.Errorf("%s %s: %d, want 404 (admin API on the content listener)", method, url, w.Code)
			}
			n++
		}
	}
	if n < 50 {
		t.Fatalf("only %d operations checked; spec not loaded?", n)
	}
	for _, p := range []string{"/", "/index.html", "/api/v1/", "/api/v1/agentx"} {
		if w := get(h, "GET", p, "10.77.0.150"); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
}

func TestNoBootContent(t *testing.T) {
	h := New(Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if w := get(h, "GET", "/content/boot/Boot/BCD", "10.77.0.150"); w.Code != http.StatusNotFound {
		t.Errorf("no content configured: %d", w.Code)
	}
}
