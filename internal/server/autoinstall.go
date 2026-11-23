package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/autoinstall"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

// Automated Linux installs (ADR 0035): autoinstall templates, the checks
// arming runs, and the NoCloud seed the installer fetches.

// SeedPrefix is where content listeners serve deployments' NoCloud seeds:
// /content/seed/<deployment token>/<file>.
const SeedPrefix = "/content/seed/"

// linuxImageInput is what autoinstall data takes from an install image.
func (s *Server) linuxImageInput(ctx context.Context, imageID string) (arch string, img autoinstall.ImageData) {
	arch = "amd64"
	im, err := s.store.Image(ctx, imageID)
	if err != nil {
		return arch, img
	}
	var d api.LinuxImageDetails
	if json.Unmarshal(im.Details, &d) != nil {
		return arch, img
	}
	if d.Architecture == api.ArchitectureArm64 {
		arch = "arm64"
	}
	return arch, autoinstall.ImageData{Distribution: ptrOr(d.Distribution, ""), Version: ptrOr(d.Version, "")}
}

// renderAutoinstall renders user-data and checks it.
func renderAutoinstall(lp autoinstall.Profile, spec unattend.TemplateSpec, in autoinstall.Input) ([]byte, []unattend.Finding, error) {
	t, err := autoinstall.ParseTemplate(spec)
	if err != nil {
		return nil, nil, err
	}
	d := autoinstall.Prepare(lp, in)
	out, err := t.Execute(d)
	if err != nil {
		return nil, nil, err
	}
	return out, autoinstall.Check(out, d), nil
}

// linuxHostname renders a Linux profile's hostname for a client.
func linuxHostname(lp autoinstall.Profile, c unattend.ClientData, clientVars map[string]string) (string, error) {
	return autoinstall.RenderHostname(lp.Settings().HostnamePattern, unattend.NameData{
		Client:    unattend.NameClient{Serial: c.Serial, MAC: c.MAC, SMBIOSUUID: c.SMBIOSUUID, Hostname: c.Hostname},
		Variables: unattend.MergeVariables(lp.Variables, clientVars)})
}

// prepareAutoinstall adds an automatic Linux install's hostname, secrets,
// template and variables to a new deployment, after rendering what the
// client would get with secrets masked. A problem refuses the deployment.
func (s *Server) prepareAutoinstall(ctx context.Context, c store.Client, p store.Profile, snap json.RawMessage, nd *store.NewDeployment) (*api.Problem, error) {
	lp, err := autoinstall.ParseProfile(snap)
	if err != nil {
		return nil, err
	}
	if !lp.Automatic() {
		return nil, nil
	}
	cd := clientData(c)
	name, err := linuxHostname(lp, cd, c.Variables)
	if err != nil {
		pr := newProblem(http.StatusUnprocessableEntity, "invalid_hostname", "Invalid hostname", err.Error(), nil)
		return &pr, nil
	}
	nd.ComputerName = &name
	nd.Secrets = lp.Secrets()
	spec, err := s.linuxTemplateFor(ctx, p.UnattendTemplateID)
	if err != nil {
		return nil, err
	}
	vars := unattend.MergeVariables(spec.VariableDefaults, lp.Variables, c.Variables)
	if missing := unattend.MissingVariables(spec, vars); len(missing) > 0 {
		pr := newProblem(http.StatusUnprocessableEntity, "missing_variable", "Missing variable",
			fmt.Sprintf("Autoinstall template %q requires %s; set it on the profile or client %s.", spec.Name, strings.Join(missing, ", "), c.MAC), nil)
		return &pr, nil
	}
	arch, img := s.linuxImageInput(ctx, p.InstallImageID)
	hash := ""
	if len(nd.Secrets) > 0 {
		hash = unattend.Mask
	}
	_, fs, err := renderAutoinstall(lp, spec, autoinstall.Input{Arch: arch, Hostname: name, PasswordHash: hash, Image: img, Client: cd,
		ProfileID: p.ID, DeploymentID: "preview", Variables: vars})
	if err != nil {
		pr := newProblem(http.StatusUnprocessableEntity, "template_render", "Template fails to render", err.Error(), nil)
		return &pr, nil
	}
	var tfs []finding
	for _, f := range fs {
		tfs = append(tfs, finding{f.Severity, f.Code, fmt.Sprintf("Autoinstall template %q: %s", spec.Name, findingMessage(f))})
	}
	if len(errorsIn(tfs)) > 0 {
		pr := findingsProblem(p.Name, tfs)
		return &pr, nil
	}
	if nd.UnattendTemplate, err = json.Marshal(spec); err != nil {
		return nil, err
	}
	nd.Variables = vars
	return nil, nil
}

