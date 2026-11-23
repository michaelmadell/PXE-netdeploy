package autoinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

// Data is what an autoinstall template renders from (ADR 0035; documented
// in the spec's UnattendTemplateInput.content). Field names are part of
// the template contract.
type Data struct {
	Arch                 string // amd64 or arm64
	Hostname             string
	Account              AccountData
	Locale               string
	KeyboardLayout       string
	Timezone             string
	StorageLayout        string // lvm or direct
	Packages             []string
	BootOrderAfterDeploy string // disk-first or network-first
	Image                ImageData
	Report               ReportData
	Client               unattend.ClientData
	Profile              unattend.ProfileData
	Deployment           unattend.DeploymentData
	Variables            map[string]string
}

// AccountData is the account the installer creates.
type AccountData struct {
	Username, RealName string
	PasswordHash       string // secret: SHA-512 crypt; empty without a password
	SSHAuthorizedKeys  []string
	SSHPasswordAuth    bool
}

// ImageData names the distribution.
type ImageData struct{ Distribution, Version string }

// ReportData are the addresses the installer reports to (ADR 0036):
// Subiquity's reporting webhook, and the hooks' start, finish and failure.
type ReportData struct{ Webhook, Started, Finished, Failed string }

// ReportURLs builds ReportData from a deployment's report base URL (ending
// in /).
func ReportURLs(base string) ReportData {
	return ReportData{Webhook: base + "events", Started: base + "started", Finished: base + "finished", Failed: base + "failed"}
}

// Install modes.
const (
	InstallAutomatic   = "automatic"
	InstallInteractive = "interactive"
)

// Settings is a Linux profile's `linux` object (the API's LinuxSettings).
type Settings struct {
	Install         string   `json:"install,omitempty"`
	HostnamePattern *string  `json:"hostnamePattern,omitempty"`
	Account         *Account `json:"account,omitempty"`
	StorageLayout   string   `json:"storageLayout,omitempty"`
	Locale          string   `json:"locale,omitempty"`
	KeyboardLayout  string   `json:"keyboardLayout,omitempty"`
	Timezone        string   `json:"timezone,omitempty"`
	Packages        []string `json:"packages,omitempty"`
}

