// Package unattend reads what a deployment needs from its profile snapshot,
// renders the computer name (ADR 0026) and the unattend file from a
// template, the built-in one or an admin's, and checks the result (ADR
// 0027).
package unattend

import (
	"encoding/json"
	"fmt"
)

// Profile is the part of a profile snapshot (api ProfileInput) that
// identity, naming and the unattend file use.
type Profile struct {
	Name      string            `json:"name"`
	Identity  Identity          `json:"identity"`
	OS        OSSettings        `json:"osSettings"`
	Variables map[string]string `json:"variables"`
}

// Identity is every identity mode's fields; Mode says which apply.
type Identity struct {
	Mode string `json:"mode"`

	SkipEula *bool `json:"skipEula"` // none

	Accounts  []Account  `json:"accounts"` // local
	AutoLogon *AutoLogon `json:"autoLogon"`
	HideOobe  *bool      `json:"hideOobe"`

	JoinMethod             string      `json:"joinMethod"` // ad
	Domain                 string      `json:"domain"`
	OrganizationalUnit     *string     `json:"organizationalUnit"`
	JoinCredentialSecretID *string     `json:"joinCredentialSecretId"`
	LocalAdmin             *LocalAdmin `json:"localAdmin"`

	Method       string  `json:"method"` // entra
	FileSecretID *string `json:"fileSecretId"`
}

// Account is a local account (identity local).
type Account struct {
	Name             string `json:"name"`
	DisplayName      string `json:"displayName"`
	Group            string `json:"group"`
	PasswordSecretID string `json:"passwordSecretId"`
}

// AutoLogon signs one account in automatically.
type AutoLogon struct {
	Account string `json:"account"`
	Count   *int   `json:"count"`
}

// LocalAdmin is ad's optional break-glass administrator.
type LocalAdmin struct {
	Name             string `json:"name"`
	PasswordSecretID string `json:"passwordSecretId"`
}

// OSSettings is osSettings, with the spec's defaults applied by Parse.
type OSSettings struct {
	ComputerNamePattern *string `json:"computerNamePattern"`
	UILanguage          string  `json:"uiLanguage"`
	SystemLocale        string  `json:"systemLocale"`
	UserLocale          string  `json:"userLocale"`
	InputLocale         string  `json:"inputLocale"`
	TimeZone            string  `json:"timeZone"`
	ProductKeySecretID  *string `json:"productKeySecretId"`
}

// Identity modes and AD join methods.
const (
	ModeNone  = "none"
	ModeLocal = "local"
	ModeAD    = "ad"
	ModeEntra = "entra"

	JoinODJ         = "odj"
	JoinCredentials = "credentials"
)

// Parse reads a profile snapshot (or a profile's ProfileInput JSON).
func Parse(snapshot []byte) (Profile, error) {
	var p Profile
	if err := json.Unmarshal(snapshot, &p); err != nil {
		return Profile{}, fmt.Errorf("unattend: profile: %w", err)
	}
	def := func(s *string, v string) {
		if *s == "" {
			*s = v
		}
	}
	def(&p.OS.UILanguage, "en-GB")
	def(&p.OS.SystemLocale, "en-GB")
	def(&p.OS.UserLocale, "en-GB")
	def(&p.OS.InputLocale, "0809:00000809")
	def(&p.OS.TimeZone, "GMT Standard Time")
	return p, nil
}

// SecretRef is a secret a deployment uses, with the kind it must have.
type SecretRef struct {
	ID   string
	Kind string
	What string // for messages: "account admin", "product key", …
}

// Secret kinds (api SecretKind).
const (
	KindPassword         = "password"
	KindProductKey       = "product-key"
	KindDomainCredential = "domain-credential"
	KindODJBlob          = "odj-blob"
)

// Secrets lists the profile's secrets. odjBlob is the client's
// secretRefs.odjBlob, used only for ad/odj.
func (p Profile) Secrets(odjBlob *string) []SecretRef {
	var out []SecretRef
	add := func(id *string, kind, what string) {
		if id != nil && *id != "" {
			out = append(out, SecretRef{ID: *id, Kind: kind, What: what})
		}
	}
	add(p.OS.ProductKeySecretID, KindProductKey, "product key")
	id := p.Identity
	switch id.Mode {
	case ModeLocal:
		for _, a := range id.Accounts {
			add(&a.PasswordSecretID, KindPassword, "password of account "+a.Name)
		}
	case ModeAD:
		if id.LocalAdmin != nil {
			add(&id.LocalAdmin.PasswordSecretID, KindPassword, "password of local administrator "+id.LocalAdmin.Name)
		}
		switch id.JoinMethod {
		case JoinCredentials:
			add(id.JoinCredentialSecretID, KindDomainCredential, "domain join credential")
		case JoinODJ:
			add(odjBlob, KindODJBlob, "the client's ODJ blob")
		}
	}
	return out
}
