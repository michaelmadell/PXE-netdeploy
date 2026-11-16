package unattend

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"text/template"
	"unicode/utf16"
)

// Input is everything Render needs besides the profile.
type Input struct {
	// Arch is the unattend processorArchitecture: amd64 or arm64.
	Arch string
	// ComputerName is the rendered name; nil lets Windows choose ("*").
	ComputerName *string
	// Secrets are the decrypted values by secret id.
	Secrets map[string][]byte
	// ODJBlob is the client's djoin /provision file (identity ad, odj).
	ODJBlob []byte
}

// Credential is a domain-credential secret's value.
type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type account struct {
	Name, DisplayName, Group, Password string
}

type view struct {
	Arch, ComputerName, TimeZone, ProductKey string
	AccountData                              string // ODJ: Setup joins offline
	UILanguage, SystemLocale, UserLocale     string
	InputLocale                              string
	HideEULA, HideOOBE                       bool
	Accounts                                 []account
	AutoLogon                                *struct {
		Username, Password string
		Count              int
	}
	Join *struct {
		Domain, Username, Password, JoinDomain, OU string
	}
}

// Render builds the unattend file for the profile's identity mode.
func Render(p Profile, in Input) ([]byte, error) {
	secret := func(id, what string) (string, error) {
		v, ok := in.Secrets[id]
		if !ok {
			return "", fmt.Errorf("unattend: %s: secret %s not in the deployment", what, id)
		}
		return string(v), nil
	}
	v := view{
		Arch: in.Arch, ComputerName: "*", TimeZone: p.OS.TimeZone,
		UILanguage: p.OS.UILanguage, SystemLocale: p.OS.SystemLocale, UserLocale: p.OS.UserLocale, InputLocale: p.OS.InputLocale,
		HideEULA: true,
	}
	if v.Arch == "" {
		v.Arch = "amd64"
	}
	if in.ComputerName != nil && *in.ComputerName != "" {
		v.ComputerName = *in.ComputerName
	}
	if id := p.OS.ProductKeySecretID; id != nil && *id != "" {
		k, err := secret(*id, "product key")
		if err != nil {
			return nil, err
		}
		v.ProductKey = strings.TrimSpace(k)
	}
	id := p.Identity
	switch id.Mode {
	case ModeNone:
		v.HideEULA = id.SkipEula == nil || *id.SkipEula
	case ModeLocal:
		v.HideOOBE = id.HideOobe == nil || *id.HideOobe
		pw := map[string]string{}
		for _, a := range id.Accounts {
			s, err := secret(a.PasswordSecretID, "account "+a.Name)
			if err != nil {
				return nil, err
			}
			pw[a.Name] = s
			v.Accounts = append(v.Accounts, account{Name: a.Name, DisplayName: a.DisplayName, Group: a.Group, Password: encodePassword(s)})
		}
		if al := id.AutoLogon; al != nil && al.Account != "" {
			s, ok := pw[al.Account]
			if !ok {
				return nil, fmt.Errorf("unattend: autoLogon account %q isn't one of the accounts", al.Account)
			}
			n := 1
			if al.Count != nil {
				n = *al.Count
			}
			v.AutoLogon = &struct {
				Username, Password string
				Count              int
			}{al.Account, encodePassword(s), n}
		}
	case ModeAD:
		v.HideOOBE = true // users sign in with domain accounts
		if la := id.LocalAdmin; la != nil && la.Name != "" {
			s, err := secret(la.PasswordSecretID, "local administrator")
			if err != nil {
				return nil, err
			}
			v.Accounts = append(v.Accounts, account{Name: la.Name, Group: "Administrators", Password: encodePassword(s)})
		}
		if id.JoinMethod == JoinODJ {
			if len(in.ODJBlob) == 0 {
				return nil, fmt.Errorf("unattend: offline domain join without the client's ODJ blob")
			}
			data, err := ODJAccountData(in.ODJBlob)
			if err != nil {
				return nil, fmt.Errorf("unattend: %w", err)
			}
			v.AccountData = data
			v.ComputerName = "" // the blob names the computer
		}
		if id.JoinMethod == JoinCredentials {
			if id.JoinCredentialSecretID == nil {
				return nil, fmt.Errorf("unattend: ad credentials join without joinCredentialSecretId")
			}
			raw, err := secret(*id.JoinCredentialSecretID, "domain join credential")
			if err != nil {
				return nil, err
			}
			var c Credential
			if err := json.Unmarshal([]byte(raw), &c); err != nil {
				return nil, fmt.Errorf("unattend: domain join credential: %w", err)
			}
			dom, user := SplitAccount(c.Username, id.Domain)
			ou := ""
			if id.OrganizationalUnit != nil {
				ou = *id.OrganizationalUnit
			}
			v.Join = &struct {
				Domain, Username, Password, JoinDomain, OU string
			}{dom, user, c.Password, id.Domain, ou}
		}
	default:
		return nil, fmt.Errorf("unattend: identity mode %q isn't supported yet", id.Mode)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, v); err != nil {
		return nil, fmt.Errorf("unattend: %w", err)
	}
	return b.Bytes(), nil
}

