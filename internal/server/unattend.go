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
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

// Unattend templates (ADR 0027): CRUD, previews, and the template a profile
// or deployment uses.

func templateSpec(t store.UnattendTemplate) unattend.TemplateSpec {
	return unattend.TemplateSpec{ID: t.ID, Name: t.Name, Description: t.Description, ETag: versionETag(t.Version), Format: t.Format,
		Content: t.Content, IdentityModes: t.IdentityModes, VariableDefaults: t.VariableDefaults, RequiredVariables: t.RequiredVariables}
}

func templateToAPI(spec unattend.TemplateSpec, t *store.UnattendTemplate) api.UnattendTemplate {
	builtIn := spec.BuiltIn
	format := api.UnattendFormat(templateFormat(spec))
	out := api.UnattendTemplate{Id: &spec.ID, BuiltIn: &builtIn, Name: spec.Name, Content: spec.Content,
		IdentityModes: []api.IdentityMode{}, Format: &format}
	if spec.Description != "" {
		out.Description = &spec.Description
	}
	for _, m := range spec.IdentityModes {
		out.IdentityModes = append(out.IdentityModes, api.IdentityMode(m))
	}
	defaults := api.Variables(spec.VariableDefaults)
	if defaults == nil {
		defaults = api.Variables{}
	}
	required := append([]string{}, spec.RequiredVariables...)
	out.VariableDefaults, out.RequiredVariables = &defaults, &required
	if t != nil {
		out.Etag, out.CreatedAt, out.UpdatedAt = &spec.ETag, &t.CreatedAt, &t.UpdatedAt
	} else {
		tag := `"` + spec.ID + `"`
		out.Etag = &tag
	}
	return out
}

func templateInput(b api.UnattendTemplateInput) store.UnattendTemplateInput {
	in := store.UnattendTemplateInput{Name: strings.TrimSpace(b.Name), Content: b.Content, VariableDefaults: map[string]string{}, RequiredVariables: []string{}}
	if b.Description != nil {
		in.Description = *b.Description
	}
	in.Format = store.FormatUnattend
	if b.Format != nil {
		in.Format = string(*b.Format)
	}
	for _, m := range b.IdentityModes {
		in.IdentityModes = append(in.IdentityModes, string(m))
	}
	if b.VariableDefaults != nil {
		in.VariableDefaults = *b.VariableDefaults
	}
	if b.RequiredVariables != nil {
		in.RequiredVariables = *b.RequiredVariables
	}
	return in
}

func inputSpec(in store.UnattendTemplateInput) unattend.TemplateSpec {
	return unattend.TemplateSpec{Name: in.Name, Description: in.Description, Format: in.Format, Content: in.Content, IdentityModes: in.IdentityModes,
		VariableDefaults: in.VariableDefaults, RequiredVariables: in.RequiredVariables}
}

func isBuiltinID(id string) bool { return id == unattend.BuiltinID || id == autoinstall.BuiltinID }

// templateFormat is a template's format ("" is unattend).
func templateFormat(spec unattend.TemplateSpec) string {
	if spec.Format == "" {
		return store.FormatUnattend
	}
	return spec.Format
}

// builtinTemplate is a built-in template by id.
func builtinTemplate(id string) unattend.TemplateSpec {
	if id == autoinstall.BuiltinID {
		return autoinstall.Builtin()
	}
	return unattend.Builtin()
}

// templateFor returns the unattend template a Windows profile names: null
// or builtin is the built-in one. store.ErrNotFound if it doesn't exist.
func (s *Server) templateFor(ctx context.Context, id *string) (unattend.TemplateSpec, error) {
	if id == nil || *id == "" || *id == unattend.BuiltinID {
		return unattend.Builtin(), nil
	}
	return s.storedTemplate(ctx, *id)
}

