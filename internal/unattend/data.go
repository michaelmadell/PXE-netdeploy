package unattend

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Mask stands for every secret value in a preview: previews never decrypt
// anything (ADR 0027).
const Mask = "********"

// Data is what a template renders from (ADR 0027; documented in the spec's
// UnattendTemplateInput.content). Field names are part of the template
// contract: renaming one breaks stored templates.
type Data struct {
	Arch          string // amd64 or arm64
	InstallMethod string // agent (smb later)
	ComputerName  string // empty: Windows picks it, or the ODJ blob names it
	OS            OSData
	Identity      IdentityData
	Client        ClientData
	Profile       ProfileData
	Deployment    DeploymentData
	Variables     map[string]string
}

// OSData is the profile's Windows settings.
type OSData struct {
	UILanguage, SystemLocale, UserLocale, InputLocale, TimeZone string
	ProductKey                                                  string // secret
}

// IdentityData is the identity mode's accounts and domain join, worked
// out from the profile.
type IdentityData struct {
	Mode               string
	HideEULA, HideOOBE bool
	// Accounts are the local accounts (local) or the break-glass
	// administrator (ad).
	Accounts  []UserAccount
	AutoLogon *AutoLogonData // nil: none
	Join      *JoinData      // ad credentials join
	ODJ       *ODJData       // ad offline join
}

// UserAccount is a local account; Password is in unattend's encoded form.
type UserAccount struct {
	Name, DisplayName, Group string
	Password                 string // secret
}

// AutoLogonData signs one account in automatically.
type AutoLogonData struct {
	Username string
	Password string // secret, encoded
	Count    int
}

// JoinData is UnattendedJoin's online join.
type JoinData struct {
	Domain, Username string
	Password         string // secret
	JoinDomain, OU   string
}

// ODJData is the offline join blob as Provisioning/AccountData takes it.
type ODJData struct {
	AccountData string // secret
}

// ClientData is the machine being deployed.
type ClientData struct {
	ID, MAC, Serial, SMBIOSUUID, Hostname string
}

// ProfileData names the profile.
type ProfileData struct{ ID, Name string }

// DeploymentData names the deployment ("preview" in previews).
type DeploymentData struct{ ID string }

// Input is everything rendering needs besides the profile.
type Input struct {
	// Arch is the unattend processorArchitecture: amd64 (default) or arm64.
	Arch string
	// InstallMethod is agent (default) or smb.
	InstallMethod string
	// ComputerName is the rendered name; nil lets Windows choose.
	ComputerName *string
	// Secrets are the decrypted values by secret id.
	Secrets map[string][]byte
	// ODJBlob is the client's djoin /provision file (identity ad, odj).
	ODJBlob []byte
	// Masked renders a preview: every secret is Mask and Secrets and
	// ODJBlob aren't read.
	Masked bool
	// Client, ProfileID and DeploymentID fill .Client, .Profile and
	// .Deployment.
	Client       ClientData
	ProfileID    string
	DeploymentID string
	// Variables are the merged variables (MergeVariables).
	Variables map[string]string
	// Template renders the file; nil is the built-in template.
	Template *Template
}

var builtinTemplate = func() *Template {
	t, err := ParseTemplate(Builtin())
	if err != nil {
		panic(err)
	}
	return t
}()

// Render builds the unattend file: Prepare, then the input's template (or
// the built-in one).
func Render(p Profile, in Input) ([]byte, error) {
	d, err := Prepare(p, in)
	if err != nil {
		return nil, err
	}
	t := in.Template
	if t == nil {
		t = builtinTemplate
	}
	if !t.Spec.Supports(p.Identity.Mode) {
		return nil, fmt.Errorf("unattend: template %q doesn't render identity mode %s", t.Spec.Name, p.Identity.Mode)
	}
	b, err := t.Execute(d)
	if err != nil {
		return nil, fmt.Errorf("unattend: %w", err)
	}
	return b, nil
}

