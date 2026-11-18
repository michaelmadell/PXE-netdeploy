package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers/legacy"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Admin authentication (ADR 0029): session cookies for people, bearer API
// tokens for automation.

const (
	sessionIdle    = 8 * time.Hour
	sessionMax     = 7 * 24 * time.Hour
	touchEvery     = time.Minute
	cookieName     = "__Host-netdeploy-session"
	cookieNameDev  = "netdeploy-session"
	apiTokenPrefix = "ndt_"
)

// Principal is who an admin request is from.
type Principal struct {
	Via         string // "cookie" or "token"
	UserID      string
	Username    string
	SessionHash string    // cookie
	ExpiresAt   time.Time // cookie: the absolute end
	TokenID     string    // token
	TokenName   string
}

type authKey int

const (
	ctxPrincipal authKey = iota
	ctxLogInfo
)

func principalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxPrincipal).(Principal)
	return p, ok
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxPrincipal, p)
}

// authState is the authentication's memory.
type authState struct {
	dev   bool
	setup *SetupCode
	lim   attempts
	now   func() time.Time
}

func (a *authState) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (s *Server) cookieName() string {
	if s.auth.dev {
		return cookieNameDev
	}
	return cookieName
}

func (s *Server) sessionCookie(value string, expires time.Time) *http.Cookie {
	c := &http.Cookie{Name: s.cookieName(), Value: value, Path: "/", HttpOnly: true, Secure: !s.auth.dev, SameSite: http.SameSiteStrictMode}
	if value == "" {
		c.MaxAge = -1
	} else {
		c.Expires = expires
	}
	return c
}

// Authenticate is the admin listener's authentication middleware. It finds
// the caller (a bearer API token, else the session cookie), checks the
// Origin of cookie requests that change something, and answers 401 for
// admin operations without a caller. Open operations are those the spec
// marks `security: []`, plus everything outside /api/v1/ (the UI) and the
// agent API, which has its own tokens.
func (s *Server) Authenticate(spec *openapi3.T) (func(http.Handler) http.Handler, error) {
	doc := *spec
	doc.Servers = nil
	router, err := legacy.NewRouter(&doc)
	if err != nil {
		return nil, fmt.Errorf("authenticate: %w", err)
	}
	open := func(r *http.Request, path string) bool {
		rr := r.Clone(r.Context())
		rr.URL.Path, rr.URL.RawPath = path, ""
		route, _, err := router.FindRoute(rr)
		if err != nil || route.Operation == nil {
			return false
		}
		sec := route.Operation.Security
		return sec != nil && len(*sec) == 0
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path, isAPI := strings.CutPrefix(r.URL.Path, apiBase)
			// The agent API has its own tokens. Only a clean path counts, so
			// /agent/../users can't skip authentication.
			if !isAPI || (strings.HasPrefix(path, "/agent/") && pathpkg.Clean(path) == path) {
				next.ServeHTTP(w, r)
				return
			}
			p, err := s.resolve(r)
			if err != nil {
				slog.Error("authentication lookup failed", "err", err)
				writeProblem(w, http.StatusInternalServerError, "internal_error", "Internal error", "")
				return
			}
			if p != nil {
				if li, ok := r.Context().Value(ctxLogInfo).(*logInfo); ok {
					li.who = p.Via + ":" + p.Username
					if p.Via == "token" {
						li.who += "/" + p.TokenName
					}
				}
				r = r.WithContext(withPrincipal(r.Context(), *p))
			}
			unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
			isOpen := open(r, path)
			// CSRF: a cookie request that changes something must come from
			// this listener's own pages; so must sign-in and setup when a
			// browser says where it comes from.
			if unsafe && ((p != nil && p.Via == "cookie") || (isOpen && r.Header.Get("Origin") != "")) && !s.originOK(r) {
				writeProblem(w, http.StatusForbidden, "bad_origin", "Request from another site",
					"Requests that change something must come from this server's own web UI.")
				return
			}
			if p == nil && !isOpen {
				writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Sign in required",
					"Sign in to the web UI, or send an API token (Authorization: Bearer ndt_…).")
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// originOK: the Origin header names the host the request was sent to (or,
// with -dev, the host the Vite proxy was asked for).
func (s *Server) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return s.auth.dev && strings.EqualFold(u.Host, r.Header.Get("X-Forwarded-Host"))
}

// resolve finds the caller: an API token in Authorization, else the session
// cookie. Unknown, expired or idle credentials count as none.
func (s *Server) resolve(r *http.Request) (*Principal, error) {
	if s.store == nil {
		return nil, nil
	}
	ctx := r.Context()
	now := s.auth.clock()
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		tok = strings.TrimSpace(tok)
		if !strings.HasPrefix(tok, apiTokenPrefix) {
			return nil, nil
		}
		t, err := s.store.APITokenByHash(ctx, tokenHash(tok))
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
			return nil, nil
		}
		if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= touchEvery {
			if err := s.store.TouchAPIToken(ctx, t.ID); err != nil {
				slog.Warn("token last use not recorded", "err", err)
			}
		}
		return &Principal{Via: "token", UserID: t.UserID, Username: t.Username, TokenID: t.ID, TokenName: t.Name}, nil
	}
	c, err := r.Cookie(s.cookieName())
	if err != nil || c.Value == "" {
		return nil, nil
	}
	hash := tokenHash(c.Value)
	x, err := s.store.AdminSessionByHash(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !now.Before(x.ExpiresAt) || now.Sub(x.LastSeenAt) >= sessionIdle {
		_ = s.store.DeleteAdminSession(ctx, hash)
		return nil, nil
	}
	if now.Sub(x.LastSeenAt) >= touchEvery {
		if err := s.store.TouchAdminSession(ctx, hash); err != nil {
			slog.Warn("session activity not recorded", "err", err)
		}
	}
	return &Principal{Via: "cookie", UserID: x.UserID, Username: x.Username, SessionHash: hash, ExpiresAt: x.ExpiresAt}, nil
}