// linuxTemplateFor returns the autoinstall template a Linux profile names
// (ADR 0035): null or builtin-autoinstall is the built-in one.
func (s *Server) linuxTemplateFor(ctx context.Context, id *string) (unattend.TemplateSpec, error) {
	if id == nil || *id == "" || *id == autoinstall.BuiltinID {
		return autoinstall.Builtin(), nil
	}
	return s.storedTemplate(ctx, *id)
}

// templateByID is any template, built-in or stored.
func (s *Server) templateByID(ctx context.Context, id string) (unattend.TemplateSpec, error) {
	if isBuiltinID(id) {
		return builtinTemplate(id), nil
	}
	return s.storedTemplate(ctx, id)
}

func (s *Server) storedTemplate(ctx context.Context, id string) (unattend.TemplateSpec, error) {
	t, err := s.store.UnattendTemplate(ctx, id)
	if err != nil {
		return unattend.TemplateSpec{}, err
	}
	return templateSpec(t), nil
}

// deploymentTemplate parses the template a deployment copied (the built-in
// one for deployments from before templates).
func deploymentTemplate(d store.Deployment) (*unattend.Template, error) {
	spec := unattend.Builtin()
	if len(d.UnattendTemplate) > 0 {
		if err := json.Unmarshal(d.UnattendTemplate, &spec); err != nil {
			return nil, fmt.Errorf("deployment %s unattend template: %w", d.ID, err)
		}
	}
	return unattend.ParseTemplate(spec)
}

func snapshotInstallMethod(snap json.RawMessage) string {
	var p struct {
		InstallMethod string `json:"installMethod"`
	}
	json.Unmarshal(snap, &p)
	return p.InstallMethod
}

func clientData(c store.Client) unattend.ClientData {
	return unattend.ClientData{ID: c.ID, MAC: c.MAC, Serial: ptrOr(c.Serial, ""), SMBIOSUUID: ptrOr(c.SMBIOSUUID, ""), Hostname: ptrOr(c.Hostname, "")}
}

// templateProblem checks a template for saving (ADR 0027): 422
// template_syntax, template_render, or template_invalid with the error
// findings. Nil: it can be saved.
func templateProblem(in store.UnattendTemplateInput) *api.Problem {
	unproc := func(code, title, detail string, fields []fieldError) *api.Problem {
		p := newProblem(http.StatusUnprocessableEntity, code, title, detail, fields)
		return &p
	}
	if in.Name == "" || len(in.Name) > 200 {
		p := newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "name must be 1 to 200 characters.", nil)
		return &p
	}
	var fs []unattend.Finding
	var err error
	switch in.Format {
	case store.FormatAutoinstall:
		if len(in.IdentityModes) > 0 {
			return unproc("not_for_format", "Field not used by this format", "Autoinstall templates have no identityModes.",
				[]fieldError{{"identityModes", "must be empty for autoinstall templates"}})
		}
		_, fs, err = autoinstall.CheckTemplate(inputSpec(in))
	default:
		if len(in.IdentityModes) == 0 {
			return unproc("identity_modes_required", "Identity modes required", "An unattend template renders at least one identity mode.",
				[]fieldError{{"identityModes", "at least one"}})
		}
		_, fs, err = unattend.CheckTemplate(inputSpec(in))
	}
	switch {
	case errors.Is(err, unattend.ErrTemplateSyntax):
		return unproc("template_syntax", "Template doesn't parse", err.Error(), nil)
	case errors.Is(err, unattend.ErrTemplateRender):
		return unproc("template_render", "Template fails to render", err.Error(), nil)
	case err != nil:
		return unproc("template_render", "Template fails to render", err.Error(), nil)
	}
	var fields []fieldError
	for _, f := range unattend.Errors(fs) {
		fields = append(fields, fieldError{f.Code, findingMessage(f)})
	}
	if len(fields) > 0 {
		return unproc("template_invalid", "Template is invalid", fmt.Sprintf("The template has %d problem(s); see errors.", len(fields)), fields)
	}
	return nil
}

