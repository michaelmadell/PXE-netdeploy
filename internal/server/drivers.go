package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/drivers/archive"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/imports"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

func badRequest(detail string) api.ProblemApplicationProblemPlusJSONResponse {
	return api.ProblemApplicationProblemPlusJSONResponse(newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", detail, nil))
}

// sniffUpload reads the first bytes of a complete upload and checks it is
// an archive we take (ADR 0019): installers and other files are refused
// before a job is queued.
func (s *Server) sniffUpload(u store.Upload) error {
	if s.uploads == nil {
		return nil
	}
	p, err := s.uploads.Path(u.ID)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	_, err = archive.Detect(head[:n])
	return err
}

// ImportDriverPackage implements POST /driver-packages.
func (s *Server) ImportDriverPackage(ctx context.Context, req api.ImportDriverPackageRequestObject) (api.ImportDriverPackageResponseObject, error) {
	b := req.Body
	if b == nil || b.Name == "" || len(b.Name) > 200 {
		return api.ImportDriverPackage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("uploadId and a name of 1 to 200 characters are required.")}, nil
	}
	if u, err := s.store.Upload(ctx, b.UploadId); err == nil && u.Complete() {
		if err := s.sniffUpload(u); errors.Is(err, archive.ErrInstaller) || errors.Is(err, archive.ErrUnsupported) {
			detail := "Only zip, cab and 7z archives can be imported."
			if errors.Is(err, archive.ErrInstaller) {
				detail = "This is an installer (.exe or .msi), not an archive. Extract the drivers from it first (most vendors document an extract-only switch), then upload the folder as a zip."
			}
			return api.ImportDriverPackage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
				newProblem(http.StatusBadRequest, "unsupported_archive", "Unsupported archive", detail, nil))}, nil
		}
		// Rough space check: driver archives typically expand to a few
		// times their size.
		if s.content != nil {
			if err := s.content.CheckSpace(4 * u.Length); errors.Is(err, cas.ErrInsufficientStorage) {
				return api.ImportDriverPackage507ApplicationProblemPlusJSONResponse(
					newProblem(http.StatusInsufficientStorage, "insufficient_storage", "Not enough storage", err.Error(), nil)), nil
			}
		}
	}

	pkg, job, err := s.store.CreateDriverImport(ctx, b.Name, b.UploadId, imports.KindDriverImport)
	var dup *store.DuplicatePackageError
	switch {
	case errors.As(err, &dup):
		msg := fmt.Sprintf("This archive is already imported as %q.", dup.Package.Name)
		if dup.Package.Status == store.ImageImporting {
			msg = fmt.Sprintf("This archive is already being imported as %q.", dup.Package.Name)
		}
		if job, err = s.store.CreateFinishedJob(ctx, imports.KindDriverImport, dup.Package.ID, msg); err != nil {
			return nil, err
		}
		s.jobEvent(job)
		return api.ImportDriverPackage202JSONResponse{JobAcceptedJSONResponse: jobAccepted(job).JobAcceptedJSONResponse}, nil
	case errors.Is(err, store.ErrNotFound):
		return api.ImportDriverPackage404ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusNotFound, "not_found", "Not found", "No upload "+b.UploadId+".", nil)), nil
	case errors.Is(err, store.ErrUploadIncomplete):
		return api.ImportDriverPackage409ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusConflict, "upload_incomplete", "Upload incomplete", "The upload hasn't received all its bytes yet.", nil)), nil
	case errors.Is(err, store.ErrUploadInUse):
		return api.ImportDriverPackage409ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusConflict, "upload_in_use", "Upload in use", "Another import is using this upload.", nil)), nil
	case err != nil:
		return nil, err
	}
	s.events.Publish(events.Event{Type: events.DriverUpdated, ResourceID: pkg.ID})
	s.jobEvent(job)
	if s.jobs != nil {
		s.jobs.Wake()
	}
	return api.ImportDriverPackage202JSONResponse{JobAcceptedJSONResponse: jobAccepted(job).JobAcceptedJSONResponse}, nil
}