// SplitAccount splits DOMAIN\user or user@domain; a bare user name takes
// def as its domain.
func SplitAccount(username, def string) (domain, user string) {
	if i := strings.IndexByte(username, '\\'); i >= 0 {
		return username[:i], username[i+1:]
	}
	if i := strings.LastIndexByte(username, '@'); i >= 0 {
		return username[i+1:], username[:i]
	}
	return def, username
}

// encodePassword is unattend's password encoding (PlainText false):
// base64 of UTF-16LE(password + "Password"). It only hides the value from
// a casual look.
func encodePassword(p string) string {
	u := utf16.Encode([]rune(p + "Password"))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

var tmpl = template.Must(template.New("unattend").Funcs(template.FuncMap{"x": xmlEscape}).Parse(`<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="{{.Arch}}" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
{{- if .ComputerName}}
      <ComputerName>{{x .ComputerName}}</ComputerName>
{{- end}}
      <TimeZone>{{x .TimeZone}}</TimeZone>
{{- if .ProductKey}}
      <ProductKey>{{x .ProductKey}}</ProductKey>
{{- end}}
    </component>
{{- with .Join}}
    <component name="Microsoft-Windows-UnattendedJoin" processorArchitecture="{{$.Arch}}" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <Identification>
        <Credentials>
          <Domain>{{x .Domain}}</Domain>
          <Username>{{x .Username}}</Username>
          <Password>{{x .Password}}</Password>
        </Credentials>
        <JoinDomain>{{x .JoinDomain}}</JoinDomain>
{{- if .OU}}
        <MachineObjectOU>{{x .OU}}</MachineObjectOU>
{{- end}}
      </Identification>
    </component>
{{- end}}
{{- if .AccountData}}
    <component name="Microsoft-Windows-UnattendedJoin" processorArchitecture="{{.Arch}}" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <Identification>
        <Provisioning>
          <AccountData>{{.AccountData}}</AccountData>
        </Provisioning>
      </Identification>
    </component>
{{- end}}
  </settings>
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-International-Core" processorArchitecture="{{.Arch}}" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <InputLocale>{{x .InputLocale}}</InputLocale>
      <SystemLocale>{{x .SystemLocale}}</SystemLocale>
      <UILanguage>{{x .UILanguage}}</UILanguage>
      <UserLocale>{{x .UserLocale}}</UserLocale>
    </component>
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="{{.Arch}}" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE>
        <HideEULAPage>{{.HideEULA}}</HideEULAPage>
{{- if .HideOOBE}}
        <HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideLocalAccountScreen>true</HideLocalAccountScreen>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <ProtectYourPC>3</ProtectYourPC>
{{- end}}
      </OOBE>
{{- if .Accounts}}
      <UserAccounts>
        <LocalAccounts>
{{- range .Accounts}}
          <LocalAccount wcm:action="add">
            <Name>{{x .Name}}</Name>
{{- if .DisplayName}}
            <DisplayName>{{x .DisplayName}}</DisplayName>
{{- end}}
            <Group>{{x .Group}}</Group>
            <Password>
              <Value>{{.Password}}</Value>
              <PlainText>false</PlainText>
            </Password>
          </LocalAccount>
{{- end}}
        </LocalAccounts>
      </UserAccounts>
{{- end}}
{{- with .AutoLogon}}
      <AutoLogon>
        <Username>{{x .Username}}</Username>
        <Enabled>true</Enabled>
        <LogonCount>{{.Count}}</LogonCount>
        <Password>
          <Value>{{.Password}}</Value>
          <PlainText>false</PlainText>
        </Password>
      </AutoLogon>
{{- end}}
    </component>
  </settings>
</unattend>
`))
