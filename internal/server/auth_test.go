package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// authFixture is the admin handler as main builds it: authentication
// outside validation, idempotency and the operations.
type authFixture struct {
	t     *testing.T
	h     http.Handler
	srv   *Server
	st    *store.Store
	dir   *string
	setup *SetupCode
	now   time.Time
}

func newAuthFixture(t *testing.T, dev bool) *authFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	setup, err := NewSetupCode(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &authFixture{t: t, st: st, dir: &dir, setup: setup, now: time.Now()}
	f.srv = New("test", Deps{Store: st, Events: events.New(), Setup: setup, Dev: dev})
	f.srv.auth.now = func() time.Time { return f.now }
	spec, _ := api.GetSwagger()
	validate, _ := RequestValidator(spec)
	auth, err := f.srv.Authenticate(spec)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(f.srv, nil, api.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	f.h = Recover(Logging(WithRequestInfo(auth(validate(Idempotency(st)(mux))))))
	return f
}

type req struct {
	method, path, body string
	cookie             *http.Cookie
	token              string
	origin             string
	remote             string
	headers            map[string]string
}

func (f *authFixture) do(r req) (*httptest.ResponseRecorder, map[string]any) {
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	hr.Header.Set("Content-Type", "application/json")
	if r.cookie != nil {
		hr.AddCookie(r.cookie)
	}
	if r.token != "" {
		hr.Header.Set("Authorization", "Bearer "+r.token)
	}
	if r.origin != "" {
		hr.Header.Set("Origin", r.origin)
	}
	if r.remote != "" {
		hr.RemoteAddr = r.remote
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, hr)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", name, rec.Header())
	return nil
}

const site = "https://example.com" // httptest requests are for Host example.com

func TestAuthSetupAndSignIn(t *testing.T) {
	f := newAuthFixture(t, false)

	// Before the first account: only the open operations work.
	if rec, s := f.do(req{method: "GET", path: "/api/v1/auth/session"}); rec.Code != 200 || s["setupRequired"] != true || s["signedIn"] != false {
		t.Fatalf("session: %d %v", rec.Code, s)
	}
	if rec, p := f.do(req{method: "GET", path: "/api/v1/clients"}); rec.Code != 401 || p["code"] != "unauthenticated" {
		t.Fatalf("clients: %d %v", rec.Code, p)
	}
	if rec, _ := f.do(req{method: "GET", path: "/api/v1/no-such-thing"}); rec.Code != 401 {
		t.Errorf("unknown path: %d", rec.Code)
	}
	if rec, _ := f.do(req{method: "GET", path: "/api/v1/agent/../clients"}); rec.Code != 401 {
		t.Errorf("agent path trick: %d", rec.Code)
	}
	if rec, _ := f.do(req{method: "GET", path: "/index.html"}); rec.Code == 401 {
		t.Error("UI files need no sign-in")
	}
	if rec, h := f.do(req{method: "GET", path: "/api/v1/system/health"}); rec.Code != 200 || h["warnings"] != nil || h["status"] == nil {
		t.Errorf("public health: %d %v", rec.Code, h)
	}
	if rec, p := f.do(req{method: "POST", path: "/api/v1/auth/sign-in", body: `{"username":"a","password":"b"}`}); rec.Code != 409 || p["code"] != "setup_required" {
		t.Errorf("sign-in before setup: %d %v", rec.Code, p)
	}
	body := `{"setupCode":"%s","username":"Michael","password":"correct horse battery"}`
	if rec, p := f.do(req{method: "POST", path: "/api/v1/auth/setup", body: strings.Replace(body, "%s", "AAAAA-AAAAA", 1)}); rec.Code != 403 || p["code"] != "bad_setup_code" {
		t.Fatalf("wrong code: %d %v", rec.Code, p)
	}
	if rec, p := f.do(req{method: "POST", path: "/api/v1/auth/setup", origin: "https://evil.example", body: strings.Replace(body, "%s", f.setup.Display(), 1)}); rec.Code != 403 || p["code"] != "bad_origin" {
		t.Fatalf("setup from another site: %d %v", rec.Code, p)
	}
	code := strings.ToLower(f.setup.Display()) // case and dash don't matter
	rec, s := f.do(req{method: "POST", path: "/api/v1/auth/setup", origin: site, body: strings.Replace(body, "%s", code, 1)})
	if rec.Code != 201 || s["signedIn"] != true {
		t.Fatalf("setup: %d %v", rec.Code, s)
	}
	c := sessionCookieFrom(t, rec, cookieName)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie attributes: %+v", c)
	}
	if _, err := os.Stat(filepath.Join(*f.dir, SetupCodeFile)); !os.IsNotExist(err) {
		t.Errorf("setup code file left: %v", err)
	}
	if rec, p := f.do(req{method: "POST", path: "/api/v1/auth/setup", body: strings.Replace(body, "%s", code, 1)}); rec.Code != 409 || p["code"] != "already_set_up" {
		t.Errorf("second setup: %d %v", rec.Code, p)
	}

	// The cookie works; changes need this site's Origin.
	if rec, s := f.do(req{method: "GET", path: "/api/v1/auth/session", cookie: c}); s["via"] != "cookie" || s["user"].(map[string]any)["username"] != "Michael" {
		t.Fatalf("session with cookie: %d %v", rec.Code, s)
	}
	if rec, p := f.do(req{method: "POST", path: "/api/v1/tokens", cookie: c, body: `{"name":"ci"}`}); rec.Code != 403 || p["code"] != "bad_origin" {
		t.Fatalf("token without Origin: %d %v", rec.Code, p)
	}
	rec, tok := f.do(req{method: "POST", path: "/api/v1/tokens", cookie: c, origin: site, body: `{"name":"ci"}`})
	secret, _ := tok["secret"].(string)
	if rec.Code != 201 || !strings.HasPrefix(secret, "ndt_") || tok["createdBy"] != "Michael" {
		t.Fatalf("create token: %d %v", rec.Code, tok)
	}

	// The token: everything but accounts and tokens, no Origin needed.
	if rec, _ := f.do(req{method: "GET", path: "/api/v1/clients", token: secret}); rec.Code != 200 {
		t.Errorf("clients with token: %d", rec.Code)
	}
	if rec, p := f.do(req{method: "POST", path: "/api/v1/clients", token: secret, body: `{"mac":"00:15:5d:00:00:09"}`}); rec.Code != 201 {
		t.Errorf("create client with token (no Origin): %d %v", rec.Code, p)
	}
	for _, r := range []req{
		{method: "GET", path: "/api/v1/users"},
		{method: "POST", path: "/api/v1/tokens", body: `{"name":"more"}`},
		{method: "POST", path: "/api/v1/users", body: `{"username":"x","password":"correct horse battery"}`},
	} {
		r.token = secret
		if rec, p := f.do(r); rec.Code != 403 || p["code"] != "not_for_api_tokens" {
			t.Errorf("%s %s with token: %d %v", r.method, r.path, rec.Code, p)
		}
	}
	if rec, _ := f.do(req{method: "GET", path: "/api/v1/clients", token: "ndt_wrong"}); rec.Code != 401 {
		t.Errorf("wrong token: %d", rec.Code)
	}
	if rec, s := f.do(req{method: "GET", path: "/api/v1/tokens", cookie: c}); rec.Code != 200 || s["items"].([]any)[0].(map[string]any)["lastUsedAt"] == nil {
		t.Errorf("tokens: %d %v", rec.Code, s)
	}

	// Sign out, then in again.
	if rec, _ := f.do(req{method: "POST", path: "/api/v1/auth/sign-out", cookie: c, origin: site}); rec.Code != 204 || sessionCookieFrom(t, rec, cookieName).MaxAge >= 0 {
		t.Fatalf("sign-out: %d", rec.Code)
	}
	if rec, _ := f.do(req{method: "GET", path: "/api/v1/clients", cookie: c}); rec.Code != 401 {
		t.Errorf("after sign-out: %d", rec.Code)
	}
	rec, s = f.do(req{method: "POST", path: "/api/v1/auth/sign-in", origin: site, body: `{"username":"michael","password":"correct horse battery"}`})
	if rec.Code != 200 || s["signedIn"] != true {
		t.Fatalf("sign-in: %d %v", rec.Code, s)
	}
	c = sessionCookieFrom(t, rec, cookieName)

	// Idle for 8 hours: the session has ended.
	f.now = f.now.Add(sessionIdle + time.Minute)
	if rec, _ := f.do(req{method: "GET", path: "/api/v1/clients", cookie: c}); rec.Code != 401 {
		t.Errorf("idle session: %d", rec.Code)
	}
}

