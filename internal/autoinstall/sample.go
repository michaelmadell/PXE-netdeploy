package autoinstall

import "github.com/michaelmadell/pxe-netdeploy/internal/unattend"

// SampleReportBase stands for a deployment's report URL in previews.
const SampleReportBase = "http://netdeploy.example/content/report/********/"

// SampleHostname is the hostname sample renders use.
const SampleHostname = "nd-sample-01"

// SampleProfile is the profile sample renders use.
func SampleProfile() Profile {
	pw := "sample-password"
	return Profile{Name: "Sample Linux profile", Kind: "linux", BootOrderAfterDeploy: "disk-first", Variables: map[string]string{},
		Linux: &Settings{Install: InstallAutomatic, Account: &Account{Username: "sample", RealName: "Sample User", PasswordSecretID: &pw,
			SSHAuthorizedKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAISAMPLE sample@netdeploy"}}, Packages: []string{"htop"}}}
}

// SampleInput is the sample deployment data, secrets masked.
func SampleInput(vars map[string]string) Input {
	return Input{Arch: "amd64", Hostname: SampleHostname, PasswordHash: unattend.Mask, ReportBase: SampleReportBase, Image: ImageData{Distribution: "Ubuntu-Server", Version: "26.04.1"},
		Client: unattend.SampleClient, ProfileID: "sample-profile", DeploymentID: "preview", Variables: vars}
}

// CheckTemplate parses a template and checks it rendered with sample data
// twice: with a password, and with SSH keys only. The error is
// unattend.ErrTemplateSyntax or ErrTemplateRender (wrapped).
func CheckTemplate(spec unattend.TemplateSpec) (*Template, []unattend.Finding, error) {
	t, err := ParseTemplate(spec)
	if err != nil {
		return nil, nil, err
	}
	var fs []unattend.Finding
	if len(spec.IdentityModes) > 0 {
		fs = append(fs, unattend.Finding{Severity: unattend.SevError, Code: "template_identity_modes", Message: "Autoinstall templates have no identity modes."})
	}
	seen := map[string]bool{}
	for _, c := range []struct {
		label string
		hash  string
	}{{"with a password", unattend.Mask}, {"SSH keys only", ""}} {
		in := SampleInput(unattend.SampleVariables(spec))
		in.PasswordHash = c.hash
		d := Prepare(SampleProfile(), in)
		out, err := t.Execute(d)
		if err != nil {
			return nil, nil, err
		}
		for _, f := range Check(out, d) {
			key := f.Code + f.Message
			if seen[key] {
				continue
			}
			seen[key] = true
			f.Sample, f.Message = c.label, c.label+": "+f.Message
			fs = append(fs, f)
		}
	}
	return t, fs, nil
}
