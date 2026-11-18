package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

// finding is one ValidationResult finding.
type finding struct{ severity, code, message string }

func errorsIn(fs []finding) []finding {
	var out []finding
	for _, f := range fs {
		if f.severity == "error" {
			out = append(out, f)
		}
	}
	return out
}

// validateProfile checks a profile is deployable (ADR 0026): what
// POST /profiles/{id}/validate reports and what arming refuses on.
func (s *Server) validateProfile(ctx context.Context, p store.Profile) ([]finding, error) {
	var fs []finding
	add := func(sev, code, format string, a ...any) {
		fs = append(fs, finding{sev, code, fmt.Sprintf(format, a...)})
	}

	// References: the same checks as creating the profile.
	if prob, err := s.checkProfile(ctx, p.ProfileInput); err != nil {
		return nil, err
	} else if prob != nil {
		msg := prob.Title
		if prob.Detail != nil {
			msg = *prob.Detail
		}
		add("error", prob.Code, "%s", msg)
	}

	// Boot image.
	if bi, err := s.store.BootImage(ctx, p.BootImageID); err == nil {
		status := "draft"
		if s.boot != nil {
			status, _ = s.boot.Status(ctx, bi)
		} else if bi.TreeID != nil {
			status = "ready" // no builder service (tests): a built tree is ready
		}
		switch status {
		case "ready":
		case "stale":
			add("error", "boot_image_stale", "Boot image %q is out of date; build it again.", bi.Name)
		default:
			add("error", "boot_image_not_ready", "Boot image %q is %s; build it first.", bi.Name, status)
		}
		var bm struct {
			Signer string `json:"signer"`
		}
		if bi.SelectedBootManager != nil && json.Unmarshal(bi.SelectedBootManager, &bm) == nil && bm.Signer != "" && bm.Signer != "windows-uefi-ca-2023" {
			add("warning", "boot_manager_2011_only", "Boot image %q serves a PCA 2011-signed boot manager; machines that have revoked it won't boot.", bi.Name)
		}
		// The agent in WinPE calls the boot image's serverUrls: something
		// must serve HTTP there, or machines boot and then find nothing.
		if s.settings != nil && !agentReachable(bi.ServerURLs, s.settings.HTTPEndpoints()) {
			add("warning", "agent_unreachable", "None of boot image %q's server addresses (%s) is served by a deployment interface with HTTP on. Machines will start Windows PE, then fail to reach this server. Turn HTTP on for the interface at that address and port in Settings, or change the boot image's server addresses and rebuild.", bi.Name, strings.Join(bi.ServerURLs, ", "))
		}
		// OS drivers need DISM offline servicing, which plain WinPE can't run
		// (ADR 0022 amendment): refuse before a disk is wiped.
		if len(p.OSDriverGroupIDs) > 0 && bi.TreeID != nil {
			switch {
			case bi.CanAddOSDrivers == nil:
				add("warning", "boot_image_cannot_add_drivers", "Boot image %q was built before netdeploy recorded whether it can add OS drivers; build it again to check.", bi.Name)
			case !*bi.CanAddOSDrivers:
				add("error", "boot_image_cannot_add_drivers", "Boot image %q is plain Windows PE (boot.wim index %d), which can't add OS drivers. Use the Windows Setup image (index 2 on install media), or remove the profile's OS driver groups.", bi.Name, bi.SourceIndex)
			}
		}
		if s.settings != nil {
			if cfg, _ := s.settings.Current(); bi.TFTPBlockSize > cfg.TFTP.MaxBlockSize || bi.TFTPWindowSize > cfg.TFTP.MaxWindowSize {
				add("warning", "tftp_capped_by_server", "Boot image %q asks for TFTP %d/%d; the server caps it at %d/%d.",
					bi.Name, bi.TFTPBlockSize, bi.TFTPWindowSize, cfg.TFTP.MaxBlockSize, cfg.TFTP.MaxWindowSize)
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// Identity and secrets.
	snap, err := profileSnapshot(p)
	if err != nil {
		return nil, err
	}
	up, err := unattend.Parse(snap)
	if err != nil {
		return nil, err
	}
	id := up.Identity
	switch id.Mode {
	case unattend.ModeEntra:
		add("error", "not_implemented", "Identity entra arrives in Phase 5.")
	case unattend.ModeLocal:
		names := map[string]bool{}
		for _, a := range id.Accounts {
			names[a.Name] = true
		}
		if id.AutoLogon != nil && id.AutoLogon.Account != "" && !names[id.AutoLogon.Account] {
			add("error", "unknown_autologon_account", "autoLogon account %q isn't one of the accounts.", id.AutoLogon.Account)
		}
	case unattend.ModeAD:
		switch id.JoinMethod {
		case unattend.JoinCredentials:
			if id.Domain == "" {
				add("error", "missing_domain", "An ad credentials join needs a domain.")
			}
			if id.JoinCredentialSecretID == nil || *id.JoinCredentialSecretID == "" {
				add("error", "missing_secret", "An ad credentials join needs joinCredentialSecretId.")
			}
			// Setup finds the domain controller through DNS during specialize.
			if s.settings != nil {
				cfg, _ := s.settings.Current()
				for _, ifc := range cfg.Interfaces {
					if ifc.DHCP.Mode == "full" && ifc.DHCP.Pool != nil && len(ifc.DHCP.Pool.DNSServers) == 0 {
						add("warning", "dhcp_no_dns", "Interface %q hands out addresses with no DNS server: clients there can't find the domain %s to join it. Set the pool's dnsServers (and router) to reach a domain controller.", ifc.Name, id.Domain)
					}
				}
			}
		case unattend.JoinODJ:
			if up.OS.ComputerNamePattern != nil && *up.OS.ComputerNamePattern != "" {
				add("warning", "name_pattern_ignored", "With offline domain join the ODJ blob names the computer; computerNamePattern is ignored.")
			}
		}
	}
	// Setup can't add a local account with a built-in account's name: the
	// built-in Administrator stays disabled, so the break-glass sign-in fails.
	var accounts []string
	for _, a := range id.Accounts {
		accounts = append(accounts, a.Name)
	}
	if id.LocalAdmin != nil {
		accounts = append(accounts, id.LocalAdmin.Name)
	}
	for _, n := range accounts {
		switch strings.ToLower(n) {
		case "administrator", "guest", "defaultaccount", "wdagutilityaccount":
			add("error", "reserved_account_name", "%q is a built-in Windows account; Setup won't create it. Choose another name.", n)
		}
	}
	for _, ref := range up.Secrets(nil) {
		x, err := s.store.Secret(ctx, ref.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			add("error", "missing_secret", "The %s names secret %s, which doesn't exist.", ref.What, ref.ID)
		case err != nil:
			return nil, err
		case x.Kind != ref.Kind:
			add("error", "wrong_secret_kind", "The %s must be a %s secret; %q is a %s.", ref.What, ref.Kind, x.Name, x.Kind)
		}
	}
	pattern := ""
	if up.OS.ComputerNamePattern != nil {
		pattern = *up.OS.ComputerNamePattern
	}
	if pattern != "" {
		if _, err := unattend.ParseName(pattern); err != nil {
			add("error", "invalid_name_pattern", "%v", err)
			pattern = ""
		}
	}

	// The unattend template, rendered for the profile with a sample client
	// and secrets masked (ADR 0027).
	spec, terr := s.templateFor(ctx, p.UnattendTemplateID)
	switch {
	case errors.Is(terr, store.ErrNotFound):
		spec = unattend.TemplateSpec{} // checkProfile reported it
	case terr != nil:
		return nil, terr
	case !spec.Supports(id.Mode):
		if id.Mode != unattend.ModeEntra {
			add("error", "template_identity_mismatch", "Unattend template %q doesn't render identity mode %s.", spec.Name, id.Mode)
		}
	default:
		for _, f := range s.checkUnattend(ctx, spec, p, up, nil) {
			fs = append(fs, finding{f.Severity, f.Code, "Unattend template " + strconv.Quote(spec.Name) + ": " + findingMessage(f)})
		}
	}

	// Per assigned client.
	clients, err := s.clientsWithProfile(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	odj := id.Mode == unattend.ModeAD && id.JoinMethod == unattend.JoinODJ
	for _, c := range clients {
		if odj && c.ODJBlobSecretID == nil {
			add("warning", "missing_odj_blob", "Client %s has no ODJ blob (secretRefs.odjBlob).", c.MAC)
		}
		if pattern != "" && !odj {
			if _, err := unattend.RenderName(pattern, nameData(c, up)); err != nil {
				add("warning", "invalid_computer_name", "Client %s: %v", c.MAC, err)
			}
		}
		if spec.Content != "" {
			for _, k := range unattend.MissingVariables(spec, unattend.MergeVariables(spec.VariableDefaults, up.Variables, c.Variables)) {
				add("warning", "missing_variable", "Client %s: variable %q has no value; unattend template %q requires it, so arming will be refused.", c.MAC, k, spec.Name)
			}
		}
	}
	return fs, nil
}

func (s *Server) clientsWithProfile(ctx context.Context, profileID string) ([]store.Client, error) {
	var out []store.Client
	after := ""
	for {
		page, next, err := s.store.ListClients(ctx, store.ClientFilter{ProfileID: profileID, Limit: 200, After: after})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			return out, nil
		}
		after = next
	}
}

// checkUnattend renders the profile's template with secrets masked and
// checks the output (ADR 0027): for client c, or with the sample client
// when c is nil (required variables the client would give get sample
// values). Rendering problems become error findings.
func (s *Server) checkUnattend(ctx context.Context, spec unattend.TemplateSpec, p store.Profile, up unattend.Profile, c *store.Client) []unattend.Finding {
	errf := func(code, format string, a ...any) []unattend.Finding {
		return []unattend.Finding{{Severity: unattend.SevError, Code: code, Message: fmt.Sprintf(format, a...)}}
	}
	tpl, err := unattend.ParseTemplate(spec)
	if err != nil {
		return errf("template_syntax", "%v", err)
	}
	client, cvars := unattend.SampleClient, map[string]string(nil)
	if c != nil {
		client, cvars = clientData(*c), c.Variables
	}
	vars := unattend.MergeVariables(spec.VariableDefaults, up.Variables, cvars)
	if c == nil {
		for _, k := range unattend.MissingVariables(spec, vars) {
			vars[k] = "sample-" + k
		}
	}
	in := unattend.Input{Arch: s.imageArch(ctx, p.InstallImageID), InstallMethod: p.InstallMethod, Masked: true, Client: client,
		ProfileID: p.ID, DeploymentID: "preview", Variables: vars, Template: tpl}
	if pat := up.OS.ComputerNamePattern; pat != nil && *pat != "" {
		nd := unattend.NameData{Client: unattend.NameClient{Serial: client.Serial, MAC: client.MAC, SMBIOSUUID: client.SMBIOSUUID,
			Hostname: client.Hostname}, Variables: unattend.MergeVariables(up.Variables, cvars)}
		if name, err := unattend.RenderName(*pat, nd); err == nil {
			in.ComputerName = &name
		}
	}
	d, err := unattend.Prepare(up, in)
	if err != nil {
		return nil // the identity checks above report what's wrong with the profile
	}
	out, err := tpl.Execute(d)
	if err != nil {
		return errf("template_render", "%v", err)
	}
	return unattend.Check(out, d)
}

// nameData is what a computer name pattern sees for a client.
func nameData(c store.Client, p unattend.Profile) unattend.NameData {
	vars := map[string]string{}
	for k, v := range p.Variables {
		vars[k] = v
	}
	for k, v := range c.Variables {
		vars[k] = v
	}
	return unattend.NameData{Client: unattend.NameClient{
		Serial: ptrOr(c.Serial, ""), MAC: c.MAC, SMBIOSUUID: ptrOr(c.SMBIOSUUID, ""), Hostname: ptrOr(c.Hostname, ""),
	}, Variables: vars}
}

func findingsToAPI(fs []finding) api.ValidationResult {
	out := api.ValidationResult{Ok: len(errorsIn(fs)) == 0, Findings: []api.ValidationFinding{}}
	for _, f := range fs {
		out.Findings = append(out.Findings, api.ValidationFinding{Code: f.code, Message: f.message, Severity: api.ValidationFindingSeverity(f.severity)})
	}
	return out
}

// findingsProblem is 422 profile_invalid with the error findings.
func findingsProblem(name string, fs []finding) api.Problem {
	var fields []fieldError
	for _, f := range errorsIn(fs) {
		fields = append(fields, fieldError{f.code, f.message})
	}
	return newProblem(http.StatusUnprocessableEntity, "profile_invalid", "Profile can't be deployed",
		fmt.Sprintf("Profile %q has %d problem(s); see errors or POST /profiles/{id}/validate.", name, len(fields)), fields)
}

// ValidateProfile implements POST /profiles/{profileId}/validate.
func (s *Server) ValidateProfile(ctx context.Context, req api.ValidateProfileRequestObject) (api.ValidateProfileResponseObject, error) {
	p, err := s.store.Profile(ctx, req.ProfileId)
	if errors.Is(err, store.ErrNotFound) {
		return api.ValidateProfile404ApplicationProblemPlusJSONResponse(profileNotFound(req.ProfileId)), nil
	}
	if err != nil {
		return nil, err
	}
	fs, err := s.validateProfile(ctx, p)
	if err != nil {
		return nil, err
	}
	return api.ValidateProfile200JSONResponse(findingsToAPI(fs)), nil
}