func TestSignInRateLimit(t *testing.T) {
	f := newAuthFixture(t, false)
	if _, err := f.st.CreateUser(context.Background(), "admin", mustHash(t, "correct horse battery")); err != nil {
		t.Fatal(err)
	}
	wrong := req{method: "POST", path: "/api/v1/auth/sign-in", body: `{"username":"admin","password":"nope"}`, remote: "192.0.2.7:1234"}
	for i := range freeFailures + 1 {
		if rec, p := f.do(wrong); rec.Code != 401 || p["code"] != "bad_credentials" {
			t.Fatalf("attempt %d: %d %v", i+1, rec.Code, p)
		}
	}
	rec, p := f.do(wrong)
	if rec.Code != 429 || p["code"] != "too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after %d failures: %d %v", freeFailures+1, rec.Code, p)
	}
	// Another address is blocked too: the username is.
	right := req{method: "POST", path: "/api/v1/auth/sign-in", body: `{"username":"admin","password":"correct horse battery"}`, remote: "192.0.2.8:1234"}
	if rec, _ := f.do(right); rec.Code != 429 {
		t.Errorf("username not limited: %d", rec.Code)
	}
	f.now = f.now.Add(maxBackoff)
	if rec, _ := f.do(right); rec.Code != 200 {
		t.Errorf("after the back-off: %d", rec.Code)
	}
	// An unknown username answers the same as a wrong password.
	if rec, p := f.do(req{method: "POST", path: "/api/v1/auth/sign-in", body: `{"username":"nobody","password":"x"}`, remote: "192.0.2.9:1"}); rec.Code != 401 || p["code"] != "bad_credentials" {
		t.Errorf("unknown user: %d %v", rec.Code, p)
	}
}

