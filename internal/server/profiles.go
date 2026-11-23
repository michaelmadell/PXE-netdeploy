package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/autoinstall"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

// Profiles (ADR 0024). Identity and secret checks come with deployments
// (decision 5).

// profileInput maps the API's profile to the store's, checking which fields
// the profile's kind allows (ADR 0034). A problem means the input is invalid.
func profileInput(b *api.ProfileInput) (store.ProfileInput, *api.Problem, error) {
	in := store.ProfileInput{
		Name: b.Name, Kind: store.ProfileWindows, InstallImageID: b.InstallImageId, SMBTargetID: b.SmbTargetId,
		UnattendTemplateID: b.UnattendTemplateId, OSDriverGroupIDs: []string{}, Variables: map[string]string{},
		BootOrderAfterDeploy: "disk-first",
	}
	if b.Kind != nil {
		in.Kind = string(*b.Kind)
	}
	if b.Description != nil {
		in.Description = *b.Description
	}
	if b.Variables != nil {
		in.Variables = *b.Variables
	}
	if in.Kind == store.ProfileLinux {
		var extra []fieldError
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"bootImageId", b.BootImageId != nil}, {"editionIndex", b.EditionIndex != nil}, {"installMethod", b.InstallMethod != nil},
			{"identity", b.Identity != nil}, {"osDriverGroupIds", b.OsDriverGroupIds != nil && len(*b.OsDriverGroupIds) > 0},
			{"smbTargetId", b.SmbTargetId != nil}, {"osSettings", b.OsSettings != nil},
		} {
			if f.set {
				extra = append(extra, fieldError{field: f.name, message: "not used by linux profiles"})
			}
		}
		if len(extra) > 0 {
			p := newProblem(http.StatusUnprocessableEntity, "not_for_kind", "Field not used by this kind of profile",
				"A linux profile has a name, a description, installImageId, variables, kernelArgs, linux, unattendTemplateId and bootOrderAfterDeploy (ADR 0034, 0035).", extra)
			return in, &p, nil
		}
		if b.BootOrderAfterDeploy != nil {
			in.BootOrderAfterDeploy = string(*b.BootOrderAfterDeploy)
		}
		if id := in.UnattendTemplateID; id != nil && (*id == "" || *id == autoinstall.BuiltinID) {
			in.UnattendTemplateID = nil
		} else if id != nil && *id == unattend.BuiltinID {
			p := newProblem(http.StatusUnprocessableEntity, "template_format_mismatch", "Template format mismatch",
				"The built-in unattend template is for Windows profiles; a linux profile uses an autoinstall template.", nil)
			return in, &p, nil
		}
		if b.Linux != nil {
			if p := linuxSettingsProblem(*b.Linux); p != nil {
				return in, p, nil
			}
			var err error
			if in.LinuxSettings, err = json.Marshal(b.Linux); err != nil {
				return in, nil, err
			}
		}
		if b.KernelArgs != nil {
			in.KernelArgs = *b.KernelArgs
		}
		if len(in.KernelArgs) > 1024 || strings.ContainsAny(in.KernelArgs, "\r\n\"") {
			p := newProblem(http.StatusUnprocessableEntity, "invalid_kernel_args", "Invalid kernel arguments",
				"kernelArgs must be at most 1024 characters, on one line, without double quotes.", nil)
			return in, &p, nil
		}
		return in, nil, nil
	}

	if b.KernelArgs != nil || b.Linux != nil {
		var fields []fieldError
		if b.KernelArgs != nil {
			fields = append(fields, fieldError{field: "kernelArgs", message: "not used by windows profiles"})
		}
		if b.Linux != nil {
			fields = append(fields, fieldError{field: "linux", message: "not used by windows profiles"})
		}
		p := newProblem(http.StatusUnprocessableEntity, "not_for_kind", "Field not used by this kind of profile",
			"kernelArgs and linux are for linux profiles.", fields)
		return in, &p, nil
	}
	if id := in.UnattendTemplateID; id != nil && *id == autoinstall.BuiltinID {
		p := newProblem(http.StatusUnprocessableEntity, "template_format_mismatch", "Template format mismatch",
			"The built-in autoinstall template is for linux profiles.", nil)
		return in, &p, nil
	}
	var missing []fieldError
	if b.BootImageId == nil {
		missing = append(missing, fieldError{field: "bootImageId", message: "required for windows profiles"})
	}
	if b.EditionIndex == nil {
		missing = append(missing, fieldError{field: "editionIndex", message: "required for windows profiles"})
	}
	if b.InstallMethod == nil {
		missing = append(missing, fieldError{field: "installMethod", message: "required for windows profiles"})
	}
	if b.Identity == nil {
		missing = append(missing, fieldError{field: "identity", message: "required for windows profiles"})
	}
	if len(missing) > 0 {
		p := newProblem(http.StatusUnprocessableEntity, "required_for_kind", "Field required for this kind of profile",
			"A windows profile needs a boot image, an edition, an install method and an identity.", missing)
		return in, &p, nil
	}
	identity, err := json.Marshal(b.Identity)
	if err != nil {
		return in, nil, err
	}
	in.BootImageID, in.EditionIndex, in.InstallMethod, in.Identity = *b.BootImageId, *b.EditionIndex, string(*b.InstallMethod), identity
	if id := in.UnattendTemplateID; id != nil && (*id == "" || isBuiltinID(*id)) {
		in.UnattendTemplateID = nil // the built-in template (ADR 0027)
	}
	if b.OsDriverGroupIds != nil {
		in.OSDriverGroupIDs = *b.OsDriverGroupIds
	}
	if b.BootOrderAfterDeploy != nil {
		in.BootOrderAfterDeploy = string(*b.BootOrderAfterDeploy)
	}
	if b.OsSettings != nil {
		if in.OSSettings, err = json.Marshal(b.OsSettings); err != nil {
			return in, nil, err
		}
	}
	return in, nil, nil
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
	if in.Kind == store.ProfileLinux {
		im, err := s.store.Image(ctx, in.InstallImageID)
		if errors.Is(err, store.ErrNotFound) {
			return profileProblem(http.StatusUnprocessableEntity, "install_image_unavailable", "Install image unavailable", "No image "+in.InstallImageID+"."), nil
		}
		if err != nil {
			return nil, err
		}
		if _, reason := linuxImage(im); reason != "" {
			return profileProblem(http.StatusUnprocessableEntity, "install_image_unavailable", "Install image unavailable", reason), nil
		}
		if id := in.UnattendTemplateID; id != nil {
			t, err := s.store.UnattendTemplate(ctx, *id)
			if errors.Is(err, store.ErrNotFound) {
				return profileProblem(http.StatusUnprocessableEntity, "unknown_unattend_template", "Unknown unattend template", "No template "+*id+"."), nil
			}
			if err != nil {
				return nil, err
			}
			if t.Format != store.FormatAutoinstall {
				return profileProblem(http.StatusUnprocessableEntity, "template_format_mismatch", "Template format mismatch",
					fmt.Sprintf("Template %q is an unattend template; a linux profile uses an autoinstall template.", t.Name)), nil
			}
		}
		return nil, nil
	}
	if in.InstallMethod == "smb" {
		return profileProblem(http.StatusUnprocessableEntity, "not_implemented", "Not implemented", "installMethod smb arrives with the agent's SMB install (Phase 4 decision 4)."), nil
	}
	var mode struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(in.Identity, &mode) == nil && mode.Mode == "entra" {
		return profileProblem(http.StatusUnprocessableEntity, "not_implemented", "Not implemented", "Identity entra arrives in Phase 5 (ADR 0026)."), nil
	}
	if id := in.UnattendTemplateID; id != nil {
		t, err := s.store.UnattendTemplate(ctx, *id)
		if errors.Is(err, store.ErrNotFound) {
			return profileProblem(http.StatusUnprocessableEntity, "unknown_unattend_template", "Unknown unattend template", "No unattend template "+*id+"."), nil
		}
		if err != nil {
			return nil, err
		}
		if t.Format == store.FormatAutoinstall {
			return profileProblem(http.StatusUnprocessableEntity, "template_format_mismatch", "Template format mismatch",
				fmt.Sprintf("Template %q is an autoinstall template; a windows profile uses an unattend template.", t.Name)), nil
		}
		if !templateSpec(t).Supports(mode.Mode) {
			return profileProblem(http.StatusUnprocessableEntity, "template_identity_mismatch", "Template doesn't render the identity mode",
				fmt.Sprintf("Template %q renders %s, not %s.", t.Name, strings.Join(t.IdentityModes, ", "), mode.Mode)), nil
		}
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
	case errors.Is(err, store.ErrNotFound) && strings.Contains(msg, "unattend template"):
		return profileProblem(http.StatusUnprocessableEntity, "unknown_unattend_template", "Unknown unattend template", msg+".")
	case errors.Is(err, store.ErrTemplateFormatMismatch):
		return profileProblem(http.StatusUnprocessableEntity, "template_format_mismatch", "Template format mismatch", msg+".")
	case errors.Is(err, store.ErrTemplateIdentityMismatch):
		return profileProblem(http.StatusUnprocessableEntity, "template_identity_mismatch", "Template doesn't render the identity mode", msg+".")
	}
	return nil
}

