package server

import (
	"context"
	"testing"
	"time"
)

// stillValid: what an open event stream checks each heartbeat (ADR 0029).
func TestStillValid(t *testing.T) {
	f := newAuthFixture(t, false)
	ctx := context.Background()
	if _, err := f.st.CreateUser(ctx, "admin", mustHash(t, "correct horse battery")); err != nil {
		t.Fatal(err)
	}
	rec, _ := f.do(req{method: "POST", path: "/api/v1/auth/sign-in", body: `{"username":"admin","password":"correct horse battery"}`})
	p := Principal{Via: "cookie", SessionHash: tokenHash(sessionCookieFrom(t, rec, cookieName).Value)}
	if !f.srv.stillValid(ctx, p) {
		t.Fatal("a fresh session isn't valid")
	}
	if !f.srv.stillValid(ctx, Principal{Via: "token"}) {
		t.Error("tokens are ended by events, not the heartbeat")
	}
	f.now = f.now.Add(sessionIdle + time.Minute)
	if f.srv.stillValid(ctx, p) {
		t.Error("idle session still valid")
	}
	f.now = f.now.Add(-sessionIdle)
	f.st.DeleteAdminSession(ctx, p.SessionHash)
	if f.srv.stillValid(ctx, p) {
		t.Error("deleted session still valid")
	}
}
