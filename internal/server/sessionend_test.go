package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stream opens GET /events with a cookie and returns its lines.
func openStream(t *testing.T, base string, c *http.Cookie) (<-chan string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/events", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		cancel()
		t.Fatalf("events: %v %v", err, resp)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	// Wait for ": connected" so the subscription exists before anything is published.
	select {
	case l := <-lines:
		if !strings.HasPrefix(l, ": connected") {
			t.Fatalf("first line %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream didn't start")
	}
	return lines, func() { cancel(); resp.Body.Close() }
}

// ended waits for the stream to send session.ended (with reason) and close.
func ended(lines <-chan string, reason string, wait time.Duration) bool {
	deadline := time.After(wait)
	got := false
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return got
			}
			if strings.HasPrefix(l, "data: ") && strings.Contains(l, `"session.ended"`) && strings.Contains(l, `"reason":"`+reason+`"`) {
				got = true
			}
		case <-deadline:
			return false
		}
	}
}

// quiet: nothing about session.ended arrives within wait, and the stream stays open.
func quiet(lines <-chan string, wait time.Duration) bool {
	deadline := time.After(wait)
	for {
		select {
		case l, ok := <-lines:
			if !ok || strings.Contains(l, "session.ended") {
				return false
			}
		case <-deadline:
			return true
		}
	}
}

func TestSessionsEndAtOnce(t *testing.T) {
	f := newAuthFixture(t, false)
	ts := httptest.NewServer(f.h)
	defer ts.Close()
	ctx := context.Background()
	for _, u := range []string{"admin", "op"} {
		if _, err := f.st.CreateUser(ctx, u, mustHash(t, "correct horse battery")); err != nil {
			t.Fatal(err)
		}
	}
	// httptest serves plain HTTP: send the cookie by its name, as a browser would over HTTPS.
	signIn := func(u, pw string) *http.Cookie {
		rec, _ := f.do(req{method: "POST", path: "/api/v1/auth/sign-in", body: `{"username":"` + u + `","password":"` + pw + `"}`})
		return sessionCookieFrom(t, rec, cookieName)
	}
	post := func(c *http.Cookie, method, path, body string) int {
		r, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", ts.URL)
		r.AddCookie(c)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	opHere, opThere := signIn("op", "correct horse battery"), signIn("op", "correct horse battery")
	admin := signIn("admin", "correct horse battery")
	here, closeHere := openStream(t, ts.URL, opHere)
	defer closeHere()
	there, closeThere := openStream(t, ts.URL, opThere)
	defer closeThere()
	adm, closeAdm := openStream(t, ts.URL, admin)
	defer closeAdm()

	// op changes their password here: the other session is kicked out at once.
	if code := post(opHere, "PUT", "/api/v1/auth/session/password", `{"currentPassword":"correct horse battery","newPassword":"a brand new one"}`); code != 204 {
		t.Fatalf("change password: %d", code)
	}
	if !ended(there, endPasswordChanged, 2*time.Second) {
		t.Error("op's other session wasn't told, or its stream stayed open")
	}
	if !quiet(here, 300*time.Millisecond) || !quiet(adm, 10*time.Millisecond) {
		t.Error("the session that changed it, or another account, was told")
	}

	// The admin sets op's password: op's remaining session goes too.
	opUser, _ := f.st.UserByName(ctx, "op")
	if code := post(admin, "PUT", "/api/v1/users/"+opUser.ID+"/password", `{"password":"set by the admin"}`); code != 204 {
		t.Fatalf("set password: %d", code)
	}
	if !ended(here, endPasswordChanged, 2*time.Second) {
		t.Error("op wasn't told after the admin set the password")
	}
	if !quiet(adm, 300*time.Millisecond) {
		t.Error("the admin was told")
	}
}

func TestStreamNoticesIdleSession(t *testing.T) {
	saved := heartbeat
	heartbeat = 50 * time.Millisecond
	defer func() { heartbeat = saved }()
	f := newAuthFixture(t, false)
	ts := httptest.NewServer(f.h)
	defer ts.Close()
	if _, err := f.st.CreateUser(context.Background(), "admin", mustHash(t, "correct horse battery")); err != nil {
		t.Fatal(err)
	}
	rec, _ := f.do(req{method: "POST", path: "/api/v1/auth/sign-in", body: `{"username":"admin","password":"correct horse battery"}`})
	lines, closeIt := openStream(t, ts.URL, sessionCookieFrom(t, rec, cookieName))
	defer closeIt()
	if !quiet(lines, 200*time.Millisecond) {
		t.Fatal("ended while valid")
	}
	f.now = f.now.Add(sessionIdle + time.Minute)
	if !ended(lines, endExpired, 2*time.Second) {
		t.Error("an idle session's stream stayed open")
	}
}
