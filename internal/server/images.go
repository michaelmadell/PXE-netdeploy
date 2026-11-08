package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/imports"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// ImportImage implements POST /images: queue an import of a completed
// upload, or point at the existing image of the same ISO.
func (s *Server) ImportImage(ctx context.Context, req api.ImportImageRequestObject) (api.ImportImageResponseObject, error) {
	b := req.Body
	if b == nil || b.Name == "" {
		return api.ImportImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "uploadId and a non-empty name are required.", nil))}, nil
	}
	if len(b.Name) > 200 {
		return api.ImportImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "name is at most 200 characters.", nil))}, nil
	}
	retain := b.RetainIso != nil && *b.RetainIso

	// Rough space check: an ISO extracts to about its own size.
	if u, err := s.store.Upload(ctx, b.UploadId); err == nil && s.content != nil {
		if err := s.content.CheckSpace(u.Length); errors.Is(err, cas.ErrInsufficientStorage) {
			return api.ImportImage507ApplicationProblemPlusJSONResponse(
				newProblem(http.StatusInsufficientStorage, "insufficient_storage", "Not enough storage", err.Error(), nil)), nil
		}
	}

	im, job, err := s.store.CreateImageImport(ctx, store.NewImageImport{Name: b.Name, UploadID: b.UploadId, RetainISO: retain}, imports.KindImageImport)
	var dup *store.DuplicateImageError
	switch {
	case errors.As(err, &dup):
		msg := fmt.Sprintf("This ISO is already imported as %q.", dup.Image.Name)
		if dup.Image.Status == store.ImageImporting {
			msg = fmt.Sprintf("This ISO is already being imported as %q.", dup.Image.Name)
		}
		if job, err = s.store.CreateFinishedJob(ctx, imports.KindImageImport, dup.Image.ID, msg); err != nil {
			return nil, err
		}
		s.jobEvent(job)
		return jobAccepted(job), nil
	case errors.Is(err, store.ErrNotFound):
		return api.ImportImage404ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusNotFound, "not_found", "Not found", "No upload "+b.UploadId+".", nil)), nil
	case errors.Is(err, store.ErrUploadIncomplete):
		return api.ImportImage409ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusConflict, "upload_incomplete", "Upload incomplete", "The upload hasn't received all its bytes yet.", nil)), nil
	case errors.Is(err, store.ErrUploadInUse):
		return api.ImportImage409ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusConflict, "upload_in_use", "Upload in use", "Another import is using this upload.", nil)), nil
	case err != nil:
		return nil, err
	}
	s.events.Publish(events.Event{Type: events.ImageUpdated, ResourceID: im.ID})
	s.jobEvent(job)
	if s.jobs != nil {
		s.jobs.Wake()
	}
	return jobAccepted(job), nil
}

func jobAccepted(j store.Job) api.ImportImage202JSONResponse {
	return api.ImportImage202JSONResponse{JobAcceptedJSONResponse: api.JobAcceptedJSONResponse{
		Body: jobToAPI(j), Headers: api.JobAcceptedResponseHeaders{Location: apiBase + "/jobs/" + j.ID},
	}}
}

func (s *Server) jobEvent(j store.Job) {
	s.events.Publish(events.Event{Type: events.JobUpdated, ResourceID: j.ID, Data: map[string]any{
		"kind": j.Kind, "status": j.Status, "percent": j.Percent, "resourceId": j.ResourceID,
	}})
}

