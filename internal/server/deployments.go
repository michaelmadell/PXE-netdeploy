package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

// Deployment endpoints: listing, arming, cancelling (ADR 0026).

func deploymentToAPI(d store.Deployment) api.Deployment {
	tag := versionETag(d.Version)
	origin := api.DeploymentOrigin(d.Origin)
	out := api.Deployment{Id: &d.ID, Etag: &tag, CreatedAt: &d.CreatedAt, UpdatedAt: &d.UpdatedAt, ClientId: d.ClientID,
		ProfileId: d.ProfileID, Origin: &origin, Status: api.DeploymentStatus(d.Status), Percent: &d.Percent,
		StartedAt: d.StartedAt, FinishedAt: d.FinishedAt, ComputerName: d.ComputerName}
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

// prepareDeployment validates the profile for this client and builds the
// deployment to store: snapshot, computer name, secret copies (ADR 0026).
// A non-nil problem refuses it (422).
func (s *Server) prepareDeployment(ctx context.Context, c store.Client, p store.Profile, origin string) (store.NewDeployment, *api.Problem, error) {
	fs, err := s.validateProfile(ctx, p)
	if err != nil {
		return store.NewDeployment{}, nil, err
	}
	if origin == store.OriginBootPrompt {
		// The agent already runs in a booted WinPE: the boot image's state
		// doesn't matter.
		kept := fs[:0]
		for _, f := range fs {
			if f.code != "boot_image_stale" && f.code != "boot_image_not_ready" {
				kept = append(kept, f)
			}
		}
		fs = kept
	}
	if len(errorsIn(fs)) > 0 {
		pr := findingsProblem(p.Name, fs)
		return store.NewDeployment{}, &pr, nil
	}
	snap, err := profileSnapshot(p)
	if err != nil {
		return store.NewDeployment{}, nil, err
	}
	up, err := unattend.Parse(snap)
	if err != nil {
		return store.NewDeployment{}, nil, err
	}
	odj := up.Identity.Mode == unattend.ModeAD && up.Identity.JoinMethod == unattend.JoinODJ
	if odj && c.ODJBlobSecretID == nil {
		pr := newProblem(http.StatusUnprocessableEntity, "missing_odj_blob", "No ODJ blob",
			"Profile "+strconv.Quote(p.Name)+" joins the domain offline; client "+c.MAC+" has no secretRefs.odjBlob.", nil)
		return store.NewDeployment{}, &pr, nil
	}
	nd := store.NewDeployment{ClientID: c.ID, ProfileID: p.ID, ProfileSnapshot: snap, Origin: origin,
		Running: origin == store.OriginBootPrompt}
	if pat := up.OS.ComputerNamePattern; pat != nil && *pat != "" && !odj {
		name, err := unattend.RenderName(*pat, nameData(c, up))
		if err != nil {
			pr := newProblem(http.StatusUnprocessableEntity, "invalid_computer_name", "Invalid computer name", err.Error(), nil)
			return store.NewDeployment{}, &pr, nil
		}
		nd.ComputerName = &name
	}
	for _, ref := range up.Secrets(c.ODJBlobSecretID) {
		nd.Secrets = append(nd.Secrets, ref.ID)
	}
	return nd, nil, nil
}

// ScheduleDeployment implements POST /deployments: arm a client.
func (s *Server) ScheduleDeployment(ctx context.Context, req api.ScheduleDeploymentRequestObject) (api.ScheduleDeploymentResponseObject, error) {
	unproc := func(code, title, detail string) (api.ScheduleDeploymentResponseObject, error) {
		return api.ScheduleDeployment422ApplicationProblemPlusJSONResponse(newProblem(http.StatusUnprocessableEntity, code, title, detail, nil)), nil
	}
	if req.Body == nil || req.Body.ClientId == "" {
		return api.ScheduleDeployment400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "clientId is required.", nil))}, nil
	}
	c, err := s.store.Client(ctx, req.Body.ClientId)
	if errors.Is(err, store.ErrNotFound) {
		return unproc("unknown_client", "Unknown client", "No client "+req.Body.ClientId+".")
	}
	if err != nil {
		return nil, err
	}
	pid := req.Body.ProfileId
	if pid == nil {
		pid = c.ProfileID
	}
	if pid == nil {
		return unproc("no_profile", "No profile", "Name a profileId or assign the client a profile.")
	}
	p, err := s.store.Profile(ctx, *pid)
	if errors.Is(err, store.ErrNotFound) {
		return unproc("unknown_profile", "Unknown profile", "No profile "+*pid+".")
	}
	if err != nil {
		return nil, err
	}
	nd, prob, err := s.prepareDeployment(ctx, c, p, store.OriginScheduled)
	if err != nil {
		return nil, err
	}
	if prob != nil {
		return api.ScheduleDeployment422ApplicationProblemPlusJSONResponse(*prob), nil
	}
	d, err := s.store.CreateDeployment(ctx, nd)
	switch {
	case errors.Is(err, store.ErrDeploymentActive):
		return api.ScheduleDeployment409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "deployment_active",
			"Deployment active", "The client already has a pending or running deployment.", nil)), nil
	case errors.Is(err, store.ErrNotFound):
		return unproc("missing_secret", "Missing secret", err.Error())
	case err != nil:
		return nil, err
	}
	s.deploymentEvent(d)
	return api.ScheduleDeployment201JSONResponse(deploymentToAPI(d)), nil
}

// CancelDeployment implements POST /deployments/{id}/cancel. A running
// deployment stops gracefully: the agent finds out at its next request.
func (s *Server) CancelDeployment(ctx context.Context, req api.CancelDeploymentRequestObject) (api.CancelDeploymentResponseObject, error) {
	d, err := s.store.Deployment(ctx, req.DeploymentId)
	if errors.Is(err, store.ErrNotFound) {
		return api.CancelDeployment404ApplicationProblemPlusJSONResponse(deploymentNotFound(req.DeploymentId)), nil
	}
	if err != nil {
		return nil, err
	}
	if d.Status == store.StatusPending || d.Status == store.StatusRunning {
		d, err = s.store.FinishDeployment(ctx, d.ID, store.StatusCancelled, "cancelled by an administrator")
		if errors.Is(err, store.ErrBadTransition) { // finished meanwhile
			d, err = s.store.Deployment(ctx, req.DeploymentId)
		} else if err == nil {
			s.deploymentEvent(d)
		}
		if err != nil {
			return nil, err
		}
	}
	return api.CancelDeployment200JSONResponse(deploymentToAPI(d)), nil
}