func findingMessage(f unattend.Finding) string {
	if f.Line > 0 {
		return fmt.Sprintf("%s (line %d)", f.Message, f.Line)
	}
	return f.Message
}

func templateNotFound(id string) api.Problem {
	return newProblem(http.StatusNotFound, "not_found", "Not found", "No unattend template "+id+".", nil)
}

func builtInConflict() api.Problem {
	return newProblem(http.StatusConflict, "built_in", "Built-in template", "The built-in templates are read-only; create a copy to customise one.", nil)
}

// ListUnattendTemplates implements GET /unattend-templates: the built-in
// template first, then the stored ones.
func (s *Server) ListUnattendTemplates(ctx context.Context, req api.ListUnattendTemplatesRequestObject) (api.ListUnattendTemplatesResponseObject, error) {
	limit, after := 0, ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		after = *req.Params.Cursor
	}
	list, next, err := s.store.ListUnattendTemplates(ctx, limit, after)
	if err != nil {
		return nil, err
	}
	out := api.ListUnattendTemplates200JSONResponse{Items: []api.UnattendTemplate{}}
	if after == "" {
		out.Items = append(out.Items, templateToAPI(unattend.Builtin(), nil), templateToAPI(autoinstall.Builtin(), nil))
	}
	for i := range list {
		out.Items = append(out.Items, templateToAPI(templateSpec(list[i]), &list[i]))
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// GetUnattendTemplate implements GET /unattend-templates/{templateId}.
func (s *Server) GetUnattendTemplate(ctx context.Context, req api.GetUnattendTemplateRequestObject) (api.GetUnattendTemplateResponseObject, error) {
	if isBuiltinID(req.TemplateId) {
		return api.GetUnattendTemplate200JSONResponse(templateToAPI(builtinTemplate(req.TemplateId), nil)), nil
	}
	t, err := s.store.UnattendTemplate(ctx, req.TemplateId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetUnattendTemplate404ApplicationProblemPlusJSONResponse(templateNotFound(req.TemplateId)), nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetUnattendTemplate200JSONResponse(templateToAPI(templateSpec(t), &t)), nil
}

// CreateUnattendTemplate implements POST /unattend-templates.
func (s *Server) CreateUnattendTemplate(ctx context.Context, req api.CreateUnattendTemplateRequestObject) (api.CreateUnattendTemplateResponseObject, error) {
	if req.Body == nil {
		return api.CreateUnattendTemplate400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	in := templateInput(*req.Body)
	if p := templateProblem(in); p != nil {
		if p.Status == http.StatusBadRequest {
			return api.CreateUnattendTemplate400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(*p)}, nil
		}
		return api.CreateUnattendTemplate422ApplicationProblemPlusJSONResponse(*p), nil
	}
	t, err := s.store.CreateUnattendTemplate(ctx, in)
	if err != nil {
		return nil, err
	}
	return api.CreateUnattendTemplate201JSONResponse(templateToAPI(templateSpec(t), &t)), nil
}

// ReplaceUnattendTemplate implements PUT /unattend-templates/{templateId}.
func (s *Server) ReplaceUnattendTemplate(ctx context.Context, req api.ReplaceUnattendTemplateRequestObject) (api.ReplaceUnattendTemplateResponseObject, error) {
	if isBuiltinID(req.TemplateId) {
		return api.ReplaceUnattendTemplate409ApplicationProblemPlusJSONResponse(builtInConflict()), nil
	}
	if req.Body == nil {
		return api.ReplaceUnattendTemplate400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	version, ok := parseETag(req.Params.IfMatch)
	if !ok {
		return api.ReplaceUnattendTemplate412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	in := templateInput(*req.Body)
	if p := templateProblem(in); p != nil {
		if p.Status == http.StatusBadRequest {
			return api.ReplaceUnattendTemplate400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(*p)}, nil
		}
		return api.ReplaceUnattendTemplate422ApplicationProblemPlusJSONResponse(*p), nil
	}
	t, err := s.store.ReplaceUnattendTemplate(ctx, req.TemplateId, in, version)
	switch {
	case errors.Is(err, store.ErrVersionMismatch):
		return api.ReplaceUnattendTemplate412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	case errors.Is(err, store.ErrFormatChanged):
		return api.ReplaceUnattendTemplate422ApplicationProblemPlusJSONResponse(newProblem(http.StatusUnprocessableEntity, "format_changed",
			"A template's format can't change", "Create a new template of the other format.", nil)), nil
	case errors.Is(err, store.ErrTemplateInUse):
		return api.ReplaceUnattendTemplate409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "template_in_use", "Template in use",
			strings.TrimPrefix(err.Error(), store.ErrTemplateInUse.Error()+": ")+".", nil)), nil
	case errors.Is(err, store.ErrNotFound):
		return api.ReplaceUnattendTemplate404ApplicationProblemPlusJSONResponse(templateNotFound(req.TemplateId)), nil
	case err != nil:
		return nil, err
	}
	return api.ReplaceUnattendTemplate200JSONResponse(templateToAPI(templateSpec(t), &t)), nil
}

// DeleteUnattendTemplate implements DELETE /unattend-templates/{templateId}.
func (s *Server) DeleteUnattendTemplate(ctx context.Context, req api.DeleteUnattendTemplateRequestObject) (api.DeleteUnattendTemplateResponseObject, error) {
	if isBuiltinID(req.TemplateId) {
		return api.DeleteUnattendTemplate409ApplicationProblemPlusJSONResponse(builtInConflict()), nil
	}
	err := s.store.DeleteUnattendTemplate(ctx, req.TemplateId)
	switch {
	case errors.Is(err, store.ErrTemplateInUse):
		return api.DeleteUnattendTemplate409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "template_in_use", "Template in use",
			strings.TrimPrefix(err.Error(), store.ErrTemplateInUse.Error()+": ")+".", nil)), nil
	case errors.Is(err, store.ErrNotFound):
		return api.DeleteUnattendTemplate404ApplicationProblemPlusJSONResponse(templateNotFound(req.TemplateId)), nil
	case err != nil:
		return nil, err
	}
	return api.DeleteUnattendTemplate204Response{}, nil
}