// ListDriverPackages implements GET /driver-packages.
func (s *Server) ListDriverPackages(ctx context.Context, req api.ListDriverPackagesRequestObject) (api.ListDriverPackagesResponseObject, error) {
	p := req.Params
	f := store.PackageFilter{}
	if p.Limit != nil {
		f.Limit = *p.Limit
	}
	if p.Cursor != nil {
		f.After = *p.Cursor
	}
	if p.Class != nil {
		f.Class = *p.Class
	}
	if p.Role != nil {
		f.Role = string(*p.Role)
	}
	if p.HardwareId != nil {
		f.HardwareID = *p.HardwareId
	}
	list, next, err := s.store.ListDriverPackages(ctx, f)
	if err != nil {
		return nil, err
	}
	out := api.ListDriverPackages200JSONResponse{Items: make([]api.DriverPackage, len(list))}
	for i, pkg := range list {
		out.Items[i] = packageToAPI(pkg)
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// GetDriverPackage implements GET /driver-packages/{driverPackageId}.
func (s *Server) GetDriverPackage(ctx context.Context, req api.GetDriverPackageRequestObject) (api.GetDriverPackageResponseObject, error) {
	pkg, err := s.store.DriverPackage(ctx, req.DriverPackageId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetDriverPackage404ApplicationProblemPlusJSONResponse(packageNotFound(req.DriverPackageId)), nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetDriverPackage200JSONResponse{Body: packageToAPI(pkg), Headers: api.GetDriverPackage200ResponseHeaders{ETag: versionETag(pkg.Version)}}, nil
}

// UpdateDriverPackage implements PATCH /driver-packages/{driverPackageId}.
func (s *Server) UpdateDriverPackage(ctx context.Context, req api.UpdateDriverPackageRequestObject) (api.UpdateDriverPackageResponseObject, error) {
	b := req.Body
	if b == nil {
		return api.UpdateDriverPackage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	if b.Name != nil && (*b.Name == "" || len(*b.Name) > 200) {
		return api.UpdateDriverPackage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("name must be 1 to 200 characters.")}, nil
	}
	enabled := map[string]bool{}
	if b.Infs != nil {
		for _, i := range *b.Infs {
			enabled[i.Id] = i.Enabled
		}
	}
	version, ok := parseETag(req.Params.IfMatch)
	if !ok {
		return api.UpdateDriverPackage412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	pkg, err := s.store.UpdateDriverPackage(ctx, req.DriverPackageId, b.Name, enabled, version)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.UpdateDriverPackage404ApplicationProblemPlusJSONResponse(packageNotFound(req.DriverPackageId)), nil
	case errors.Is(err, store.ErrVersionMismatch):
		return api.UpdateDriverPackage412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	case errors.Is(err, store.ErrUnknownINF):
		return api.UpdateDriverPackage400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusBadRequest, "unknown_inf", "Unknown INF", strings.TrimPrefix(err.Error(), "store: ")+" (not in this package).", nil))}, nil
	case err != nil:
		return nil, err
	}
	s.events.Publish(events.Event{Type: events.DriverUpdated, ResourceID: pkg.ID})
	return api.UpdateDriverPackage200JSONResponse(packageToAPI(pkg)), nil
}

// DeleteDriverPackage implements DELETE /driver-packages/{driverPackageId}.
func (s *Server) DeleteDriverPackage(ctx context.Context, req api.DeleteDriverPackageRequestObject) (api.DeleteDriverPackageResponseObject, error) {
	err := s.store.DeleteDriverPackage(ctx, req.DriverPackageId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.DeleteDriverPackage404ApplicationProblemPlusJSONResponse(packageNotFound(req.DriverPackageId)), nil
	case errors.Is(err, store.ErrPackageImporting):
		return api.DeleteDriverPackage409ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusConflict, "package_importing", "Package is importing", "Cancel its import job first.", nil)), nil
	case errors.Is(err, store.ErrPackageInUse):
		return api.DeleteDriverPackage409ApplicationProblemPlusJSONResponse(
			newProblem(http.StatusConflict, "package_in_use", "Package in use", "Remove it from every driver group first.", nil)), nil
	case err != nil:
		return nil, err
	}
	s.events.Publish(events.Event{Type: events.DriverUpdated, ResourceID: req.DriverPackageId, Data: map[string]any{"change": "deleted"}})
	if s.content != nil {
		if err := s.content.Sweep(context.WithoutCancel(ctx)); err != nil {
			slog.Warn("content sweep after driver package delete", "err", err)
		}
	}
	return api.DeleteDriverPackage204Response{}, nil
}