func TestAccounts(t *testing.T) {
	f := newAuthFixture(t, false)
	ctx := context.Background()
	if _, err := f.st.CreateUser(ctx, "admin", mustHash(t, "correct horse battery")); err != nil {
		t.Fatal(err)
	}
	signIn := func(user, pw string) *http.Cookie {
		t.Helper()
		rec, s := f.do(req{method: "POST", path: "/api/v1/auth/sign-in", body: `{"username":"` + user + `","password":"` + pw + `"}`})
		if rec.Code != 200 {
			t.Fatalf("sign-in %s: %d %v", user, rec.Code, s)
		}
		return sessionCookieFrom(t, rec, cookieName)
	}
	admin := signIn("admin", "correct horse battery")
	as := func(c *http.Cookie, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
		return f.do(req{method: method, path: path, body: body, cookie: c, origin: site})
	}

	if rec, p := as(admin, "POST", "/api/v1/users", `{"username":"ADMIN","password":"another long one"}`); rec.Code != 409 || p["code"] != "username_taken" {
		t.Errorf("duplicate name: %d %v", rec.Code, p)
	}
	if rec, _ := as(admin, "POST", "/api/v1/users", `{"username":"op","password":"7chars!"}`); rec.Code != 400 {
		t.Errorf("7-character password: %d", rec.Code)
	}
	if rec, u8 := as(admin, "POST", "/api/v1/users", `{"username":"op8","password":"8chars!!"}`); rec.Code != 201 {
		t.Errorf("8-character password: %d", rec.Code)
	} else if rec, _ := as(admin, "DELETE", "/api/v1/users/"+u8["id"].(string), ""); rec.Code != 204 {
		t.Errorf("delete op8: %d", rec.Code)
	}
	rec, u := as(admin, "POST", "/api/v1/users", `{"username":"op","password":"operator password"}`)
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, u)
	}
	opID := u["id"].(string)
	op1 := signIn("op", "operator password")
	op2 := signIn("op", "operator password")
	_, tok := as(op1, "POST", "/api/v1/tokens", `{"name":"op's"}`)
	opToken := tok["secret"].(string)

	// Changing one's own password keeps this session, ends the others.
	if rec, p := as(op1, "PUT", "/api/v1/auth/session/password", `{"currentPassword":"wrong","newPassword":"a new long password"}`); rec.Code != 403 || p["code"] != "bad_credentials" {
		t.Errorf("wrong current: %d %v", rec.Code, p)
	}
	if rec, p := as(op1, "PUT", "/api/v1/auth/session/password", `{"currentPassword":"operator password","newPassword":"a new long password"}`); rec.Code != 204 {
		t.Fatalf("change: %d %v", rec.Code, p)
	}
	if rec, _ := as(op1, "GET", "/api/v1/users", ""); rec.Code != 200 {
		t.Errorf("own session ended: %d", rec.Code)
	}
	if rec, _ := as(op2, "GET", "/api/v1/users", ""); rec.Code != 401 {
		t.Errorf("other session kept: %d", rec.Code)
	}
	// Set by another account: all of op's sessions end.
	if rec, _ := as(admin, "PUT", "/api/v1/users/"+opID+"/password", `{"password":"set by the admin"}`); rec.Code != 204 {
		t.Fatalf("set password: %d", rec.Code)
	}
	if rec, _ := as(op1, "GET", "/api/v1/users", ""); rec.Code != 401 {
		t.Errorf("session after reset: %d", rec.Code)
	}

	// Deleting op revokes its token; the last account can't go.
	if rec, _ := f.do(req{method: "GET", path: "/api/v1/clients", token: opToken}); rec.Code != 200 {
		t.Fatalf("op's token: %d", rec.Code)
	}
	if rec, _ := as(admin, "DELETE", "/api/v1/users/"+opID, ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec, _ := f.do(req{method: "GET", path: "/api/v1/clients", token: opToken}); rec.Code != 401 {
		t.Errorf("token of a deleted account: %d", rec.Code)
	}
	adminUser, _ := f.st.UserByName(ctx, "admin")
	if rec, p := as(admin, "DELETE", "/api/v1/users/"+adminUser.ID, ""); rec.Code != 409 || p["code"] != "last_user" {
		t.Errorf("last account: %d %v", rec.Code, p)
	}
}

