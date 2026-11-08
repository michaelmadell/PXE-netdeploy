package uploads

import (
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Prefix is where the handler is mounted.
const Prefix = "/api/v1/uploads"

const tusVersion = "1.0.0"

// ServeHTTP serves Prefix and Prefix/{id}.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Tus-Resumable", tusVersion)
	rest, ok := strings.CutPrefix(r.URL.Path, Prefix)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		problem(w, r, http.StatusNotFound, "not_found", "Not found", "")
		return
	}
	id := strings.TrimPrefix(rest, "/")
	if id == "" {
		switch r.Method {
		case http.MethodOptions:
			w.Header().Set("Tus-Version", tusVersion)
			w.Header().Set("Tus-Extension", "creation,termination,expiration")
			w.Header().Set("Tus-Max-Size", strconv.FormatInt(MaxSize, 10))
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			m.list(w, r)
		case http.MethodPost:
			if m.tusHeader(w, r) {
				m.create(w, r)
			}
		default:
			w.Header().Set("Allow", "OPTIONS, GET, POST")
			problem(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		}
		return
	}
	if !idPattern.MatchString(id) {
		problem(w, r, http.StatusNotFound, "not_found", "Not found", "No such upload.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		m.get(w, r, id)
	case http.MethodHead:
		if m.tusHeader(w, r) {
			m.head(w, r, id)
		}
	case http.MethodPatch:
		if m.tusHeader(w, r) {
			m.patch(w, r, id)
		}
	case http.MethodDelete:
		if m.tusHeader(w, r) {
			m.delete(w, r, id)
		}
	default:
		w.Header().Set("Allow", "GET, HEAD, PATCH, DELETE")
		problem(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
	}
}

// tusHeader checks Tus-Resumable (412 with Tus-Version if wrong).
func (m *Manager) tusHeader(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Tus-Resumable") == tusVersion {
		return true
	}
	w.Header().Set("Tus-Version", tusVersion)
	problem(w, r, http.StatusPreconditionFailed, "tus_version_unsupported", "Unsupported tus version",
		"Send Tus-Resumable: 1.0.0.")
	return false
}

func (m *Manager) expires(u store.Upload, w http.ResponseWriter) {
	if u.ExpiresAt != nil {
		w.Header().Set("Upload-Expires", u.ExpiresAt.UTC().Format(http.TimeFormat))
	}
}

func (m *Manager) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Header.Get("Upload-Defer-Length") != "" {
		problem(w, r, http.StatusBadRequest, "defer_length_unsupported", "Deferred length not supported",
			"Send Upload-Length when creating the upload.")
		return
	}
	length, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil || length < 1 {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Invalid Upload-Length", "Upload-Length must be a positive integer.")
		return
	}
	if length > MaxSize {
		problem(w, r, http.StatusRequestEntityTooLarge, "upload_too_large", "Upload too large",
			fmt.Sprintf("Uploads are limited to %d bytes (64 GiB).", MaxSize))
		return
	}
	meta, err := parseMetadata(r.Header.Get("Upload-Metadata"))
	if err != nil {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Invalid Upload-Metadata", err.Error())
		return
	}
	_, pending, err := m.opts.Store.UploadBytes(ctx, "")
	if err != nil {
		m.internal(w, r, err)
		return
	}
	if err := m.opts.Content.CheckSpace(length + pending); err != nil {
		if errors.Is(err, cas.ErrInsufficientStorage) {
			problem(w, r, http.StatusInsufficientStorage, "insufficient_storage", "Not enough storage", err.Error())
			return
		}
		m.internal(w, r, err)
		return
	}

	state, _ := sha256.New().(encoding.BinaryMarshaler).MarshalBinary()
	n := store.NewUpload{Length: length, HashState: state, ExpiresAt: m.opts.Now().Add(m.opts.Expiry())}
	if v, ok := meta["filename"]; ok {
		n.Filename = &v
	}
	if v, ok := meta["filetype"]; ok {
		n.Filetype = &v
	}
	u, err := m.opts.Store.CreateUpload(ctx, n)
	if err != nil {
		m.internal(w, r, err)
		return
	}
	p, _ := m.Path(u.ID)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		_ = m.opts.Store.DeleteUpload(context.WithoutCancel(ctx), u.ID)
		m.internal(w, r, err)
		return
	}
	m.log.Info("upload created", "upload_id", u.ID, "length", length, "filename", meta["filename"])
	w.Header().Set("Location", Prefix+"/"+u.ID)
	m.expires(u, w)
	w.WriteHeader(http.StatusCreated)
}

