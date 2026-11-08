package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/bootimage"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Boot images (ADR 0022).

func bootImageInput(b *api.BootImageInput) store.BootImageInput {
	in := store.BootImageInput{
		Name: b.Name, SourceImageID: b.SourceImageId, SourceIndex: bootimage.DefaultSourceIndex, Architecture: string(b.Architecture),
		DriverGroupIDs: []string{}, AgentVersion: b.AgentVersion, ServerURLs: b.ServerUrls, Builder: "auto", Bootmgr: "auto",
		OptionalComponents: []string{}, TFTPBlockSize: bootimage.DefaultTFTPBlockSize, TFTPWindowSize: bootimage.DefaultTFTPWindowSize,
	}
	if b.SourceIndex != nil {
		in.SourceIndex = *b.SourceIndex
	}
	if b.DriverGroupIds != nil {
		in.DriverGroupIDs = *b.DriverGroupIds
	}
	if b.Builder != nil {
		in.Builder = string(*b.Builder)
	}
	if b.Bootmgr != nil {
		in.Bootmgr = string(*b.Bootmgr)
	}
	if b.OptionalComponents != nil {
		for _, c := range *b.OptionalComponents {
			in.OptionalComponents = append(in.OptionalComponents, string(c))
		}
	}
	if b.Tftp != nil {
		if b.Tftp.BlockSize != nil {
			in.TFTPBlockSize = *b.Tftp.BlockSize
		}
		if b.Tftp.WindowSize != nil {
			in.TFTPWindowSize = *b.Tftp.WindowSize
		}
	}
	if b.ProduceHttpBootIso != nil {
		in.ProduceHTTPBootISO = *b.ProduceHttpBootIso
	}
	return in
}

func problemOf(e *jobs.Error) api.Problem {
	return newProblem(e.Status, e.Code, e.Title, e.Detail, nil)
}

// checkBootImage returns a problem for invalid inputs, or nil; an
// internal failure is an error.
func (s *Server) checkBootImage(ctx context.Context, in store.BootImageInput) (*api.Problem, error) {
	if e := s.boot.Check(ctx, in); e != nil {
		if e.Status >= 500 {
			return nil, e
		}
		p := problemOf(e)
		return &p, nil
	}
	return nil, nil
}

func bootImageStoreError(err error) (api.Problem, bool) {
	msg := strings.TrimPrefix(err.Error(), "store: ")
	switch {
	case errors.Is(err, store.ErrUnknownDriverGroup):
		return newProblem(http.StatusUnprocessableEntity, "unknown_driver_group", "Unknown driver group", msg+".", nil), true
	case errors.Is(err, store.ErrDuplicate):
		return newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", msg+".", nil), true
	}
	return api.Problem{}, false
}

// CreateBootImage implements POST /boot-images.
func (s *Server) CreateBootImage(ctx context.Context, req api.CreateBootImageRequestObject) (api.CreateBootImageResponseObject, error) {
	if req.Body == nil {
		return api.CreateBootImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	in := bootImageInput(req.Body)
	p, err := s.checkBootImage(ctx, in)
	if err != nil {
		return nil, err
	}
	if p != nil {
		if p.Status == http.StatusBadRequest {
			return api.CreateBootImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(*p)}, nil
		}
		return api.CreateBootImage422ApplicationProblemPlusJSONResponse(*p), nil
	}
	bi, err := s.store.CreateBootImage(ctx, in)
	if errors.Is(err, store.ErrNotFound) {
		return api.CreateBootImage422ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusUnprocessableEntity, "source_image_unavailable", "Source image unavailable", "No image "+in.SourceImageID+".", nil)), nil
	}
	if p, ok := bootImageStoreError(err); ok {
		if p.Status == http.StatusBadRequest {
			return api.CreateBootImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(p)}, nil
		}
		return api.CreateBootImage422ApplicationProblemPlusJSONResponse(p), nil
	}
	if err != nil {
		return nil, err
	}
	s.bootImageEvent(bi.ID, "")
	out, err := s.bootImageToAPI(ctx, bi)
	if err != nil {
		return nil, err
	}
	return api.CreateBootImage201JSONResponse(out), nil
}