func packageNotFound(id string) api.Problem {
	return newProblem(http.StatusNotFound, "not_found", "Not found", "No driver package "+id+".", nil)
}

func versionETag(v int64) string { return strconv.Quote(strconv.FormatInt(v, 10)) }

func packageToAPI(p store.DriverPackage) api.DriverPackage {
	tag := versionETag(p.Version)
	out := api.DriverPackage{
		Id: &p.ID, Etag: &tag, CreatedAt: &p.CreatedAt, UpdatedAt: &p.UpdatedAt,
		Name: p.Name, Status: api.DriverPackageStatus(p.Status), SourceFilename: p.SourceFilename, UploadId: p.UploadID,
		SizeBytes: &p.SizeBytes, Sha256: &p.SHA256, Infs: make([]api.DriverInf, len(p.INFs)),
	}
	if p.Error != nil {
		var e api.Problem
		if json.Unmarshal(p.Error, &e) == nil {
			out.Error = &e
		}
	}
	if len(p.Warnings) > 0 {
		w := p.Warnings
		out.Warnings = &w
	}
	for i, d := range p.INFs {
		boot := d.Role == "storage"
		a := api.DriverInf{
			Id: &d.ID, Path: d.Path, Class: d.Class, Role: api.DriverRole(d.Role), BootCritical: &boot,
			Provider: d.Provider, Version: d.Version, Enabled: d.Enabled,
			Signature: api.DriverSignature(d.Signature), Signer: d.Signer, SignatureDetail: d.SignatureDetail,
			Architectures: make([]api.Architecture, len(d.Architectures)),
		}
		for j, arch := range d.Architectures {
			a.Architectures[j] = api.Architecture(arch)
		}
		if d.DriverDate != nil {
			if t, err := time.Parse(time.DateOnly, *d.DriverDate); err == nil {
				a.DriverDate = &openapi_types.Date{Time: t}
			}
		}
		if len(d.HardwareIDs) > 0 {
			h := d.HardwareIDs
			a.HardwareIds = &h
		}
		if len(d.TargetOSVersions) > 0 {
			v := d.TargetOSVersions
			a.TargetOsVersions = &v
		}
		out.Infs[i] = a
	}
	return out
}

// ---------------------------------------------------------------- groups

func groupInput(b *api.DriverGroupInput) (store.DriverGroupInput, string) {
	if b == nil {
		return store.DriverGroupInput{}, "A JSON body is required."
	}
	if b.Name == "" || len(b.Name) > 200 {
		return store.DriverGroupInput{}, "name must be 1 to 200 characters."
	}
	in := store.DriverGroupInput{Name: b.Name, PackageIDs: b.DriverPackageIds}
	if b.Description != nil {
		if len(*b.Description) > 2000 {
			return store.DriverGroupInput{}, "description is at most 2000 characters."
		}
		in.Description = *b.Description
	}
	if in.PackageIDs == nil {
		in.PackageIDs = []string{}
	}
	return in, ""
}

func unknownPackage(err error) api.Problem {
	return newProblem(http.StatusUnprocessableEntity, "unknown_driver_package", "Unknown driver package", strings.TrimPrefix(err.Error(), "store: ")+".", nil)
}

// CreateDriverGroup implements POST /driver-groups.
func (s *Server) CreateDriverGroup(ctx context.Context, req api.CreateDriverGroupRequestObject) (api.CreateDriverGroupResponseObject, error) {
	in, msg := groupInput(req.Body)
	if msg != "" {
		return api.CreateDriverGroup400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest(msg)}, nil
	}
	g, err := s.store.CreateDriverGroup(ctx, in)
	switch {
	case errors.Is(err, store.ErrUnknownPackage):
		return api.CreateDriverGroup422ApplicationProblemPlusJSONResponse(unknownPackage(err)), nil
	case errors.Is(err, store.ErrDuplicate):
		return api.CreateDriverGroup400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest(strings.TrimPrefix(err.Error(), "store: ") + ".")}, nil
	case err != nil:
		return nil, err
	}
	return api.CreateDriverGroup201JSONResponse(groupToAPI(g)), nil
}

