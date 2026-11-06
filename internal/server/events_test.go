package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// sseReader reads `event:`/`data:` pairs from a live stream.
type sseReader struct{ sc *bufio.Scanner }

func (r sseReader) next(t *testing.T) (string, events.Event) {
	t.Helper()
	var typ string
	for r.sc.Scan() {
		line := r.sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			typ = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var e events.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
				t.Fatal(err)
			}
			return typ, e
		}
	}
	t.Fatalf("stream ended: %v", r.sc.Err())
	return "", events.Event{}
}

func TestEventStream(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bus := events.New()
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(New("test", nil, nil, st, bus), nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError,
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	srv := httptest.NewServer(Logging(mux)) // Logging wraps the writer: flushing must still work
	defer srv.Close()

	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, "GET", srv.URL+"/api/v1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	r := sseReader{bufio.NewScanner(resp.Body)}
	for bus.Subscribers() == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	created, err := http.Post(srv.URL+"/api/v1/clients", "application/json", strings.NewReader(`{"mac":"00:15:5d:00:00:01"}`))
	if err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	typ, e := r.next(t)
	data, _ := e.Data.(map[string]any)
	if typ != events.ClientUpdated || e.Type != typ || data["change"] != "created" || data["mac"] != "00:15:5d:00:00:01" || e.ResourceID == "" {
		t.Fatalf("got %s %+v", typ, e)
	}

	bus.Publish(events.Event{Type: events.LeaseUpdated, Data: map[string]string{"change": "bound"}})
	if typ, _ := r.next(t); typ != events.LeaseUpdated {
		t.Fatalf("got %s", typ)
	}

	cancel() // client goes away: the server must unsubscribe
	deadline := time.Now().Add(2 * time.Second)
	for bus.Subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("subscriber not removed after the client disconnected")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHealthReportsStore(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := New("test", nil, nil, st, nil)
	h := s.health(context.Background())
	if h.Services["store"].Status != api.ServiceHealthStatusOk || h.Status != api.HealthStatusOk {
		t.Fatalf("healthy store: %+v", h)
	}
	st.Close()
	h = s.health(context.Background())
	if h.Services["store"].Status != api.ServiceHealthStatusError || h.Status != api.HealthStatusDegraded {
		t.Fatalf("closed store: %+v", h)
	}
}
