package unattend

import (
	"fmt"
	"sort"
)

// Sample data for checking a template when it is saved or previewed
// without a profile (ADR 0027).

// SampleClient stands in for a client in previews.
var SampleClient = ClientData{ID: "sample-client", MAC: "00:15:5d:00:00:01", Serial: "SAMPLE0001",
	SMBIOSUUID: "00000000-0000-0000-0000-000000000001", Hostname: "sample-host"}

// SampleComputerName is the computer name sample renders use.
const SampleComputerName = "ND-SAMPLE-01"

// sampleCase is one identity mode (and join method) to render.
type sampleCase struct {
	label, profile string
}

var sampleCases = map[string][]sampleCase{
	ModeNone: {{"none", `{"name":"Sample profile","identity":{"mode":"none"},"osSettings":{"productKeySecretId":"sample"}}`}},
	ModeLocal: {{"local", `{"name":"Sample profile","identity":{"mode":"local","accounts":[
		{"name":"nd-sample-admin","displayName":"Sample administrator","group":"Administrators","passwordSecretId":"sample"},
		{"name":"nd-sample-user","group":"Users","passwordSecretId":"sample"}],
		"autoLogon":{"account":"nd-sample-admin","count":1}},"osSettings":{"productKeySecretId":"sample"}}`}},
	ModeAD: {
		{"ad (credentials)", `{"name":"Sample profile","identity":{"mode":"ad","joinMethod":"credentials","domain":"sample.example",
			"organizationalUnit":"OU=Sample,DC=sample,DC=example","joinCredentialSecretId":"sample",
			"localAdmin":{"name":"nd-sample-admin","passwordSecretId":"sample"}},"osSettings":{"productKeySecretId":"sample"}}`},
		{"ad (offline join)", `{"name":"Sample profile","identity":{"mode":"ad","joinMethod":"odj",
			"localAdmin":{"name":"nd-sample-admin","passwordSecretId":"sample"}},"osSettings":{"productKeySecretId":"sample"}}`},
	},
}

// SampleVariables are the template's defaults plus a sample value for
// each required variable without one.
func SampleVariables(t TemplateSpec) map[string]string {
	vars := MergeVariables(t.VariableDefaults)
	for _, k := range t.RequiredVariables {
		if vars[k] == "" {
			vars[k] = "sample-" + k
		}
	}
	return vars
}

// SampleInput is a preview's input for a sample profile.
func SampleInput(t *Template, vars map[string]string) Input {
	name := SampleComputerName
	return Input{Arch: "amd64", InstallMethod: "agent", Masked: true, ComputerName: &name, Client: SampleClient,
		ProfileID: "sample-profile", DeploymentID: "preview", Variables: vars, Template: t}
}

// SampleProfile is the sample profile for an identity mode (for ad, the
// credentials join).
func SampleProfile(mode string) (Profile, bool) {
	cs, ok := sampleCases[mode]
	if !ok {
		return Profile{}, false
	}
	p, err := Parse([]byte(cs[0].profile))
	if err != nil {
		panic(err)
	}
	return p, true
}

// CheckTemplate parses a template and renders it with sample data for
// each of its identity modes (ad with both join methods), checking each
// output. The error is ErrTemplateSyntax or ErrTemplateRender (wrapped);
// otherwise the findings say what's wrong, labelled by sample.
func CheckTemplate(spec TemplateSpec) (*Template, []Finding, error) {
	t, err := ParseTemplate(spec)
	if err != nil {
		return nil, nil, err
	}
	var fs []Finding
	seen := map[string]bool{} // the same problem in every sample is reported once
	if len(spec.IdentityModes) == 0 {
		fs = append(fs, Finding{Severity: SevError, Code: "template_identity_modes", Message: "List at least one identity mode."})
	}
	modes := append([]string(nil), spec.IdentityModes...)
	sort.SliceStable(modes, func(i, j int) bool { return modeOrder(modes[i]) < modeOrder(modes[j]) })
	for _, m := range modes {
		cases, ok := sampleCases[m]
		if !ok {
			fs = append(fs, Finding{Severity: SevError, Code: "identity_mode_not_implemented", Message: fmt.Sprintf("Identity mode %s isn't supported by templates yet.", m)})
			continue
		}
		for _, c := range cases {
			p, err := Parse([]byte(c.profile))
			if err != nil {
				panic(err)
			}
			in := SampleInput(t, SampleVariables(spec))
			d, err := Prepare(p, in)
			if err != nil {
				panic(err) // sample data always prepares
			}
			out, err := t.Execute(d)
			if err != nil {
				return nil, nil, fmt.Errorf("%w (sample %s)", err, c.label)
			}
			for _, f := range Check(out, d) {
				key := fmt.Sprintf("%s|%d|%s", f.Code, f.Line, f.Message)
				if seen[key] {
					continue
				}
				seen[key] = true
				f.Sample, f.Message = c.label, c.label+": "+f.Message
				fs = append(fs, f)
			}
		}
	}
	return t, fs, nil
}

func modeOrder(m string) int {
	switch m {
	case ModeNone:
		return 0
	case ModeLocal:
		return 1
	case ModeAD:
		return 2
	}
	return 3
}

// Errors returns the error findings.
func Errors(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Severity == SevError {
			out = append(out, f)
		}
	}
	return out
}