// parseMetadata reads tus Upload-Metadata: "key base64value,key2,...".
func parseMetadata(h string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(h) == "" {
		return out, nil
	}
	for _, pair := range strings.Split(h, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(pair), " ")
		if k == "" {
			return nil, errors.New("empty key")
		}
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("value of %q is not base64", k)
		}
		if len(b) > 1024 {
			return nil, fmt.Errorf("value of %q is too long", k)
		}
		out[k] = string(b)
	}
	return out, nil
}

func (m *Manager) head(w http.ResponseWriter, r *http.Request, id string) {
	// Stop a PATCH still writing (a dead connection) so the offset is
	// current.
	release := m.acquire(id, func() {})
	defer release()
	u, err := m.opts.Store.Upload(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		m.log.Error("read upload", "upload_id", id, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Upload-Offset", strconv.FormatInt(u.Offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(u.Length, 10))
	m.expires(u, w)
	w.WriteHeader(http.StatusOK)
}

func (m *Manager) delete(w http.ResponseWriter, r *http.Request, id string) {
	release := m.acquire(id, func() {})
	defer release()
	err := m.opts.Store.DeleteUpload(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		problem(w, r, http.StatusNotFound, "not_found", "Not found", "No such upload.")
		return
	case errors.Is(err, store.ErrUploadInUse):
		problem(w, r, http.StatusConflict, "upload_in_use", "Upload in use",
			"An import using this upload is queued or running; cancel it first.")
		return
	case err != nil:
		m.internal(w, r, err)
		return
	}
	m.removeFile(id)
	m.log.Info("upload deleted", "upload_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) patch(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	if ct := r.Header.Get("Content-Type"); ct != "application/offset+octet-stream" {
		problem(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type",
			"PATCH bodies must be application/offset+octet-stream.")
		return
	}
	off, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || off < 0 {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Invalid Upload-Offset", "Upload-Offset must be a non-negative integer.")
		return
	}

	// A later request for this upload stops this one: unblock the body read.
	var stopped atomic.Bool
	rc := http.NewResponseController(w)
	release := m.acquire(id, func() {
		stopped.Store(true)
		_ = rc.SetReadDeadline(time.Now())
	})
	defer release()

	u, err := m.opts.Store.Upload(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, r, http.StatusNotFound, "not_found", "Not found", "No such upload.")
		return
	}
	if err != nil {
		m.internal(w, r, err)
		return
	}
	if off != u.Offset {
		w.Header().Set("Upload-Offset", strconv.FormatInt(u.Offset, 10))
		problem(w, r, http.StatusConflict, "upload_offset_mismatch", "Offset mismatch",
			fmt.Sprintf("The upload is at offset %d; send HEAD and resume from there.", u.Offset))
		return
	}
	if u.Complete() {
		w.Header().Set("Upload-Offset", strconv.FormatInt(u.Offset, 10))
		m.expires(u, w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	wr, err := m.openWriter(u)
	if err != nil {
		m.internal(w, r, err)
		return
	}
	copyErr := wr.copyFrom(r.Body)
	u2, ckErr := wr.finish()
	log := m.log.With("upload_id", id)
	switch {
	case ckErr != nil:
		log.Error("save upload progress", "offset", wr.offset, "err", ckErr)
		m.internal(w, r, ckErr)
		return
	case errors.Is(copyErr, cas.ErrInsufficientStorage):
		log.Warn("upload stopped: not enough storage", "offset", u2.Offset)
		w.Header().Set("Upload-Offset", strconv.FormatInt(u2.Offset, 10))
		problem(w, r, http.StatusInsufficientStorage, "insufficient_storage", "Not enough storage", copyErr.Error())
		return
	case errors.Is(copyErr, errTooLong):
		problem(w, r, http.StatusBadRequest, "upload_too_long", "Too many bytes",
			"The body goes past Upload-Length; the bytes up to it were kept.")
		return
	case copyErr != nil:
		// Usually the client went away (or a newer request took over);
		// what arrived is saved and HEAD reports it.
		reason := "connection ended"
		if stopped.Load() {
			reason = "superseded by a newer request"
		}
		log.Info("upload paused", "reason", reason, "offset", u2.Offset, "err", copyErr)
		w.Header().Set("Upload-Offset", strconv.FormatInt(u2.Offset, 10))
		problem(w, r, http.StatusBadRequest, "upload_interrupted", "Upload interrupted",
			"The bytes received were kept; send HEAD and resume.")
		return
	}
	if u2.Complete() {
		log.Info("upload complete", "length", u2.Length, "sha256", *u2.SHA256)
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(u2.Offset, 10))
	m.expires(u2, w)
	w.WriteHeader(http.StatusNoContent)
}

var errTooLong = errors.New("body longer than Upload-Length")

// writer appends to an upload, hashing and checkpointing as it goes.
type writer struct {
	m      *Manager
	u      store.Upload
	f      *os.File
	h      hash.Hash
	offset int64
	saved  int64 // offset of the last checkpoint
	space  int64 // bytes since the last space check
}

func (m *Manager) openWriter(u store.Upload) (*writer, error) {
	h := sha256.New()
	if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(u.HashState); err != nil {
		return nil, fmt.Errorf("restore hash state: %w", err)
	}
	p, _ := m.Path(u.ID)
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	// Anything past the checkpoint isn't covered by the hash state.
	if err := f.Truncate(u.Offset); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(u.Offset, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &writer{m: m, u: u, f: f, h: h, offset: u.Offset, saved: u.Offset}, nil
}

func (wr *writer) copyFrom(body io.Reader) error {
	buf := make([]byte, 1<<20)
	for {
		remaining := wr.u.Length - wr.offset
		n, rerr := body.Read(buf[:min(int64(len(buf)), remaining+1)])
		if int64(n) > remaining {
			n = int(remaining)
			rerr = errTooLong
		}
		if n > 0 {
			if _, err := wr.f.Write(buf[:n]); err != nil {
				return err
			}
			wr.h.Write(buf[:n])
			wr.offset += int64(n)
			wr.space += int64(n)
			if wr.offset-wr.saved >= wr.m.checkpointEvery && wr.offset < wr.u.Length {
				if err := wr.checkpoint(); err != nil {
					return err
				}
			}
			if wr.space >= wr.m.spaceCheckEvery {
				wr.space = 0
				if err := wr.m.opts.Content.CheckSpace(0); err != nil {
					return err
				}
			}
		}
		switch {
		case rerr == io.EOF:
			return nil
		case rerr != nil:
			return rerr
		case wr.offset == wr.u.Length:
			// All bytes are here; anything more is an error.
			var one [1]byte
			if k, _ := body.Read(one[:]); k > 0 {
				return errTooLong
			}
			return nil
		}
	}
}

// checkpoint makes the bytes so far durable and records the offset and
// hash state.
func (wr *writer) checkpoint() error {
	if err := wr.f.Sync(); err != nil {
		return err
	}
	state, err := wr.h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return err
	}
	c := store.UploadCheckpoint{Offset: wr.offset, HashState: state, ExpiresAt: wr.m.opts.Now().Add(wr.m.opts.Expiry())}
	if wr.offset == wr.u.Length {
		sum := hex.EncodeToString(wr.h.Sum(nil))
		c.SHA256 = &sum
	}
	if err := wr.m.opts.Store.CheckpointUpload(context.Background(), wr.u.ID, c); err != nil {
		return err
	}
	wr.saved = wr.offset
	return nil
}

// finish saves what arrived and closes the file.
func (wr *writer) finish() (store.Upload, error) {
	var err error
	if wr.offset != wr.saved {
		err = wr.checkpoint()
	}
	if cerr := wr.f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return store.Upload{}, err
	}
	return wr.m.opts.Store.Upload(context.Background(), wr.u.ID)
}

func (m *Manager) get(w http.ResponseWriter, r *http.Request, id string) {
	u, err := m.opts.Store.Upload(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, r, http.StatusNotFound, "not_found", "Not found", "No such upload.")
		return
	}
	if err != nil {
		m.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ToAPI(u))
}

func (m *Manager) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			problem(w, r, http.StatusBadRequest, "invalid_request", "Invalid limit", "limit must be 1..200.")
			return
		}
		limit = n
	}
	list, next, err := m.opts.Store.ListUploads(r.Context(), limit, q.Get("cursor"))
	if err != nil {
		m.internal(w, r, err)
		return
	}
	page := api.UploadPage{Items: make([]api.Upload, len(list))}
	for i, u := range list {
		page.Items[i] = ToAPI(u)
	}
	if next != "" {
		page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, page)
}

// ToAPI converts an upload for the API.
func ToAPI(u store.Upload) api.Upload {
	status := api.UploadStatusReceiving
	if u.Complete() {
		status = api.UploadStatusComplete
	}
	return api.Upload{
		Id: u.ID, Filename: u.Filename, Filetype: u.Filetype, Length: u.Length, Offset: u.Offset,
		Status: status, Sha256: u.SHA256, Imported: u.Imported, InUse: u.InUse, LastJobId: u.JobID,
		ExpiresAt: u.ExpiresAt, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

func (m *Manager) internal(w http.ResponseWriter, r *http.Request, err error) {
	m.log.Error("upload request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	problem(w, r, http.StatusInternalServerError, "internal_error", "Internal error", "")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// problem writes RFC 9457 JSON (no body for HEAD).
func problem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	p := map[string]any{"type": "about:blank", "title": title, "status": status, "code": code}
	if detail != "" {
		p["detail"] = detail
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}