// ListBootImages implements GET /boot-images.
func (s *Server) ListBootImages(ctx context.Context, req api.ListBootImagesRequestObject) (api.ListBootImagesResponseObject, error) {
	limit, after := 0, ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		after = *req.Params.Cursor
	}
	list, next, err := s.store.ListBootImages(ctx, limit, after)
	if err != nil {
		return nil, err
	}
	out := api.ListBootImages200JSONResponse{Items: make([]api.BootImage, len(list))}
	for i, bi := range list {
		if out.Items[i], err = s.bootImageToAPI(ctx, bi); err != nil {
			return nil, err
		}
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// GetBootImage implements GET /boot-images/{bootImageId}.
func (s *Server) GetBootImage(ctx context.Context, req api.GetBootImageRequestObject) (api.GetBootImageResponseObject, error) {
	bi, err := s.store.BootImage(ctx, req.BootImageId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetBootImage404ApplicationProblemPlusJSONResponse(bootImageNotFound(req.BootImageId)), nil
	}
	if err != nil {
		return nil, err
	}
	out, err := s.bootImageToAPI(ctx, bi)
	if err != nil {
		return nil, err
	}
	return api.GetBootImage200JSONResponse(out), nil
}

// ReplaceBootImage implements PUT /boot-images/{bootImageId}.
func (s *Server) ReplaceBootImage(ctx context.Context, req api.ReplaceBootImageRequestObject) (api.ReplaceBootImageResponseObject, error) {
	if req.Body == nil {
		return api.ReplaceBootImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	version, ok := parseETag(req.Params.IfMatch)
	if !ok {
		return api.ReplaceBootImage412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	in := bootImageInput(req.Body)
	p, err := s.checkBootImage(ctx, in)
	if err != nil {
		return nil, err
	}
	if p != nil {
		if p.Status == http.StatusBadRequest {
			return api.ReplaceBootImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(*p)}, nil
		}
		return api.ReplaceBootImage422ApplicationProblemPlusJSONResponse(*p), nil
	}
	bi, err := s.store.ReplaceBootImage(ctx, req.BootImageId, in, version)
	if p, ok := bootImageStoreError(err); ok {
		if p.Status == http.StatusBadRequest {
			return api.ReplaceBootImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(p)}, nil
		}
		return api.ReplaceBootImage422ApplicationProblemPlusJSONResponse(p), nil
	}
	switch {
	case errors.Is(err, store.ErrVersionMismatch):
		return api.ReplaceBootImage412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	case errors.Is(err, store.ErrNotFound) && strings.Contains(err.Error(), "source image"):
		return api.ReplaceBootImage422ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusUnprocessableEntity, "source_image_unavailable", "Source image unavailable", "No image "+in.SourceImageID+".", nil)), nil
	case errors.Is(err, store.ErrNotFound):
		return api.ReplaceBootImage404ApplicationProblemPlusJSONResponse(bootImageNotFound(req.BootImageId)), nil
	case err != nil:
		return nil, err
	}
	s.bootImageEvent(bi.ID, "")
	out, err := s.bootImageToAPI(ctx, bi)
	if err != nil {
		return nil, err
	}
	return api.ReplaceBootImage200JSONResponse(out), nil
}

// DeleteBootImage implements DELETE /boot-images/{bootImageId}.
func (s *Server) DeleteBootImage(ctx context.Context, req api.DeleteBootImageRequestObject) (api.DeleteBootImageResponseObject, error) {
	err := s.store.DeleteBootImage(ctx, req.BootImageId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.DeleteBootImage404ApplicationProblemPlusJSONResponse(bootImageNotFound(req.BootImageId)), nil
	case errors.Is(err, store.ErrBuildInProgress):
		return api.DeleteBootImage409ApplicationProblemPlusJSONResponse(buildInProgress()), nil
	case err != nil:
		return nil, err
	}
	s.bootImageEvent(req.BootImageId, "deleted")
	if s.content != nil {
		if err := s.content.Sweep(context.WithoutCancel(ctx)); err != nil {
			slog.Warn("content sweep after boot image delete", "err", err)
		}
	}
	return api.DeleteBootImage204Response{}, nil
}

// BuildBootImage implements POST /boot-images/{bootImageId}/build.
func (s *Server) BuildBootImage(ctx context.Context, req api.BuildBootImageRequestObject) (api.BuildBootImageResponseObject, error) {
	force := req.Body != nil && req.Body.Force != nil && *req.Body.Force
	bi, job, err := s.boot.Build(ctx, req.BootImageId, force)
	var je *jobs.Error
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.BuildBootImage404ApplicationProblemPlusJSONResponse(bootImageNotFound(req.BootImageId)), nil
	case errors.Is(err, store.ErrBuildInProgress):
		return api.BuildBootImage409ApplicationProblemPlusJSONResponse(buildInProgress()), nil
	case errors.Is(err, cas.ErrInsufficientStorage):
		return api.BuildBootImage507ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusInsufficientStorage, "insufficient_storage", "Not enough storage", err.Error(), nil)), nil
	case errors.As(err, &je) && je.Status == http.StatusUnprocessableEntity:
		return api.BuildBootImage422ApplicationProblemPlusJSONResponse(problemOf(je)), nil
	case err != nil:
		return nil, err
	}
	if job == nil {
		out, err := s.bootImageToAPI(ctx, bi)
		if err != nil {
			return nil, err
		}
		return api.BuildBootImage200JSONResponse(out), nil
	}
	s.jobEvent(*job)
	if s.jobs != nil {
		s.jobs.Wake()
	}
	return api.BuildBootImage202JSONResponse{JobAcceptedJSONResponse: jobAccepted(*job).JobAcceptedJSONResponse}, nil
}

