package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// IdempotencyTTL is how long a response is kept for replay.
const IdempotencyTTL = 24 * time.Hour

// maxIdempotentBody caps the request body hashed and kept in memory; larger
// POSTs (none exist yet; uploads use tus) are refused rather than buffered.
const maxIdempotentBody = 1 << 20

// Idempotency makes API POSTs carrying an Idempotency-Key replayable (spec
// conventions): the first response (below 500) is stored and returned again
// for the same key and body; a different body with the same key gets 422.
// Concurrent requests with the same key wait for the first one. The scope is
// the method and path, so a key only replays on the same operation.
func Idempotency(st *store.Store) func(http.Handler) http.Handler {
	locks := &keyLocks{m: map[string]*keyLock{}}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if r.Method != http.MethodPost || key == "" || !strings.HasPrefix(r.URL.Path, apiBase+"/") {
				next.ServeHTTP(w, r)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, maxIdempotentBody+1))
			if err != nil {
				writeProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "Could not read the request body.")
				return
			}
			if len(body) > maxIdempotentBody {
				writeProblem(w, http.StatusRequestEntityTooLarge, "body_too_large", "Request too large",
					"Requests with Idempotency-Key are limited to 1 MiB.")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			sum := sha256.Sum256(body)
			hash := hex.EncodeToString(sum[:])
			scope := r.Method + " " + r.URL.Path

			unlock := locks.lock(scope + "\x00" + key)
			defer unlock()

			ctx := r.Context()
			prev, err := st.IdempotentResponse(ctx, scope, key)
			switch {
			case err == nil && time.Since(prev.CreatedAt) < IdempotencyTTL:
				if prev.RequestHash != hash {
					writeProblem(w, http.StatusUnprocessableEntity, "idempotency_key_reused", "Idempotency key reused",
						"This Idempotency-Key was used with a different request body.")
					return
				}
				w.Header().Set("Content-Type", prev.ContentType)
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(prev.Status)
				_, _ = w.Write(prev.Body)
				return
			case err != nil && !errors.Is(err, store.ErrNotFound):
				// Without the lookup we can't promise "at most once".
				slog.Error("idempotency lookup failed", "scope", scope, "err", err)
				writeProblem(w, http.StatusInternalServerError, "internal_error", "Internal error", "")
				return
			}

			rec := &capture{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			if rec.status >= 500 {
				return
			}
			err = st.PutIdempotentResponse(context.WithoutCancel(ctx), store.IdempotentResponse{
				Scope: scope, Key: key, RequestHash: hash, Status: rec.status,
				ContentType: rec.Header().Get("Content-Type"), Body: rec.body.Bytes(),
			})
			if err != nil && !errors.Is(err, store.ErrDuplicate) {
				// The response is already sent; a later replay will run the
				// operation again, which its own conflict checks must catch.
				slog.Error("idempotency response not stored", "scope", scope, "err", err)
			}
		})
	}
}

// capture passes the response through and keeps a copy.
type capture struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *capture) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *capture) Write(b []byte) (int, error) {
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}

// keyLocks is a set of mutexes created on demand and dropped when unused.
type keyLocks struct {
	mu sync.Mutex
	m  map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyLocks) lock(key string) (unlock func()) {
	k.mu.Lock()
	l, ok := k.m[key]
	if !ok {
		l = &keyLock{}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// PurgeIdempotency deletes expired responses every interval until ctx ends.
func PurgeIdempotency(ctx context.Context, st *store.Store, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := st.PurgeIdempotentResponses(ctx, time.Now().Add(-IdempotencyTTL))
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("idempotency purge failed", "err", err)
				}
				continue
			}
			if n > 0 {
				slog.Debug("idempotency responses purged", "count", n)
			}
		}
	}
}
