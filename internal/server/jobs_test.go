package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

func TestJobsAPI(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runner := jobs.New(jobs.Options{Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	running := make(chan struct{}, 1)
	runner.Register(jobs.Kind{Name: "bootImage.build", Lane: jobs.LaneBuild, Run: func(ctx context.Context, r *jobs.Run) error {
		running <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}})
	rctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); <-runner.Done() }()

	res := "bi1"
	first, _ := st.CreateJob(ctx, store.NewJob{Kind: "bootImage.build", ResourceID: &res})
	second, _ := st.CreateJob(ctx, store.NewJob{Kind: "bootImage.build"})
	if err := runner.Start(rctx); err != nil {
		t.Fatal(err)
	}
	<-running

	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(New("test", Deps{Store: st, Jobs: runner}), nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError,
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})
	h := Recover(mux)

	rec, page := call(h, "GET", "/api/v1/jobs?resourceId=bi1", "")
	items, _ := page["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 1 {
		t.Fatalf("list by resource: %d %v", rec.Code, page)
	}
	j := items[0].(map[string]any)
	if j["id"] != first.ID || j["status"] != "running" || j["cancelRequested"] != false || j["kind"] != "bootImage.build" {
		t.Errorf("listed job: %v", j)
	}
	if _, page := call(h, "GET", "/api/v1/jobs?status=queued&limit=1", ""); len(page["items"].([]any)) != 1 || page["nextCursor"] != nil {
		t.Errorf("queued page: %v", page)
	}

	if rec, j := call(h, "POST", "/api/v1/jobs/"+second.ID+"/cancel", ""); rec.Code != http.StatusOK || j["status"] != "cancelled" {
		t.Errorf("cancel queued: %d %v", rec.Code, j)
	}
	if rec, j := call(h, "POST", "/api/v1/jobs/"+first.ID+"/cancel", ""); rec.Code != http.StatusOK || j["cancelRequested"] != true {
		t.Errorf("cancel running: %d %v", rec.Code, j)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, j := call(h, "GET", "/api/v1/jobs/"+first.ID, "")
		if j["status"] == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("running job not cancelled: %v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, path := range []string{"/api/v1/jobs/missing", "/api/v1/jobs/missing/cancel"} {
		method := "GET"
		if path != "/api/v1/jobs/missing" {
			method = "POST"
		}
		if rec, p := call(h, method, path, ""); rec.Code != http.StatusNotFound || p["code"] != "not_found" {
			t.Errorf("%s %s: %d %v", method, path, rec.Code, p)
		}
	}
}