// validateAutoinstall adds an automatic Linux profile's findings: the
// account's secret, the hostname and the template rendered for a sample
// client.
func (s *Server) validateAutoinstall(ctx context.Context, p store.Profile, fs []finding) ([]finding, error) {
	snap, err := profileSnapshot(p)
	if err != nil {
		return fs, err
	}
	lp, err := autoinstall.ParseProfile(snap)
	if err != nil || !lp.Automatic() {
		return fs, err
	}
	add := func(sev, code, format string, a ...any) { fs = append(fs, finding{sev, code, fmt.Sprintf(format, a...)}) }
	hash := ""
	for _, id := range lp.Secrets() {
		sec, err := s.store.Secret(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			add("error", "unknown_secret", "The account's password secret %s doesn't exist.", id)
		case err != nil:
			return fs, err
		case sec.Kind != "password":
			add("error", "secret_kind", "Secret %q is a %s, not a password.", sec.Name, sec.Kind)
		default:
			hash = unattend.Mask
		}
	}
	if _, err := linuxHostname(lp, unattend.SampleClient, nil); err != nil {
		add("error", "invalid_hostname", "With a sample client: %v.", err)
	}
	spec, err := s.linuxTemplateFor(ctx, p.UnattendTemplateID)
	if errors.Is(err, store.ErrNotFound) {
		add("error", "unknown_unattend_template", "The profile's autoinstall template no longer exists.")
		return fs, nil
	}
	if err != nil {
		return fs, err
	}
	arch, img := s.linuxImageInput(ctx, p.InstallImageID)
	vars := unattend.MergeVariables(spec.VariableDefaults, lp.Variables)
	for _, k := range spec.RequiredVariables {
		if vars[k] == "" {
			add("warning", "missing_variable", "Autoinstall template %q requires %s; set it here or on each client.", spec.Name, k)
			vars[k] = "sample-" + k
		}
	}
	_, tfs, err := renderAutoinstall(lp, spec, autoinstall.Input{Arch: arch, Hostname: autoinstall.SampleHostname, PasswordHash: hash, Image: img,
		Client: unattend.SampleClient, ProfileID: p.ID, DeploymentID: "preview", Variables: vars})
	if err != nil {
		add("error", "template_render", "Autoinstall template %q: %v", spec.Name, err)
		return fs, nil
	}
	for _, f := range tfs {
		add(f.Severity, f.Code, "Autoinstall template %q: %s", spec.Name, findingMessage(f))
	}
	return fs, nil
}

