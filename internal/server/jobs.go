package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// ListJobs implements GET /jobs: oldest first, filtered by status, kind or
// resource.
func (s *Server) ListJobs(ctx context.Context, req api.ListJobsRequestObject) (api.ListJobsResponseObject, error) {
	p := req.Params
	f := store.JobFilter{}
	if p.Limit != nil {
		f.Limit = *p.Limit
	}
	if p.Cursor != nil {
		f.After = *p.Cursor
	}
	if p.Status != nil {
		f.Status = string(*p.Status)
	}
	if p.Kind != nil {
		f.Kind = string(*p.Kind)
	}
	if p.ResourceId != nil {
		f.ResourceID = *p.ResourceId
	}
	list, next, err := s.store.ListJobs(ctx, f)
	if err != nil {
		return nil, err
	}
	items := make([]api.Job, len(list))
	for i, j := range list {
		items[i] = jobToAPI(j)
	}
	page := api.ListJobs200JSONResponse{Items: items}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
}

// GetJob implements GET /jobs/{jobId}.
func (s *Server) GetJob(ctx context.Context, req api.GetJobRequestObject) (api.GetJobResponseObject, error) {
	j, err := s.store.Job(ctx, req.JobId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetJob404ApplicationProblemPlusJSONResponse(jobNotFound()), nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetJob200JSONResponse(jobToAPI(j)), nil
}

// CancelJob implements POST /jobs/{jobId}/cancel.
func (s *Server) CancelJob(ctx context.Context, req api.CancelJobRequestObject) (api.CancelJobResponseObject, error) {
	j, err := s.jobs.Cancel(ctx, req.JobId)
	if errors.Is(err, store.ErrNotFound) {
		return api.CancelJob404ApplicationProblemPlusJSONResponse(jobNotFound()), nil
	}
	if err != nil {
		return nil, err
	}
	return api.CancelJob200JSONResponse(jobToAPI(j)), nil
}

func jobNotFound() api.Problem {
	return newProblem(http.StatusNotFound, "not_found", "Not found", "No job with that id.", nil)
}

func jobToAPI(j store.Job) api.Job {
	out := api.Job{
		Id: j.ID, Kind: api.JobKind(j.Kind), Status: api.JobStatus(j.Status), ResourceId: j.ResourceID,
		Percent: j.Percent, CancelRequested: ptr(j.CancelRequested), CreatedAt: j.CreatedAt,
		StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}
	if j.Message != "" {
		out.Message = ptr(j.Message)
	}
	if j.Error != nil {
		var p api.Problem
		if err := json.Unmarshal(j.Error, &p); err == nil {
			out.Error = &p
		}
	}
	return out
}
