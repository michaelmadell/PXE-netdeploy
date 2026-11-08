package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Read-only deployment endpoints (arming and cancelling come with
// decision 5).

func deploymentToAPI(d store.Deployment) api.Deployment {
	tag := versionETag(d.Version)
	origin := api.DeploymentOrigin(d.Origin)
	out := api.Deployment{Id: &d.ID, Etag: &tag, CreatedAt: &d.CreatedAt, UpdatedAt: &d.UpdatedAt, ClientId: d.ClientID,
		ProfileId: d.ProfileID, Origin: &origin, Status: api.DeploymentStatus(d.Status), Percent: &d.Percent,
		StartedAt: d.StartedAt, FinishedAt: d.FinishedAt}
	if d.Message != "" {
		out.Message = &d.Message
	}
	if d.Stage != nil {
		st := api.DeploymentStage(*d.Stage)
		out.Stage = &st
	}
	var snap api.ProfileInput
	if json.Unmarshal(d.ProfileSnapshot, &snap) == nil && snap.Name != "" {
		out.ProfileSnapshot = &snap
	}
	return out
}

// ListDeployments implements GET /deployments (newest first).
func (s *Server) ListDeployments(ctx context.Context, req api.ListDeploymentsRequestObject) (api.ListDeploymentsResponseObject, error) {
	limit, before, client := 0, "", ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		before = *req.Params.Cursor
	}
	if req.Params.ClientId != nil {
		client = *req.Params.ClientId
	}
	list, next, err := s.store.ListDeployments(ctx, client, limit, before)
	if err != nil {
		return nil, err
	}
	out := api.ListDeployments200JSONResponse{Items: []api.Deployment{}}
	for _, d := range list {
		if req.Params.Status != nil && string(*req.Params.Status) != d.Status {
			continue
		}
		out.Items = append(out.Items, deploymentToAPI(d))
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// GetDeployment implements GET /deployments/{deploymentId}.
func (s *Server) GetDeployment(ctx context.Context, req api.GetDeploymentRequestObject) (api.GetDeploymentResponseObject, error) {
	d, err := s.store.Deployment(ctx, req.DeploymentId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetDeployment404ApplicationProblemPlusJSONResponse(deploymentNotFound(req.DeploymentId)), nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetDeployment200JSONResponse(deploymentToAPI(d)), nil
}

// GetDeploymentLogs implements GET /deployments/{deploymentId}/logs.
func (s *Server) GetDeploymentLogs(ctx context.Context, req api.GetDeploymentLogsRequestObject) (api.GetDeploymentLogsResponseObject, error) {
	if _, err := s.store.Deployment(ctx, req.DeploymentId); errors.Is(err, store.ErrNotFound) {
		return api.GetDeploymentLogs404ApplicationProblemPlusJSONResponse(deploymentNotFound(req.DeploymentId)), nil
	} else if err != nil {
		return nil, err
	}
	limit, after := 0, int64(0)
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		after, _ = strconv.ParseInt(*req.Params.Cursor, 10, 64)
	}
	entries, last, err := s.store.DeploymentLogs(ctx, req.DeploymentId, after, limit)
	if err != nil {
		return nil, err
	}
	out := api.GetDeploymentLogs200JSONResponse{Items: make([]api.LogEntry, len(entries))}
	for i, e := range entries {
		out.Items[i] = api.LogEntry{At: e.At, Level: api.LogEntryLevel(e.Level), Message: e.Message, Source: e.Source}
	}
	if last > 0 {
		c := strconv.FormatInt(last, 10)
		out.NextCursor = &c
	}
	return out, nil
}

func deploymentNotFound(id string) api.Problem {
	return newProblem(http.StatusNotFound, "not_found", "Not found", "No deployment "+id+".", nil)
}
