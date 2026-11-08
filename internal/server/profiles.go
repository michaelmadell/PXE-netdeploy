package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Profiles (ADR 0024). Identity and secret checks come with deployments
// (decision 5).

func profileInput(b *api.ProfileInput) (store.ProfileInput, error) {
	identity, err := json.Marshal(b.Identity)
	if err != nil {
		return store.ProfileInput{}, err
	}
	in := store.ProfileInput{
		Name: b.Name, BootImageID: b.BootImageId, InstallImageID: b.InstallImageId, EditionIndex: b.EditionIndex,
		InstallMethod: string(b.InstallMethod), SMBTargetID: b.SmbTargetId, UnattendTemplateID: b.UnattendTemplateId,
		Identity: identity, BootOrderAfterDeploy: "disk-first", OSDriverGroupIDs: []string{}, Variables: map[string]string{},
	}
	if b.Description != nil {
		in.Description = *b.Description
	}
	if b.OsDriverGroupIds != nil {
		in.OSDriverGroupIDs = *b.OsDriverGroupIds
	}
	if b.Variables != nil {
		in.Variables = *b.Variables
	}
	if b.BootOrderAfterDeploy != nil {
		in.BootOrderAfterDeploy = string(*b.BootOrderAfterDeploy)
	}
	if b.OsSettings != nil {
		if in.OSSettings, err = json.Marshal(b.OsSettings); err != nil {
			return store.ProfileInput{}, err
		}
	}
	return in, nil
}

func profileProblem(status int, code, title, detail string) *api.Problem {
	p := newProblem(status, code, title, detail, nil)
	return &p
}

// checkProfile checks what a profile refers to. A nil problem means valid.
func (s *Server) checkProfile(ctx context.Context, in store.ProfileInput) (*api.Problem, error) {
	if in.Name == "" || len(in.Name) > 200 {
		return profileProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "name must be 1 to 200 characters."), nil
	}
	if in.InstallMethod == "smb" {
		return profileProblem(http.StatusUnprocessableEntity, "not_implemented", "Not implemented", "installMethod smb arrives with the agent's SMB install (Phase 4 decision 4)."), nil
	}
	if in.UnattendTemplateID != nil {
		return profileProblem(http.StatusUnprocessableEntity, "not_implemented", "Not implemented", "Unattend templates arrive in Phase 5; use null for the built-in template."), nil
	}
	bi, err := s.store.BootImage(ctx, in.BootImageID)
	if errors.Is(err, store.ErrNotFound) {
		return profileProblem(http.StatusUnprocessableEntity, "unknown_boot_image", "Unknown boot image", "No boot image "+in.BootImageID+"."), nil
	}
	if err != nil {
		return nil, err
	}
	im, err := s.store.Image(ctx, in.InstallImageID)
	if errors.Is(err, store.ErrNotFound) {
		return profileProblem(http.StatusUnprocessableEntity, "install_image_unavailable", "Install image unavailable", "No image "+in.InstallImageID+"."), nil
	}
	if err != nil {
		return nil, err
	}
	if im.Status != store.ImageReady || im.Kind == nil || *im.Kind != "windows" {
		return profileProblem(http.StatusUnprocessableEntity, "install_image_unavailable", "Install image unavailable",
			fmt.Sprintf("Image %q is not a ready Windows image.", im.Name)), nil
	}
	var d struct {
		Architecture string `json:"architecture"`
		Editions     []struct {
			Index int    `json:"index"`
			Name  string `json:"name"`
		} `json:"editions"`
	}
	if err := json.Unmarshal(im.Details, &d); err != nil {
		return nil, fmt.Errorf("image %s details: %w", im.ID, err)
	}
	if d.Architecture != bi.Architecture {
		return profileProblem(http.StatusUnprocessableEntity, "architecture_mismatch", "Architecture mismatch",
			fmt.Sprintf("Image %q is %s but boot image %q is %s.", im.Name, d.Architecture, bi.Name, bi.Architecture)), nil
	}
	var editions []string
	found := false
	for _, e := range d.Editions {
		editions = append(editions, fmt.Sprintf("%d (%s)", e.Index, e.Name))
		found = found || e.Index == in.EditionIndex
	}
	if !found {
		return profileProblem(http.StatusUnprocessableEntity, "unknown_edition", "Unknown edition",
			fmt.Sprintf("Image %q has editions %s.", im.Name, strings.Join(editions, ", "))), nil
	}
	return nil, nil
}

func profileStoreError(err error) *api.Problem {
	msg := strings.TrimPrefix(err.Error(), "store: ")
	switch {
	case errors.Is(err, store.ErrUnknownDriverGroup):
		return profileProblem(http.StatusUnprocessableEntity, "unknown_driver_group", "Unknown driver group", msg+".")
	case errors.Is(err, store.ErrDuplicate):
		return profileProblem(http.StatusBadRequest, "invalid_request", "Invalid request", msg+".")
	case errors.Is(err, store.ErrNotFound) && strings.Contains(msg, "boot image"):
		return profileProblem(http.StatusUnprocessableEntity, "unknown_boot_image", "Unknown boot image", msg+".")
	case errors.Is(err, store.ErrNotFound) && strings.Contains(msg, "install image"):
		return profileProblem(http.StatusUnprocessableEntity, "install_image_unavailable", "Install image unavailable", msg+".")
	}
	return nil
}

