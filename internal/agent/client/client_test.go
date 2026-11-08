package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agent/wire"
)

func TestDownloadResumesAndVerifies(t *testing.T) {
	data := strings.Repeat("0123456789", 10000)
	sum := sha256.Sum256([]byte(data))
	sha := hex.EncodeToString(sum[:])
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		if fail && r.Header.Get("Range") == "" {
			// The first attempt stops half way.
			fail = false
			w.Header().Set("Content-Length", "100000")
			w.Write([]byte(data[:50000]))
			if hj, ok := w.(http.Hijacker); ok {
				c, _, _ := hj.Hijack()
				c.Close()
			}
			return
		}
		http.ServeContent(w, r, "f", time.Time{}, strings.NewReader(data))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	c.SetToken("tok")
	c.Sleep = func(time.Duration) {}
	dest := filepath.Join(t.TempDir(), "f")
	var last int64
	if err := c.Download(context.Background(), wire.File{ContentURL: "/api/v1/agent/files/" + sha, SHA256: sha, SizeBytes: int64(len(data))}, dest,
		func(done, total int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != data || last != int64(len(data)) {
		t.Fatalf("downloaded %d bytes, progress %d", len(b), last)
	}
	// A wrong hash fails (after retries) and the bad file is removed.
	c.Retries = 1
	bad := filepath.Join(t.TempDir(), "g")
	if err := c.Download(context.Background(), wire.File{ContentURL: "/x", SHA256: strings.Repeat("0", 64), SizeBytes: int64(len(data))}, bad, nil); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	// 4xx isn't retried.
	c.SetToken("wrong")
	if err := c.Download(context.Background(), wire.File{ContentURL: "/x", SHA256: sha}, filepath.Join(t.TempDir(), "h"), nil); err == nil {
		t.Fatal("401 accepted")
	}
}

func TestAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(429)
		w.Write([]byte(`{"title":"Too many attempts","code":"too_many_attempts","detail":"Try again in 17 s."}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	_, err := c.Unlock(context.Background(), "c", nil)
	ae, ok := err.(*APIError)
	if !ok || ae.Status != 429 || ae.Code != "too_many_attempts" || ae.RetryAfter != 17*time.Second {
		t.Fatalf("%#v", err)
	}
}