// Prepare works out the template data for the profile's identity mode.
func Prepare(p Profile, in Input) (Data, error) {
	secret := func(id, what string) (string, error) {
		if in.Masked {
			return Mask, nil
		}
		v, ok := in.Secrets[id]
		if !ok {
			return "", fmt.Errorf("unattend: %s: secret %s not in the deployment", what, id)
		}
		return string(v), nil
	}
	encode := func(pw string) string {
		if in.Masked {
			return Mask
		}
		return encodePassword(pw)
	}
	d := Data{
		Arch: in.Arch, InstallMethod: in.InstallMethod,
		OS: OSData{UILanguage: p.OS.UILanguage, SystemLocale: p.OS.SystemLocale, UserLocale: p.OS.UserLocale,
			InputLocale: p.OS.InputLocale, TimeZone: p.OS.TimeZone},
		Identity:   IdentityData{Mode: p.Identity.Mode, HideEULA: true},
		Client:     in.Client,
		Profile:    ProfileData{ID: in.ProfileID, Name: p.Name},
		Deployment: DeploymentData{ID: in.DeploymentID},
		Variables:  in.Variables,
	}
	if d.Arch == "" {
		d.Arch = "amd64"
	}
	if d.InstallMethod == "" {
		d.InstallMethod = "agent"
	}
	if d.Variables == nil {
		d.Variables = map[string]string{}
	}
	if in.ComputerName != nil {
		d.ComputerName = *in.ComputerName
	}
	if id := p.OS.ProductKeySecretID; id != nil && *id != "" {
		k, err := secret(*id, "product key")
		if err != nil {
			return Data{}, err
		}
		d.OS.ProductKey = strings.TrimSpace(k)
	}
	id := p.Identity
	switch id.Mode {
	case ModeNone:
		d.Identity.HideEULA = id.SkipEula == nil || *id.SkipEula
	case ModeLocal:
		d.Identity.HideOOBE = id.HideOobe == nil || *id.HideOobe
		pw := map[string]string{}
		for _, a := range id.Accounts {
			s, err := secret(a.PasswordSecretID, "account "+a.Name)
			if err != nil {
				return Data{}, err
			}
			pw[a.Name] = s
			d.Identity.Accounts = append(d.Identity.Accounts, UserAccount{Name: a.Name, DisplayName: a.DisplayName, Group: a.Group, Password: encode(s)})
		}
		if al := id.AutoLogon; al != nil && al.Account != "" {
			s, ok := pw[al.Account]
			if !ok {
				return Data{}, fmt.Errorf("unattend: autoLogon account %q isn't one of the accounts", al.Account)
			}
			n := 1
			if al.Count != nil {
				n = *al.Count
			}
			d.Identity.AutoLogon = &AutoLogonData{Username: al.Account, Password: encode(s), Count: n}
		}
	case ModeAD:
		d.Identity.HideOOBE = true // users sign in with domain accounts
		if la := id.LocalAdmin; la != nil && la.Name != "" {
			s, err := secret(la.PasswordSecretID, "local administrator")
			if err != nil {
				return Data{}, err
			}
			d.Identity.Accounts = append(d.Identity.Accounts, UserAccount{Name: la.Name, Group: "Administrators", Password: encode(s)})
		}
		switch id.JoinMethod {
		case JoinODJ:
			data := Mask
			if !in.Masked {
				if len(in.ODJBlob) == 0 {
					return Data{}, fmt.Errorf("unattend: offline domain join without the client's ODJ blob")
				}
				var err error
				if data, err = ODJAccountData(in.ODJBlob); err != nil {
					return Data{}, fmt.Errorf("unattend: %w", err)
				}
			}
			d.Identity.ODJ = &ODJData{AccountData: data}
			d.ComputerName = "" // the blob names the computer
		case JoinCredentials:
			if id.JoinCredentialSecretID == nil {
				return Data{}, fmt.Errorf("unattend: ad credentials join without joinCredentialSecretId")
			}
			raw, err := secret(*id.JoinCredentialSecretID, "domain join credential")
			if err != nil {
				return Data{}, err
			}
			j := JoinData{JoinDomain: id.Domain, Domain: id.Domain, Username: Mask, Password: Mask}
			if !in.Masked {
				var c Credential
				if err := json.Unmarshal([]byte(raw), &c); err != nil {
					return Data{}, fmt.Errorf("unattend: domain join credential: %w", err)
				}
				j.Domain, j.Username = SplitAccount(c.Username, id.Domain)
				j.Password = c.Password
			}
			if id.OrganizationalUnit != nil {
				j.OU = *id.OrganizationalUnit
			}
			d.Identity.Join = &j
		}
	default:
		return Data{}, fmt.Errorf("unattend: identity mode %q isn't supported yet", id.Mode)
	}
	return d, nil
}

// MergeVariables applies template defaults < profile < client.
func MergeVariables(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, l := range layers {
		for k, v := range l {
			out[k] = v
		}
	}
	return out
}

// MissingVariables lists the template's required variables that have no
// value in vars.
func MissingVariables(t TemplateSpec, vars map[string]string) []string {
	var out []string
	for _, k := range t.RequiredVariables {
		if strings.TrimSpace(vars[k]) == "" {
			out = append(out, k)
		}
	}
	return out
}