// CreateProfile implements POST /profiles.
func (s *Server) CreateProfile(ctx context.Context, req api.CreateProfileRequestObject) (api.CreateProfileResponseObject, error) {
	if req.Body == nil {
		return api.CreateProfile400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	in, err := profileInput(req.Body)
	if err != nil {
		return nil, err
	}
	p, err := s.checkProfile(ctx, in)
	if err != nil {
		return nil, err
	}
	if p == nil {
		var pr store.Profile
		if pr, err = s.store.CreateProfile(ctx, in); err == nil {
			s.events.Publish(events.Event{Type: events.ProfileUpdated, ResourceID: pr.ID})
			out, err := profileToAPI(pr)
			if err != nil {
				return nil, err
			}
			return api.CreateProfile201JSONResponse(out), nil
		}
		if p = profileStoreError(err); p == nil {
			return nil, err
		}
	}
	if p.Status == http.StatusBadRequest {
		return api.CreateProfile400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(*p)}, nil
	}
	return api.CreateProfile422ApplicationProblemPlusJSONResponse(*p), nil
}

// ListProfiles implements GET /profiles.
func (s *Server) ListProfiles(ctx context.Context, req api.ListProfilesRequestObject) (api.ListProfilesResponseObject, error) {
	limit, after := 0, ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		after = *req.Params.Cursor
	}
	list, next, err := s.store.ListProfiles(ctx, limit, after)
	if err != nil {
		return nil, err
	}
	out := api.ListProfiles200JSONResponse{Items: make([]api.Profile, len(list))}
	for i, p := range list {
		if out.Items[i], err = profileToAPI(p); err != nil {
			return nil, err
		}
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// GetProfile implements GET /profiles/{profileId}.
func (s *Server) GetProfile(ctx context.Context, req api.GetProfileRequestObject) (api.GetProfileResponseObject, error) {
	p, err := s.store.Profile(ctx, req.ProfileId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetProfile404ApplicationProblemPlusJSONResponse(profileNotFound(req.ProfileId)), nil
	}
	if err != nil {
		return nil, err
	}
	out, err := profileToAPI(p)
	if err != nil {
		return nil, err
	}
	return api.GetProfile200JSONResponse(out), nil
}

// ReplaceProfile implements PUT /profiles/{profileId}.
func (s *Server) ReplaceProfile(ctx context.Context, req api.ReplaceProfileRequestObject) (api.ReplaceProfileResponseObject, error) {
	if req.Body == nil {
		return api.ReplaceProfile400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	version, ok := parseETag(req.Params.IfMatch)
	if !ok {
		return api.ReplaceProfile412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	in, err := profileInput(req.Body)
	if err != nil {
		return nil, err
	}
	p, err := s.checkProfile(ctx, in)
	if err != nil {
		return nil, err
	}
	if p == nil {
		pr, err := s.store.ReplaceProfile(ctx, req.ProfileId, in, version)
		switch {
		case err == nil:
			s.events.Publish(events.Event{Type: events.ProfileUpdated, ResourceID: pr.ID})
			out, err := profileToAPI(pr)
			if err != nil {
				return nil, err
			}
			return api.ReplaceProfile200JSONResponse(out), nil
		case errors.Is(err, store.ErrVersionMismatch):
			return api.ReplaceProfile412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
		case err == store.ErrNotFound:
			return api.ReplaceProfile404ApplicationProblemPlusJSONResponse(profileNotFound(req.ProfileId)), nil
		}
		if p = profileStoreError(err); p == nil {
			return nil, err
		}
	}
	if p.Status == http.StatusBadRequest {
		return api.ReplaceProfile400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(*p)}, nil
	}
	return api.ReplaceProfile422ApplicationProblemPlusJSONResponse(*p), nil
}

// DeleteProfile implements DELETE /profiles/{profileId}.
func (s *Server) DeleteProfile(ctx context.Context, req api.DeleteProfileRequestObject) (api.DeleteProfileResponseObject, error) {
	err := s.store.DeleteProfile(ctx, req.ProfileId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.DeleteProfile404ApplicationProblemPlusJSONResponse(profileNotFound(req.ProfileId)), nil
	case errors.Is(err, store.ErrProfileInUse):
		return api.DeleteProfile409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "profile_in_use", "Profile in use",
			"The profile is in use: "+strings.TrimPrefix(err.Error(), store.ErrProfileInUse.Error()+": ")+".", nil)), nil
	case err != nil:
		return nil, err
	}
	s.events.Publish(events.Event{Type: events.ProfileUpdated, ResourceID: req.ProfileId, Data: map[string]any{"change": "deleted"}})
	return api.DeleteProfile204Response{}, nil
}

func profileNotFound(id string) api.Problem {
	return newProblem(http.StatusNotFound, "not_found", "Not found", "No profile "+id+".", nil)
}

// profileToAPI builds the API object through JSON: the stored identity and
// OS settings are the API's own documents.
func profileToAPI(p store.Profile) (api.Profile, error) {
	tag := versionETag(p.Version)
	doc := map[string]any{
		"id": p.ID, "etag": tag, "createdAt": p.CreatedAt, "updatedAt": p.UpdatedAt,
		"name": p.Name, "bootImageId": p.BootImageID, "installImageId": p.InstallImageID, "editionIndex": p.EditionIndex,
		"osDriverGroupIds": p.OSDriverGroupIDs, "unattendTemplateId": p.UnattendTemplateID, "variables": p.Variables,
		"installMethod": p.InstallMethod, "smbTargetId": p.SMBTargetID, "identity": p.Identity,
		"bootOrderAfterDeploy": p.BootOrderAfterDeploy,
	}
	if p.Description != "" {
		doc["description"] = p.Description
	}
	if p.OSSettings != nil {
		doc["osSettings"] = p.OSSettings
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return api.Profile{}, err
	}
	var out api.Profile
	err = json.Unmarshal(b, &out)
	return out, err
}
