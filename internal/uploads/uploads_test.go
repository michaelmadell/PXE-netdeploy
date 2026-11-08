package uploads

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

type harness struct {
	t       *testing.T
	st      *store.Store
	content *cas.Store
	m       *Manager
	srv     *httptest.Server
	now     time.Time
	reserve int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &harness{t: t, st: st, now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	h.content, err = cas.Open(ctx, cas.Options{Root: filepath.Join(dir, "content"), Store: st, Reserve: func() int64 { return h.reserve }})
	if err != nil {
		t.Fatal(err)
	}
	h.start()
	return h
}

func (h *harness) start() {
	h.t.Helper()
	if h.srv != nil {
		h.srv.Close()
	}
	h.m = New(Options{Store: h.st, Content: h.content, Now: func() time.Time { return h.now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	h.m.checkpointEvery = 16
	if err := h.m.Start(h.t.Context()); err != nil {
		h.t.Fatal(err)
	}
	h.srv = httptest.NewServer(h.m)
	h.t.Cleanup(h.srv.Close)
}

func (h *harness) do(method, path string, body []byte, headers ...string) *http.Response {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, bytes.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func (h *harness) create(length int, meta string) string {
	h.t.Helper()
	res := h.do("POST", Prefix, nil, "Tus-Resumable", "1.0.0", "Upload-Length", strconv.Itoa(length), "Upload-Metadata", meta)
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("create: %d", res.StatusCode)
	}
	return res.Header.Get("Location")
}

func (h *harness) patch(loc string, off int, data []byte) *http.Response {
	return h.do("PATCH", loc, data, "Tus-Resumable", "1.0.0", "Upload-Offset", strconv.Itoa(off),
		"Content-Type", "application/offset+octet-stream")
}

func (h *harness) offset(loc string) int {
	h.t.Helper()
	res := h.do("HEAD", loc, nil, "Tus-Resumable", "1.0.0")
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "no-store" {
		h.t.Fatalf("head: %d %v", res.StatusCode, res.Header)
	}
	n, _ := strconv.Atoi(res.Header.Get("Upload-Offset"))
	return n
}

func (h *harness) upload(loc string) store.Upload {
	h.t.Helper()
	u, err := h.st.Upload(context.Background(), filepath.Base(loc))
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

func data(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestFullUpload(t *testing.T) {
	h := newHarness(t)
	res := h.do("OPTIONS", Prefix, nil)
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Tus-Max-Size") != strconv.FormatInt(MaxSize, 10) ||
		!strings.Contains(res.Header.Get("Tus-Extension"), "termination") || res.Header.Get("Tus-Resumable") != "1.0.0" {
		t.Fatalf("options: %d %v", res.StatusCode, res.Header)
	}
	payload := data(100)
	loc := h.create(len(payload), "filename V2luMTEuaXNv,filetype YXBwbGljYXRpb24veC1pc285NjYwLWltYWdl")
	if !strings.HasPrefix(loc, Prefix+"/") || h.offset(loc) != 0 {
		t.Fatalf("location %q", loc)
	}
	res = h.patch(loc, 0, payload[:37])
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Upload-Offset") != "37" || res.Header.Get("Upload-Expires") == "" {
		t.Fatalf("patch 1: %d %v", res.StatusCode, res.Header)
	}
	if h.offset(loc) != 37 {
		t.Fatal("offset after patch 1")
	}
	if res = h.patch(loc, 37, payload[37:]); res.StatusCode != http.StatusNoContent || res.Header.Get("Upload-Offset") != "100" {
		t.Fatalf("patch 2: %d", res.StatusCode)
	}
	u := h.upload(loc)
	if !u.Complete() || *u.SHA256 != sum(payload) || *u.Filename != "Win11.iso" || *u.Filetype != "application/x-iso9660-image" {
		t.Fatalf("complete upload: %+v", u)
	}
	p, _ := h.m.Path(u.ID)
	if b, _ := os.ReadFile(p); !bytes.Equal(b, payload) {
		t.Error("file contents differ")
	}

	// JSON view.
	r2, _ := http.Get(h.srv.URL + loc)
	var got map[string]any
	json.NewDecoder(r2.Body).Decode(&got)
	r2.Body.Close()
	if got["status"] != "complete" || got["sha256"] != sum(payload) || got["imported"] != false || got["expiresAt"] == nil {
		t.Errorf("GET: %v", got)
	}

	if res := h.do("DELETE", loc, nil, "Tus-Resumable", "1.0.0"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if res := h.do("HEAD", loc, nil, "Tus-Resumable", "1.0.0"); res.StatusCode != http.StatusNotFound {
		t.Errorf("head after delete: %d", res.StatusCode)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("file kept after delete: %v", err)
	}
}

func TestRequestErrors(t *testing.T) {
	h := newHarness(t)
	loc := h.create(10, "")
	cases := []struct {
		name   string
		res    *http.Response
		status int
	}{
		{"no tus header", h.do("POST", Prefix, nil, "Upload-Length", "10"), http.StatusPreconditionFailed},
		{"too large", h.do("POST", Prefix, nil, "Tus-Resumable", "1.0.0", "Upload-Length", strconv.FormatInt(MaxSize+1, 10)), http.StatusRequestEntityTooLarge},
		{"deferred", h.do("POST", Prefix, nil, "Tus-Resumable", "1.0.0", "Upload-Defer-Length", "1"), http.StatusBadRequest},
		{"bad metadata", h.do("POST", Prefix, nil, "Tus-Resumable", "1.0.0", "Upload-Length", "5", "Upload-Metadata", "filename !!!"), http.StatusBadRequest},
		{"content type", h.do("PATCH", loc, []byte("x"), "Tus-Resumable", "1.0.0", "Upload-Offset", "0", "Content-Type", "text/plain"), http.StatusUnsupportedMediaType},
		{"offset", h.patch(loc, 3, []byte("x")), http.StatusConflict},
		{"too long", h.patch(loc, 0, data(11)), http.StatusBadRequest},
		{"unknown", h.do("HEAD", Prefix+"/01JZZZZZZZZZZZZZZZZZZZZZZZ", nil, "Tus-Resumable", "1.0.0"), http.StatusNotFound},
		{"bad id", h.do("GET", Prefix+"/..%2F..%2Fx", nil), http.StatusNotFound},
	}
	for _, c := range cases {
		if c.res.StatusCode != c.status {
			t.Errorf("%s: %d, want %d", c.name, c.res.StatusCode, c.status)
		}
	}
	if c := cases[0].res; c.Header.Get("Tus-Version") != "1.0.0" {
		t.Errorf("412 without Tus-Version")
	}
	// The overlong PATCH kept the bytes up to the length.
	if u := h.upload(loc); !u.Complete() {
		t.Errorf("overlong patch: %+v", u)
	}
}

func TestInsufficientStorage(t *testing.T) {
	h := newHarness(t)
	h.reserve = 1 << 62
	if res := h.do("POST", Prefix, nil, "Tus-Resumable", "1.0.0", "Upload-Length", "10"); res.StatusCode != http.StatusInsufficientStorage {
		t.Errorf("create: %d", res.StatusCode)
	}
}

// rawPatch sends a PATCH whose body is cut short and leaves the connection
// open; close it to simulate a drop.
func (h *harness) rawPatch(loc string, off, length int, body []byte) net.Conn {
	h.t.Helper()
	c, err := net.Dial("tcp", h.srv.Listener.Addr().String())
	if err != nil {
		h.t.Fatal(err)
	}
	fmt.Fprintf(c, "PATCH %s HTTP/1.1\r\nHost: x\r\nTus-Resumable: 1.0.0\r\nUpload-Offset: %d\r\n"+
		"Content-Type: application/offset+octet-stream\r\nContent-Length: %d\r\n\r\n", loc, off, length)
	c.Write(body)
	return c
}

func (h *harness) waitOffset(loc string, want int) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.upload(loc).Offset != int64(want) {
		if time.Now().After(deadline) {
			h.t.Fatalf("offset %d, want %d", h.upload(loc).Offset, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDroppedConnectionKeepsBytes(t *testing.T) {
	h := newHarness(t)
	payload := data(100)
	loc := h.create(len(payload), "")
	c := h.rawPatch(loc, 0, len(payload), payload[:60])
	time.Sleep(50 * time.Millisecond)
	c.Close()
	h.waitOffset(loc, 60)
	if res := h.patch(loc, 60, payload[60:]); res.StatusCode != http.StatusNoContent {
		t.Fatalf("resume: %d", res.StatusCode)
	}
	if u := h.upload(loc); *u.SHA256 != sum(payload) {
		t.Error("hash after resume differs")
	}
}

// A new request stops a PATCH whose connection is still open but idle.
func TestNewRequestTakesOver(t *testing.T) {
	h := newHarness(t)
	payload := data(100)
	loc := h.create(len(payload), "")
	c := h.rawPatch(loc, 0, len(payload), payload[:50])
	defer c.Close()
	time.Sleep(50 * time.Millisecond)

	done := make(chan int, 1)
	go func() { done <- h.offset(loc) }()
	select {
	case off := <-done:
		if off != 50 {
			t.Fatalf("HEAD offset %d, want 50", off)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HEAD blocked behind the idle PATCH")
	}
	// The old request got an answer (and its bytes were kept).
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if line, _ := bufio.NewReader(c).ReadString('\n'); !strings.Contains(line, "400") {
		t.Errorf("old request answered %q", line)
	}
	if res := h.patch(loc, 50, payload[50:]); res.StatusCode != http.StatusNoContent {
		t.Fatalf("resume: %d", res.StatusCode)
	}
	if u := h.upload(loc); *u.SHA256 != sum(payload) {
		t.Error("hash after takeover differs")
	}
}

// After a crash, bytes past the last checkpoint are dropped and orphan
// files removed.
func TestRecoveryTruncatesToCheckpoint(t *testing.T) {
	h := newHarness(t)
	loc := h.create(100, "")
	h.patch(loc, 0, data(40))
	p, _ := h.m.Path(filepath.Base(loc))
	f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	f.Write([]byte("bytes the crash didn't record"))
	f.Close()
	orphan := filepath.Join(h.content.UploadDir(), "01JORPHANORPHANORPHANORPHA")
	os.WriteFile(orphan, []byte("x"), 0o600)

	h.start()
	if fi, _ := os.Stat(p); fi.Size() != 40 {
		t.Errorf("file is %d bytes, want 40", fi.Size())
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan kept: %v", err)
	}
	if h.offset(loc) != 40 {
		t.Error("offset after restart")
	}
}

func TestExpiryAndImportLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	stale := h.create(10, "")
	kept := h.create(10, "")
	h.patch(kept, 0, data(10))
	keptID := filepath.Base(kept)

	// An import claims it, succeeds: kept until deleted.
	job, err := h.st.CreateJobForUpload(ctx, keptID, store.NewJob{Kind: "image.import"})
	if err != nil {
		t.Fatal(err)
	}
	if res := h.do("DELETE", kept, nil, "Tus-Resumable", "1.0.0"); res.StatusCode != http.StatusConflict {
		t.Errorf("delete in use: %d", res.StatusCode)
	}
	h.st.StartJob(ctx, job.ID)
	h.st.FinishJob(ctx, job.ID, store.JobOutcome{Status: store.JobSucceeded})
	if u, err := h.m.Release(ctx, keptID, job.ID, true); err != nil || !u.Imported || u.ExpiresAt != nil {
		t.Fatalf("release: %+v %v", u, err)
	}

	h.now = h.now.Add(25 * time.Hour)
	h.m.expire(ctx)
	if res := h.do("HEAD", stale, nil, "Tus-Resumable", "1.0.0"); res.StatusCode != http.StatusNotFound {
		t.Errorf("stale upload not expired: %d", res.StatusCode)
	}
	if p, _ := h.m.Path(filepath.Base(stale)); fileExists(p) {
		t.Error("stale file kept")
	}
	if h.offset(kept) != 10 {
		t.Error("imported upload expired")
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