// CreateProfile implements POST /profiles.
func (s *Server) CreateProfile(ctx context.Context, req api.CreateProfileRequestObject) (api.CreateProfileResponseObject, error) {
	if req.Body == nil {
		return api.CreateProfile400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	in, p, err := profileInput(req.Body)
	if err != nil {
		return nil, err
	}
	if p == nil {
		if p, err = s.checkProfile(ctx, in); err != nil {
			return nil, err
		}
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
	in, p, err := profileInput(req.Body)
	if err != nil {
		return nil, err
	}
	if p == nil {
		if p, err = s.checkProfile(ctx, in); err != nil {
			return nil, err
		}
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
		case errors.Is(err, store.ErrKindChanged):
			return api.ReplaceProfile422ApplicationProblemPlusJSONResponse(*profileProblem(http.StatusUnprocessableEntity, "kind_changed",
				"A profile's kind can't change", "Make a new profile of the other kind.")), nil
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
		"name": p.Name, "kind": p.Kind, "installImageId": p.InstallImageID, "variables": p.Variables,
	}
	if p.Kind == store.ProfileLinux {
		if p.KernelArgs != "" {
			doc["kernelArgs"] = p.KernelArgs
		}
		doc["unattendTemplateId"], doc["bootOrderAfterDeploy"] = p.UnattendTemplateID, p.BootOrderAfterDeploy
		if p.LinuxSettings != nil {
			doc["linux"] = p.LinuxSettings
		}
	} else {
		doc["bootImageId"], doc["editionIndex"], doc["installMethod"], doc["identity"] = p.BootImageID, p.EditionIndex, p.InstallMethod, p.Identity
		doc["osDriverGroupIds"], doc["unattendTemplateId"], doc["smbTargetId"] = p.OSDriverGroupIDs, p.UnattendTemplateID, p.SMBTargetID
		doc["bootOrderAfterDeploy"] = p.BootOrderAfterDeploy
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

// linuxSettingsProblem checks a Linux profile's install settings (ADR
// 0035): an automatic install needs an account that isn't a system one,
// with a password or an SSH key, and a hostname pattern that parses.
func linuxSettingsProblem(l api.LinuxSettings) *api.Problem {
	unproc := func(code, title, detail, field string) *api.Problem {
		p := newProblem(http.StatusUnprocessableEntity, code, title, detail, []fieldError{{field: field, message: detail}})
		return &p
	}
	if l.HostnamePattern != nil && strings.TrimSpace(*l.HostnamePattern) != "" {
		if _, err := unattend.ParseName(*l.HostnamePattern); err != nil {
			return unproc("invalid_hostname_pattern", "Invalid hostname pattern", err.Error(), "linux.hostnamePattern")
		}
	}
	if l.Install != nil && *l.Install == api.LinuxSettingsInstallInteractive {
		return nil
	}
	a := l.Account
	if a == nil || (ptrOr(a.PasswordSecretId, "") == "" && (a.SshAuthorizedKeys == nil || len(*a.SshAuthorizedKeys) == 0)) {
		return unproc("linux_account_required", "Account required",
			"An automatic install creates an account: give linux.account a username and a password secret or an SSH key.", "linux.account")
	}
	if autoinstall.ReservedUsername(a.Username) {
		return unproc("reserved_account_name", "Reserved account name", fmt.Sprintf("%q is a system account; choose another username.", a.Username), "linux.account.username")
	}
	if a.SshAuthorizedKeys != nil {
		for _, k := range *a.SshAuthorizedKeys {
			if f := strings.Fields(k); len(f) < 2 || strings.ContainsAny(k, "\r\n") {
				return unproc("invalid_ssh_key", "Invalid SSH key", "Each SSH key is one line: type, key and an optional comment.", "linux.account.sshAuthorizedKeys")
			}
		}
	}
	return nil
}