// RenderUnattendTemplate implements POST /unattend-templates/{templateId}/render.
func (s *Server) RenderUnattendTemplate(ctx context.Context, req api.RenderUnattendTemplateRequestObject) (api.RenderUnattendTemplateResponseObject, error) {
	spec, err := s.templateByID(ctx, req.TemplateId)
	if errors.Is(err, store.ErrNotFound) {
		return api.RenderUnattendTemplate404ApplicationProblemPlusJSONResponse(templateNotFound(req.TemplateId)), nil
	}
	if err != nil {
		return nil, err
	}
	var body api.UnattendRenderRequest
	if req.Body != nil {
		body = *req.Body
	}
	res, prob, err := s.preview(ctx, spec, body, false)
	switch {
	case err != nil:
		return nil, err
	case prob != nil && prob.Status == http.StatusNotFound:
		return api.RenderUnattendTemplate404ApplicationProblemPlusJSONResponse(*prob), nil
	case prob != nil:
		return api.RenderUnattendTemplate422ApplicationProblemPlusJSONResponse(*prob), nil
	}
	return api.RenderUnattendTemplate200JSONResponse(res), nil
}

// RenderUnattendDraft implements POST /unattend-templates/render.
func (s *Server) RenderUnattendDraft(ctx context.Context, req api.RenderUnattendDraftRequestObject) (api.RenderUnattendDraftResponseObject, error) {
	if req.Body == nil {
		return api.RenderUnattendDraft400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	b := req.Body
	spec := inputSpec(templateInput(b.Template))
	res, prob, err := s.preview(ctx, spec, api.UnattendRenderRequest{ProfileId: b.ProfileId, ClientId: b.ClientId, IdentityMode: b.IdentityMode, Variables: b.Variables}, true)
	switch {
	case err != nil:
		return nil, err
	case prob != nil && prob.Status == http.StatusNotFound:
		return api.RenderUnattendDraft404ApplicationProblemPlusJSONResponse(*prob), nil
	case prob != nil:
		return api.RenderUnattendDraft422ApplicationProblemPlusJSONResponse(*prob), nil
	}
	return api.RenderUnattendDraft200JSONResponse(res), nil
}

// preview renders a template with secrets masked: for a profile (and
// client) when the request names one, else with the identity mode's sample
// data. saveChecks adds the checks saving runs (drafts).
func (s *Server) preview(ctx context.Context, spec unattend.TemplateSpec, req api.UnattendRenderRequest, saveChecks bool) (api.UnattendRenderResult, *api.Problem, error) {
	fail := func(status int, code, title, detail string) (api.UnattendRenderResult, *api.Problem, error) {
		p := newProblem(status, code, title, detail, nil)
		return api.UnattendRenderResult{}, &p, nil
	}
	if templateFormat(spec) == store.FormatAutoinstall {
		return s.previewAutoinstall(ctx, spec, req, saveChecks)
	}
	var fs []unattend.Finding
	if saveChecks {
		_, sfs, err := unattend.CheckTemplate(spec)
		switch {
		case errors.Is(err, unattend.ErrTemplateSyntax):
			return fail(http.StatusUnprocessableEntity, "template_syntax", "Template doesn't parse", err.Error())
		case err != nil:
			return fail(http.StatusUnprocessableEntity, "template_render", "Template fails to render", err.Error())
		}
		fs = sfs
	}
	tpl, err := unattend.ParseTemplate(spec)
	if err != nil {
		return fail(http.StatusUnprocessableEntity, "template_syntax", "Template doesn't parse", err.Error())
	}
	client := unattend.SampleClient
	var clientVars map[string]string
	if req.ClientId != nil && *req.ClientId != "" {
		c, err := s.store.Client(ctx, *req.ClientId)
		if errors.Is(err, store.ErrNotFound) {
			return fail(http.StatusNotFound, "not_found", "Not found", "No client "+*req.ClientId+".")
		}
		if err != nil {
			return api.UnattendRenderResult{}, nil, err
		}
		client, clientVars = clientData(c), c.Variables
	}
	var extra map[string]string
	if req.Variables != nil {
		extra = *req.Variables
	}

	var (
		p    unattend.Profile
		in   unattend.Input
		mode string
	)
	if req.ProfileId != nil && *req.ProfileId != "" {
		sp, err := s.store.Profile(ctx, *req.ProfileId)
		if errors.Is(err, store.ErrNotFound) {
			return fail(http.StatusNotFound, "not_found", "Not found", "No profile "+*req.ProfileId+".")
		}
		if err != nil {
			return api.UnattendRenderResult{}, nil, err
		}
		snap, err := profileSnapshot(sp)
		if err != nil {
			return api.UnattendRenderResult{}, nil, err
		}
		if p, err = unattend.Parse(snap); err != nil {
			return api.UnattendRenderResult{}, nil, err
		}
		mode = p.Identity.Mode
		if req.IdentityMode != nil && string(*req.IdentityMode) != mode {
			return fail(http.StatusUnprocessableEntity, "identity_mode_unsupported", "Identity mode mismatch",
				fmt.Sprintf("Profile %q uses identity mode %s, not %s.", sp.Name, mode, *req.IdentityMode))
		}
		if !spec.Supports(mode) {
			return fail(http.StatusUnprocessableEntity, "identity_mode_unsupported", "Identity mode not supported",
				fmt.Sprintf("Template %q doesn't render identity mode %s (profile %q).", spec.Name, mode, sp.Name))
		}
		vars := unattend.MergeVariables(spec.VariableDefaults, p.Variables, clientVars, extra)
		for _, k := range unattend.MissingVariables(spec, vars) {
			fs = append(fs, unattend.Finding{Severity: unattend.SevError, Code: "missing_variable",
				Message: fmt.Sprintf("Variable %q has no value (the template requires it); previewed as sample-%s.", k, k)})
			vars[k] = "sample-" + k
		}
		in = unattend.Input{Arch: s.imageArch(ctx, sp.InstallImageID), InstallMethod: sp.InstallMethod, Masked: true,
			Client: client, ProfileID: sp.ID, DeploymentID: "preview", Variables: vars, Template: tpl}
		if pat := p.OS.ComputerNamePattern; pat != nil && *pat != "" {
			name, err := unattend.RenderName(*pat, unattend.NameData{Client: unattend.NameClient{Serial: client.Serial, MAC: client.MAC,
				SMBIOSUUID: client.SMBIOSUUID, Hostname: client.Hostname}, Variables: unattend.MergeVariables(p.Variables, clientVars)})
			if err != nil {
				fs = append(fs, unattend.Finding{Severity: unattend.SevWarning, Code: "invalid_computer_name", Message: err.Error()})
			} else {
				in.ComputerName = &name
			}
		}
	} else {
		mode = firstMode(spec)
		if req.IdentityMode != nil {
			mode = string(*req.IdentityMode)
		}
		if !spec.Supports(mode) {
			return fail(http.StatusUnprocessableEntity, "identity_mode_unsupported", "Identity mode not supported",
				fmt.Sprintf("Template %q doesn't render identity mode %s.", spec.Name, mode))
		}
		sample, ok := unattend.SampleProfile(mode)
		if !ok {
			return fail(http.StatusUnprocessableEntity, "identity_mode_unsupported", "Identity mode not supported",
				"Templates don't support identity mode "+mode+" yet.")
		}
		p = sample
		in = unattend.SampleInput(tpl, unattend.MergeVariables(unattend.SampleVariables(spec), clientVars, extra))
		in.Client = client
	}
	d, err := unattend.Prepare(p, in)
	if err != nil {
		return fail(http.StatusUnprocessableEntity, "profile_invalid", "Profile can't be rendered", err.Error())
	}
	out, err := tpl.Execute(d)
	if err != nil {
		return fail(http.StatusUnprocessableEntity, "template_render", "Template fails to render", err.Error())
	}
	fs = append(fs, unattend.Check(out, d)...)
	im := api.IdentityMode(mode)
	return api.UnattendRenderResult{Content: string(out), IdentityMode: &im, Ok: len(unattend.Errors(fs)) == 0,
		Findings: findingsAPI(dedupe(fs))}, nil, nil
}

func firstMode(spec unattend.TemplateSpec) string {
	for _, m := range []string{unattend.ModeNone, unattend.ModeLocal, unattend.ModeAD} {
		if spec.Supports(m) {
			return m
		}
	}
	if len(spec.IdentityModes) > 0 {
		return spec.IdentityModes[0]
	}
	return unattend.ModeNone
}

// dedupe drops a finding the save checks already reported for a sample.
func dedupe(fs []unattend.Finding) []unattend.Finding {
	seen := map[string]bool{}
	var out []unattend.Finding
	for _, f := range fs {
		msg := strings.TrimPrefix(f.Message, f.Sample+": ")
		key := f.Code + "|" + fmt.Sprint(f.Line) + "|" + msg
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

func findingsAPI(fs []unattend.Finding) []api.ValidationFinding {
	out := []api.ValidationFinding{}
	for _, f := range fs {
		v := api.ValidationFinding{Code: f.Code, Message: f.Message, Severity: api.ValidationFindingSeverity(f.Severity)}
		if f.Line > 0 {
			line := f.Line
			v.Line = &line
		}
		out = append(out, v)
	}
	return out
}