// ListImages implements GET /images.
func (s *Server) ListImages(ctx context.Context, req api.ListImagesRequestObject) (api.ListImagesResponseObject, error) {
	p := req.Params
	f := store.ImageFilter{}
	if p.Limit != nil {
		f.Limit = *p.Limit
	}
	if p.Cursor != nil {
		f.After = *p.Cursor
	}
	if p.Kind != nil {
		f.Kind = string(*p.Kind)
	}
	list, next, err := s.store.ListImages(ctx, f)
	if err != nil {
		return nil, err
	}
	out := api.ListImages200JSONResponse{Items: make([]api.Image, len(list))}
	for i, im := range list {
		out.Items[i] = imageToAPI(im)
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// GetImage implements GET /images/{imageId}.
func (s *Server) GetImage(ctx context.Context, req api.GetImageRequestObject) (api.GetImageResponseObject, error) {
	im, err := s.store.Image(ctx, req.ImageId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetImage404ApplicationProblemPlusJSONResponse(imageNotFound(req.ImageId)), nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetImage200JSONResponse{Body: imageToAPI(im), Headers: api.GetImage200ResponseHeaders{ETag: imageETag(im)}}, nil
}

// UpdateImage implements PATCH /images/{imageId} (name, notes).
func (s *Server) UpdateImage(ctx context.Context, req api.UpdateImageRequestObject) (api.UpdateImageResponseObject, error) {
	if req.Body == nil {
		return api.UpdateImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "A JSON body is required.", nil))}, nil
	}
	if n := req.Body.Name; n != nil && (*n == "" || len(*n) > 200) {
		return api.UpdateImage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "name must be 1 to 200 characters.", nil))}, nil
	}
	version, ok := parseETag(req.Params.IfMatch)
	if !ok {
		return api.UpdateImage412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	im, err := s.store.UpdateImage(ctx, req.ImageId, req.Body.Name, req.Body.Notes, version)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.UpdateImage404ApplicationProblemPlusJSONResponse(imageNotFound(req.ImageId)), nil
	case errors.Is(err, store.ErrVersionMismatch):
		return api.UpdateImage412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	case err != nil:
		return nil, err
	}
	s.events.Publish(events.Event{Type: events.ImageUpdated, ResourceID: im.ID})
	return api.UpdateImage200JSONResponse(imageToAPI(im)), nil
}

// DeleteImage implements DELETE /images/{imageId}. The files are reclaimed
// by a content store sweep before it returns.
func (s *Server) DeleteImage(ctx context.Context, req api.DeleteImageRequestObject) (api.DeleteImageResponseObject, error) {
	err := s.store.DeleteImage(ctx, req.ImageId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.DeleteImage404ApplicationProblemPlusJSONResponse(imageNotFound(req.ImageId)), nil
	case errors.Is(err, store.ErrImageImporting):
		return api.DeleteImage409ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusConflict, "image_importing", "Image is importing", "Cancel its import job first.", nil)), nil
	case err != nil:
		return nil, err
	}
	s.events.Publish(events.Event{Type: events.ImageUpdated, ResourceID: req.ImageId, Data: map[string]any{"change": "deleted"}})
	if s.content != nil {
		// Reclaim the files now; a failure here only delays it to the next
		// sweep (startup, or the next delete).
		if err := s.content.Sweep(context.WithoutCancel(ctx)); err != nil {
			slog.Warn("content sweep after image delete", "err", err)
		}
	}
	return api.DeleteImage204Response{}, nil
}

func imageNotFound(id string) api.Problem {
	return newProblem(http.StatusNotFound, "not_found", "Not found", "No image "+id+".", nil)
}

func imageETag(im store.Image) string { return strconv.Quote(strconv.FormatInt(im.Version, 10)) }

func imageToAPI(im store.Image) api.Image {
	tag := imageETag(im)
	out := api.Image{
		Id: &im.ID, Etag: &tag, CreatedAt: &im.CreatedAt, UpdatedAt: &im.UpdatedAt,
		Name: im.Name, Status: api.ImageStatus(im.Status), RetainIso: im.RetainISO,
		SourceFilename: im.SourceFilename, VolumeLabel: im.VolumeLabel, UploadId: im.UploadID,
		SizeBytes: &im.SizeBytes, Sha256: &im.SHA256,
	}
	if im.Notes != "" {
		out.Notes = &im.Notes
	}
	if im.Kind != nil {
		k := api.ImageKind(*im.Kind)
		out.Kind = &k
	}
	if im.Error != nil {
		var p api.Problem
		if json.Unmarshal(im.Error, &p) == nil {
			out.Error = &p
		}
	}
	if im.Details != nil {
		var d api.Image_Details
		if d.UnmarshalJSON(im.Details) == nil {
			out.Details = &d
		}
	}
	if len(im.Warnings) > 0 {
		w := im.Warnings
		out.Warnings = &w
	}
	return out
}