func TestDevOrigin(t *testing.T) {
	f := newAuthFixture(t, true)
	if _, err := f.st.CreateUser(context.Background(), "admin", mustHash(t, "correct horse battery")); err != nil {
		t.Fatal(err)
	}
	dev := map[string]string{"X-Forwarded-Host": "localhost:5173"}
	rec, _ := f.do(req{method: "POST", path: "/api/v1/auth/sign-in", origin: "http://localhost:5173", headers: dev,
		body: `{"username":"admin","password":"correct horse battery"}`})
	if rec.Code != 200 {
		t.Fatalf("sign-in through the Vite proxy: %d", rec.Code)
	}
	c := sessionCookieFrom(t, rec, cookieNameDev)
	if c.Secure {
		t.Error("dev cookie is Secure")
	}
	if rec, _ := f.do(req{method: "POST", path: "/api/v1/clients", cookie: c, origin: "http://localhost:5173", headers: dev, body: `{"mac":"00:15:5d:00:00:0a"}`}); rec.Code != 201 {
		t.Errorf("change through the proxy: %d", rec.Code)
	}
	if rec, _ := f.do(req{method: "POST", path: "/api/v1/clients", cookie: c, origin: "http://evil.example", headers: dev, body: `{"mac":"00:15:5d:00:00:0b"}`}); rec.Code != 403 {
		t.Errorf("other origin in dev: %d", rec.Code)
	}
}

func TestIdempotencyKeysPerCaller(t *testing.T) {
	f := newAuthFixture(t, false)
	ctx := context.Background()
	for _, u := range []string{"a1", "b1"} {
		if _, err := f.st.CreateUser(ctx, u, mustHash(t, "correct horse battery")); err != nil {
			t.Fatal(err)
		}
	}
	cookie := func(u string) *http.Cookie {
		rec, _ := f.do(req{method: "POST", path: "/api/v1/auth/sign-in", body: `{"username":"` + u + `","password":"correct horse battery"}`})
		return sessionCookieFrom(t, rec, cookieName)
	}
	key := map[string]string{"Idempotency-Key": "same"}
	body := `{"mac":"00:15:5d:00:00:0c"}`
	if rec, _ := f.do(req{method: "POST", path: "/api/v1/clients", cookie: cookie("a1"), origin: site, headers: key, body: body}); rec.Code != 201 {
		t.Fatalf("first: %d", rec.Code)
	}
	rec, _ := f.do(req{method: "POST", path: "/api/v1/clients", cookie: cookie("b1"), origin: site, headers: key, body: body})
	if rec.Header().Get("Idempotent-Replayed") != "" || rec.Code != 409 {
		t.Errorf("another account got a replay: %d %v", rec.Code, rec.Header())
	}
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := settings.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