func buildInProgress() api.Problem {
	return newProblem(http.StatusConflict, "build_in_progress", "Build in progress", "A build of this boot image is queued or running; cancel its job first.", nil)
}

func bootImageNotFound(id string) api.Problem {
	return newProblem(http.StatusNotFound, "not_found", "Not found", "No boot image "+id+".", nil)
}

func (s *Server) bootImageEvent(id, change string) {
	e := events.Event{Type: events.BootImageUpdated, ResourceID: id}
	if change != "" {
		e.Data = map[string]any{"change": change}
	}
	s.events.Publish(e)
}

// bootImagesChanged announces the boot images whose drivers changed (they
// may now be stale).
func (s *Server) bootImagesChanged(ctx context.Context, groupIDs []string, packageID string) {
	ids, err := s.store.BootImagesUsing(ctx, groupIDs, packageID)
	if err != nil {
		slog.Warn("find boot images using drivers", "err", err)
		return
	}
	for _, id := range ids {
		s.bootImageEvent(id, "")
	}
}

// artifactKind classifies a file of a built tree.
func artifactKind(p string) api.BootArtifactKind {
	switch {
	case strings.EqualFold(p, bootimage.TreeWIM):
		return api.BootArtifactKindWim
	case strings.EqualFold(p, bootimage.TreeBCD):
		return api.BootArtifactKindBcd
	case strings.EqualFold(p, bootimage.TreeSDI):
		return api.BootArtifactKindSdi
	case strings.EqualFold(p, bootimage.TreeBootmgr):
		return api.BootArtifactKindBootmgr
	case strings.EqualFold(path.Ext(p), ".iso"):
		return api.BootArtifactKindHttpBootIso
	}
	return api.BootArtifactKindFont
}

func (s *Server) bootImageToAPI(ctx context.Context, bi store.BootImage) (api.BootImage, error) {
	tag := versionETag(bi.Version)
	groups := bi.DriverGroupIDs
	urls := bi.ServerURLs
	builder := api.BootImageBuilder(bi.Builder)
	bootmgr := api.BootImageBootmgr(bi.Bootmgr)
	ocs := make([]api.WinPeOptionalComponent, len(bi.OptionalComponents))
	for i, c := range bi.OptionalComponents {
		ocs[i] = api.WinPeOptionalComponent(c)
	}
	iso := bi.ProduceHTTPBootISO
	idx := bi.SourceIndex
	out := api.BootImage{
		Id: &bi.ID, Etag: &tag, CreatedAt: &bi.CreatedAt, UpdatedAt: &bi.UpdatedAt,
		Name: bi.Name, SourceImageId: bi.SourceImageID, SourceIndex: &idx, Architecture: api.Architecture(bi.Architecture),
		DriverGroupIds: &groups, AgentVersion: bi.AgentVersion, ServerUrls: urls, Builder: &builder, Bootmgr: &bootmgr,
		OptionalComponents: &ocs, ProduceHttpBootIso: &iso, InputHash: bi.InputHash, BuiltAt: bi.BuiltAt, LastJobId: bi.LastJobID,
	}
	out.Tftp = &struct {
		BlockSize  *int `json:"blockSize,omitempty"`
		WindowSize *int `json:"windowSize,omitempty"`
	}{BlockSize: &bi.TFTPBlockSize, WindowSize: &bi.TFTPWindowSize}
	status, reasons := "draft", []string(nil)
	if s.boot != nil {
		status, reasons = s.boot.Status(ctx, bi)
	}
	out.Status = api.BootImageStatus(status)
	if len(reasons) > 0 {
		r := make([]api.BootImageStaleReasons, len(reasons))
		for i, x := range reasons {
			r[i] = api.BootImageStaleReasons(x)
		}
		out.StaleReasons = &r
	}
	if bi.BuiltWith != nil {
		w := api.BuilderName(*bi.BuiltWith)
		out.BuiltWith = &w
	}
	if bi.SelectedBootManager != nil {
		var bm api.BootManager
		if json.Unmarshal(bi.SelectedBootManager, &bm) == nil {
			out.SelectedBootManager = &bm
		}
	}
	if bi.Error != nil {
		var e api.Problem
		if json.Unmarshal(bi.Error, &e) == nil {
			out.Error = &e
		}
	}
	if len(bi.Warnings) > 0 {
		w := bi.Warnings
		out.Warnings = &w
	}
	if bi.TreeID != nil {
		entries, err := s.store.TreeEntries(ctx, *bi.TreeID)
		if err != nil {
			return out, err
		}
		arts := make([]api.BootArtifact, len(entries))
		total := 0
		for i, e := range entries {
			arts[i] = api.BootArtifact{Kind: artifactKind(e.Path), Path: e.Path, Sha256: e.SHA256, SizeBytes: int(e.Size)}
			total += int(e.Size)
		}
		out.Artifacts, out.SizeBytes = &arts, &total
	}
	return out, nil
}