// ---------------------------------------------------------------- attempts

// attempts rate-limits failures per key (ADR 0029, as for /agent/unlock):
// 5 free, then a doubling back-off from 5 s up to 5 minutes.
type attempts struct {
	mu       sync.Mutex
	failures map[string]*limit
}

func (a *attempts) blocked(now time.Time, keys ...string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	var wait time.Duration
	for _, k := range keys {
		if l := a.failures[k]; l != nil {
			wait = max(wait, l.until.Sub(now))
		}
	}
	return max(wait, 0)
}

func (a *attempts) fail(now time.Time, keys ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failures == nil {
		a.failures = map[string]*limit{}
	}
	for _, k := range keys {
		l := a.failures[k]
		if l == nil {
			l = &limit{}
			a.failures[k] = l
		}
		l.failures++
		if n := l.failures - freeFailures; n > 0 {
			d := 5 * time.Second << min(n-1, 10)
			l.until = now.Add(min(d, maxBackoff))
		}
	}
}

func (a *attempts) succeed(keys ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range keys {
		delete(a.failures, k)
	}
}

func tooMany(wait time.Duration) api.TooManyAttemptsApplicationProblemPlusJSONResponse {
	secs := max(int((wait+time.Second-1)/time.Second), 1)
	return api.TooManyAttemptsApplicationProblemPlusJSONResponse{
		Body:    newProblem(http.StatusTooManyRequests, "too_many_attempts", "Too many attempts", fmt.Sprintf("Try again in %d s.", secs), nil),
		Headers: api.TooManyAttemptsResponseHeaders{RetryAfter: secs},
	}
}

// ---------------------------------------------------------------- helpers

func newAPIToken() (string, string) {
	b := make([]byte, 32)
	rand.Read(b)
	tok := apiTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return tok, tokenHash(tok)
}