// previewAutoinstall renders an autoinstall template with secrets masked:
// for a profile (and client) when the request names one, else the sample.
func (s *Server) previewAutoinstall(ctx context.Context, spec unattend.TemplateSpec, req api.UnattendRenderRequest, saveChecks bool) (api.UnattendRenderResult, *api.Problem, error) {
	fail := func(status int, code, title, detail string) (api.UnattendRenderResult, *api.Problem, error) {
		p := newProblem(status, code, title, detail, nil)
		return api.UnattendRenderResult{}, &p, nil
	}
	var fs []unattend.Finding
	if saveChecks {
		_, sfs, err := autoinstall.CheckTemplate(spec)
		switch {
		case errors.Is(err, unattend.ErrTemplateSyntax):
			return fail(http.StatusUnprocessableEntity, "template_syntax", "Template doesn't parse", err.Error())
		case err != nil:
			return fail(http.StatusUnprocessableEntity, "template_render", "Template fails to render", err.Error())
		}
		fs = sfs
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
	lp := autoinstall.SampleProfile()
	in := autoinstall.SampleInput(nil)
	in.Client = client
	if req.ProfileId != nil && *req.ProfileId != "" {
		sp, err := s.store.Profile(ctx, *req.ProfileId)
		if errors.Is(err, store.ErrNotFound) {
			return fail(http.StatusNotFound, "not_found", "Not found", "No profile "+*req.ProfileId+".")
		}
		if err != nil {
			return api.UnattendRenderResult{}, nil, err
		}
		if sp.Kind != store.ProfileLinux {
			return fail(http.StatusUnprocessableEntity, "template_format_mismatch", "Template format mismatch",
				fmt.Sprintf("Profile %q is a Windows profile; autoinstall templates are for Linux profiles.", sp.Name))
		}
		snap, err := profileSnapshot(sp)
		if err != nil {
			return api.UnattendRenderResult{}, nil, err
		}
		if lp, err = autoinstall.ParseProfile(snap); err != nil {
			return api.UnattendRenderResult{}, nil, err
		}
		in.ProfileID = sp.ID
		in.Arch, in.Image = s.linuxImageInput(ctx, sp.InstallImageID)
		in.PasswordHash = ""
		if len(lp.Secrets()) > 0 {
			in.PasswordHash = unattend.Mask
		}
		if name, err := linuxHostname(lp, client, clientVars); err != nil {
			fs = append(fs, unattend.Finding{Severity: unattend.SevWarning, Code: "invalid_hostname", Message: err.Error()})
		} else {
			in.Hostname = name
		}
		vars := unattend.MergeVariables(spec.VariableDefaults, lp.Variables, clientVars, extra)
		for _, k := range unattend.MissingVariables(spec, vars) {
			fs = append(fs, unattend.Finding{Severity: unattend.SevError, Code: "missing_variable",
				Message: fmt.Sprintf("Variable %q has no value (the template requires it); previewed as sample-%s.", k, k)})
			vars[k] = "sample-" + k
		}
		in.Variables = vars
	} else {
		in.Variables = unattend.MergeVariables(unattend.SampleVariables(spec), clientVars, extra)
	}
	out, cfs, err := renderAutoinstall(lp, spec, in)
	if errors.Is(err, unattend.ErrTemplateSyntax) {
		return fail(http.StatusUnprocessableEntity, "template_syntax", "Template doesn't parse", err.Error())
	}
	if err != nil {
		return fail(http.StatusUnprocessableEntity, "template_render", "Template fails to render", err.Error())
	}
	fs = append(fs, cfs...)
	return api.UnattendRenderResult{Content: string(out), Ok: len(unattend.Errors(fs)) == 0, Findings: findingsAPI(dedupe(fs))}, nil, nil
}

// LinuxSeed serves a running automatic Linux deployment's NoCloud seed
// (ADR 0035): user-data rendered now (the password hashed with a new
// salt), meta-data, and an empty vendor-data. network-config is 404, which
// cloud-init takes as "none". status is the HTTP status.
func (s *Server) LinuxSeed(r *http.Request, token, file string) ([]byte, int, []any) {
	ctx := r.Context()
	t, err := s.store.DeploymentToken(ctx, tokenHash(token), 0)
	if errors.Is(err, store.ErrTokenInvalid) {
		return nil, http.StatusNotFound, []any{"reason", "unknown or expired seed token"}
	}
	if err != nil {
		return nil, http.StatusInternalServerError, []any{"err", err.Error()}
	}
	d, err := s.store.Deployment(ctx, *t.DeploymentID)
	if err != nil {
		return nil, http.StatusInternalServerError, []any{"err", err.Error()}
	}
	attrs := []any{"deployment_id", d.ID, "seed_file", file}
	lp, err := autoinstall.ParseProfile(d.ProfileSnapshot)
	if err != nil || lp.Kind != store.ProfileLinux || !lp.Automatic() {
		return nil, http.StatusNotFound, append(attrs, "reason", "not an automatic Linux deployment")
	}
	if err := s.store.TouchDeployment(ctx, d.ID); err != nil {
		slog.Warn("record installer activity", "svc", "agent", "deployment_id", d.ID, "err", err)
	}
	switch file {
	case "meta-data":
		return []byte(fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", yamlQuote(d.ID), yamlQuote(ptrOr(d.ComputerName, "")))), http.StatusOK, attrs
	case "vendor-data":
		return []byte{}, http.StatusOK, attrs
	case "user-data":
	default:
		return nil, http.StatusNotFound, append(attrs, "reason", "no such seed file")
	}
	b, err := s.deploymentUserData(ctx, d, lp)
	if err != nil {
		return nil, http.StatusInternalServerError, append(attrs, "err", err.Error())
	}
	return b, http.StatusOK, attrs
}

// deploymentUserData renders a deployment's user-data with its real
// secrets, from what it copied when it was created.
func (s *Server) deploymentUserData(ctx context.Context, d store.Deployment, lp autoinstall.Profile) ([]byte, error) {
	spec := autoinstall.Builtin()
	if len(d.UnattendTemplate) > 0 {
		if err := json.Unmarshal(d.UnattendTemplate, &spec); err != nil {
			return nil, fmt.Errorf("deployment %s template: %w", d.ID, err)
		}
	}
	values, err := s.deploymentSecretValues(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	hash := ""
	for _, id := range lp.Secrets() {
		v, ok := values[id]
		if !ok {
			return nil, fmt.Errorf("deployment %s has no copy of secret %s", d.ID, id)
		}
		hash = autoinstall.HashPassword(string(v.value))
	}
	c, err := s.store.Client(ctx, d.ClientID)
	if err != nil {
		return nil, err
	}
	arch, img := s.linuxImageInput(ctx, lp.InstallImageID)
	t, err := autoinstall.ParseTemplate(spec)
	if err != nil {
		return nil, err
	}
	return t.Execute(autoinstall.Prepare(lp, autoinstall.Input{Arch: arch, Hostname: ptrOr(d.ComputerName, ""), PasswordHash: hash, Image: img,
		Client: clientData(c), ProfileID: d.ProfileID, DeploymentID: d.ID, Variables: d.Variables}))
}

// yamlQuote is s as a YAML double-quoted scalar.
func yamlQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