// ListDriverGroups implements GET /driver-groups.
func (s *Server) ListDriverGroups(ctx context.Context, req api.ListDriverGroupsRequestObject) (api.ListDriverGroupsResponseObject, error) {
	limit, after := 0, ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		after = *req.Params.Cursor
	}
	list, next, err := s.store.ListDriverGroups(ctx, limit, after)
	if err != nil {
		return nil, err
	}
	out := api.ListDriverGroups200JSONResponse{Items: make([]api.DriverGroup, len(list))}
	for i, g := range list {
		out.Items[i] = groupToAPI(g)
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// GetDriverGroup implements GET /driver-groups/{driverGroupId}.
func (s *Server) GetDriverGroup(ctx context.Context, req api.GetDriverGroupRequestObject) (api.GetDriverGroupResponseObject, error) {
	g, err := s.store.DriverGroup(ctx, req.DriverGroupId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetDriverGroup404ApplicationProblemPlusJSONResponse(groupNotFound(req.DriverGroupId)), nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetDriverGroup200JSONResponse{Body: groupToAPI(g), Headers: api.GetDriverGroup200ResponseHeaders{ETag: versionETag(g.Version)}}, nil
}

// ReplaceDriverGroup implements PUT /driver-groups/{driverGroupId}.
func (s *Server) ReplaceDriverGroup(ctx context.Context, req api.ReplaceDriverGroupRequestObject) (api.ReplaceDriverGroupResponseObject, error) {
	in, msg := groupInput(req.Body)
	if msg != "" {
		return api.ReplaceDriverGroup400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest(msg)}, nil
	}
	version, ok := parseETag(req.Params.IfMatch)
	if !ok {
		return api.ReplaceDriverGroup412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	g, err := s.store.ReplaceDriverGroup(ctx, req.DriverGroupId, in, version)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.ReplaceDriverGroup404ApplicationProblemPlusJSONResponse(groupNotFound(req.DriverGroupId)), nil
	case errors.Is(err, store.ErrVersionMismatch):
		return api.ReplaceDriverGroup412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	case errors.Is(err, store.ErrUnknownPackage):
		return api.ReplaceDriverGroup422ApplicationProblemPlusJSONResponse(unknownPackage(err)), nil
	case errors.Is(err, store.ErrDuplicate):
		return api.ReplaceDriverGroup400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest(strings.TrimPrefix(err.Error(), "store: ") + ".")}, nil
	case err != nil:
		return nil, err
	}
	return api.ReplaceDriverGroup200JSONResponse(groupToAPI(g)), nil
}

// DeleteDriverGroup implements DELETE /driver-groups/{driverGroupId}.
// (409 arrives with profiles in Phase 4.)
func (s *Server) DeleteDriverGroup(ctx context.Context, req api.DeleteDriverGroupRequestObject) (api.DeleteDriverGroupResponseObject, error) {
	err := s.store.DeleteDriverGroup(ctx, req.DriverGroupId)
	if errors.Is(err, store.ErrNotFound) {
		return api.DeleteDriverGroup404ApplicationProblemPlusJSONResponse(groupNotFound(req.DriverGroupId)), nil
	}
	if err != nil {
		return nil, err
	}
	return api.DeleteDriverGroup204Response{}, nil
}

func groupNotFound(id string) api.Problem {
	return newProblem(http.StatusNotFound, "not_found", "Not found", "No driver group "+id+".", nil)
}

func groupToAPI(g store.DriverGroup) api.DriverGroup {
	tag := versionETag(g.Version)
	out := api.DriverGroup{Id: &g.ID, Etag: &tag, CreatedAt: &g.CreatedAt, UpdatedAt: &g.UpdatedAt, Name: g.Name, DriverPackageIds: g.PackageIDs}
	if out.DriverPackageIds == nil {
		out.DriverPackageIds = []string{}
	}
	if g.Description != "" {
		out.Description = &g.Description
	}
	return out
}