func userToAPI(u store.User) api.User {
	return api.User{Id: u.ID, Username: u.Username, CreatedAt: u.CreatedAt, LastSignInAt: u.LastSignInAt, PasswordChangedAt: u.PasswordChangedAt}
}

func tokenToAPI(t store.APIToken) api.ApiToken {
	return api.ApiToken{Id: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, CreatedBy: t.Username, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt}
}

// requireSession: the operation is for people signed in to the UI, not API
// tokens (ADR 0029).
func requireSession(ctx context.Context) (Principal, *api.Problem) {
	p, ok := principalFrom(ctx)
	if !ok {
		pr := newProblem(http.StatusUnauthorized, "unauthenticated", "Sign in required", "", nil)
		return p, &pr
	}
	if p.Via != "cookie" {
		pr := newProblem(http.StatusForbidden, "not_for_api_tokens", "Not available to API tokens",
			"Accounts, tokens and passwords are managed by people signed in to the web UI.", nil)
		return p, &pr
	}
	return p, nil
}

// dummyHash makes a sign-in for an unknown username take as long as one for
// a known username with a wrong password.
var dummyHash = sync.OnceValue(func() string {
	h, _ := settings.HashPassword("netdeploy-dummy-password")
	return h
})

// startSession creates a session for userID and returns its cookie and the
// Session to answer with.
func (s *Server) startSession(ctx context.Context, u store.User) (*http.Cookie, api.Session, error) {
	tok, hash := newToken()
	exp := s.auth.clock().Add(sessionMax).UTC().Truncate(time.Millisecond)
	if err := s.store.CreateAdminSession(ctx, hash, u.ID, exp); err != nil {
		return nil, api.Session{}, err
	}
	if err := s.store.RecordSignIn(ctx, u.ID); err != nil {
		slog.Warn("sign-in time not recorded", "err", err)
	}
	if fresh, err := s.store.UserByID(ctx, u.ID); err == nil {
		u = fresh
	}
	user := userToAPI(u)
	via := api.SessionViaCookie
	return s.sessionCookie(tok, exp), api.Session{SignedIn: true, User: &user, Via: &via, ExpiresAt: &exp}, nil
}

// sessionResponse answers sign-in, setup and sign-out with Set-Cookie.
type sessionResponse struct {
	status  int
	session *api.Session
	cookie  *http.Cookie
}

func (r sessionResponse) visit(w http.ResponseWriter) error {
	http.SetCookie(w, r.cookie)
	w.Header().Set("Cache-Control", "no-store")
	if r.session == nil {
		w.WriteHeader(r.status)
		return nil
	}
	if r.status == http.StatusOK {
		return api.SignIn200JSONResponse(*r.session).VisitSignInResponse(w)
	}
	return api.SetUp201JSONResponse(*r.session).VisitSetUpResponse(w)
}

func (r sessionResponse) VisitSignInResponse(w http.ResponseWriter) error  { return r.visit(w) }
func (r sessionResponse) VisitSetUpResponse(w http.ResponseWriter) error   { return r.visit(w) }
func (r sessionResponse) VisitSignOutResponse(w http.ResponseWriter) error { return r.visit(w) }

// logInfo is filled in by the authentication for the request log.
type logInfo struct{ who string }

func withLogInfo(ctx context.Context) (context.Context, *logInfo) {
	li := &logInfo{}
	return context.WithValue(ctx, ctxLogInfo, li), li
}

// PurgeSessions deletes ended admin sessions every interval.
func PurgeSessions(ctx context.Context, st *store.Store, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := st.PurgeAdminSessions(ctx, sessionIdle); err != nil && ctx.Err() == nil {
				slog.Warn("session purge failed", "err", err)
			} else if n > 0 {
				slog.Debug("admin sessions purged", "count", n)
			}
		}
	}
}