// Account is LinuxSettings.account.
type Account struct {
	Username          string   `json:"username"`
	RealName          string   `json:"realName,omitempty"`
	PasswordSecretID  *string  `json:"passwordSecretId,omitempty"`
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`
	SSHPasswordAuth   bool     `json:"sshPasswordAuth,omitempty"`
}

// WithDefaults fills in the spec's defaults.
func (s Settings) WithDefaults() Settings {
	if s.Install == "" {
		s.Install = InstallAutomatic
	}
	if s.StorageLayout == "" {
		s.StorageLayout = "lvm"
	}
	if s.Locale == "" {
		s.Locale = "en_GB.UTF-8"
	}
	if s.KeyboardLayout == "" {
		s.KeyboardLayout = "gb"
	}
	if s.Timezone == "" {
		s.Timezone = "Europe/London"
	}
	return s
}

// Profile is what rendering needs from a Linux profile snapshot.
type Profile struct {
	ID                   string            `json:"-"`
	Name                 string            `json:"name"`
	Kind                 string            `json:"kind"`
	InstallImageID       string            `json:"installImageId"`
	Linux                *Settings         `json:"linux"`
	Variables            map[string]string `json:"variables"`
	BootOrderAfterDeploy string            `json:"bootOrderAfterDeploy"`
	UnattendTemplateID   *string           `json:"unattendTemplateId"`
}

// ParseProfile reads a profile snapshot (or ProfileInput JSON).
func ParseProfile(b []byte) (Profile, error) {
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("linux profile: %w", err)
	}
	return p, nil
}

// Automatic reports whether the profile installs unattended: it has
// `linux` settings and their install isn't interactive.
func (p Profile) Automatic() bool {
	return p.Linux != nil && p.Linux.WithDefaults().Install == InstallAutomatic
}

// Settings is the profile's settings with defaults.
func (p Profile) Settings() Settings {
	if p.Linux == nil {
		return Settings{}.WithDefaults()
	}
	return p.Linux.WithDefaults()
}

// Secrets lists the secrets an automatic install needs.
func (p Profile) Secrets() []string {
	s := p.Settings()
	if s.Account != nil && s.Account.PasswordSecretID != nil && *s.Account.PasswordSecretID != "" {
		return []string{*s.Account.PasswordSecretID}
	}
	return nil
}

// Input is the deployment-specific data.
type Input struct {
	Arch, Hostname string
	// PasswordHash is the account's crypt hash, unattend.Mask in previews,
	// or empty.
	PasswordHash string
	Image        ImageData
	// ReportBase is the deployment's report URL, ending in /.
	ReportBase              string
	Client                  unattend.ClientData
	ProfileID, DeploymentID string
	Variables               map[string]string
}

// Prepare builds a template's data.
func Prepare(p Profile, in Input) Data {
	s := p.Settings()
	d := Data{Arch: in.Arch, Hostname: in.Hostname, Locale: s.Locale, KeyboardLayout: s.KeyboardLayout, Timezone: s.Timezone,
		StorageLayout: s.StorageLayout, Packages: append([]string{}, s.Packages...), BootOrderAfterDeploy: p.BootOrderAfterDeploy,
		Image: in.Image, Report: ReportURLs(in.ReportBase), Client: in.Client, Profile: unattend.ProfileData{ID: in.ProfileID, Name: p.Name},
		Deployment: unattend.DeploymentData{ID: in.DeploymentID}, Variables: in.Variables,
		Account: AccountData{SSHAuthorizedKeys: []string{}}}
	if d.BootOrderAfterDeploy == "" {
		d.BootOrderAfterDeploy = "disk-first"
	}
	if d.Variables == nil {
		d.Variables = map[string]string{}
	}
	if a := s.Account; a != nil {
		d.Account.Username, d.Account.RealName, d.Account.SSHPasswordAuth = a.Username, a.RealName, a.SSHPasswordAuth
		d.Account.SSHAuthorizedKeys = append(d.Account.SSHAuthorizedKeys, a.SSHAuthorizedKeys...)
		d.Account.PasswordHash = in.PasswordHash
	}
	return d
}

// reservedUsers are system accounts an installed Ubuntu already has.
var reservedUsers = map[string]bool{"root": true, "daemon": true, "bin": true, "sys": true, "sync": true, "games": true, "man": true,
	"lp": true, "mail": true, "news": true, "uucp": true, "proxy": true, "www-data": true, "backup": true, "list": true, "irc": true,
	"nobody": true, "systemd-network": true, "systemd-resolve": true, "messagebus": true, "syslog": true, "sshd": true,
	"sudo": true, "adm": true, "_apt": true}

// ReservedUsername reports whether a username is a system account's.
func ReservedUsername(u string) bool { return reservedUsers[u] }

var hostnameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ErrInvalidHostname: the rendered hostname isn't a hostname label.
var ErrInvalidHostname = errors.New("invalid hostname")

// RenderHostname renders a hostname pattern (as computer name patterns)
// and lowercases it; nil gives linux- and the MAC's last 6 hex digits.
func RenderHostname(pattern *string, d unattend.NameData) (string, error) {
	var name string
	if pattern == nil || strings.TrimSpace(*pattern) == "" {
		mac := strings.NewReplacer(":", "", "-", "").Replace(d.Client.MAC)
		name = "linux-" + pick(6, mac, true)
	} else {
		t, err := unattend.ParseName(*pattern)
		if err != nil {
			return "", fmt.Errorf("hostname pattern: %w", err)
		}
		if d.Variables == nil {
			d.Variables = map[string]string{}
		}
		if unattend.PlaceholderSerial(d.Client.Serial) {
			d.Client.Serial = strings.NewReplacer(":", "", "-", "").Replace(d.Client.MAC)
		}
		var b strings.Builder
		if err := t.Execute(&b, d); err != nil {
			return "", fmt.Errorf("hostname pattern: %w", err)
		}
		name = b.String()
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if !hostnameRE.MatchString(name) {
		return name, fmt.Errorf("%w %q: 1-63 letters, digits or hyphens, no hyphen at either end", ErrInvalidHostname, name)
	}
	return name, nil
}
