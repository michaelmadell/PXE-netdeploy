package unattend

// builtinContent is the built-in template. Its output must stay byte for
// byte what testdata/builtin pins (the Phase 4 unattend, ADR 0026).
const builtinContent = `<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="{{.Arch}}" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
{{- if not .Identity.ODJ}}
      <ComputerName>{{if .ComputerName}}{{.ComputerName}}{{else}}*{{end}}</ComputerName>
{{- end}}
      <TimeZone>{{.OS.TimeZone}}</TimeZone>
{{- if .OS.ProductKey}}
      <ProductKey>{{.OS.ProductKey}}</ProductKey>
{{- end}}
    </component>
{{- with .Identity.Join}}
    <component name="Microsoft-Windows-UnattendedJoin" processorArchitecture="{{$.Arch}}" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <Identification>
        <Credentials>
          <Domain>{{.Domain}}</Domain>
          <Username>{{.Username}}</Username>
          <Password>{{.Password}}</Password>
        </Credentials>
        <JoinDomain>{{.JoinDomain}}</JoinDomain>
{{- if .OU}}
        <MachineObjectOU>{{.OU}}</MachineObjectOU>
{{- end}}
      </Identification>
    </component>
{{- end}}
{{- with .Identity.ODJ}}
    <component name="Microsoft-Windows-UnattendedJoin" processorArchitecture="{{$.Arch}}" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
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
      <InputLocale>{{.OS.InputLocale}}</InputLocale>
      <SystemLocale>{{.OS.SystemLocale}}</SystemLocale>
      <UILanguage>{{.OS.UILanguage}}</UILanguage>
      <UserLocale>{{.OS.UserLocale}}</UserLocale>
    </component>
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="{{.Arch}}" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE>
        <HideEULAPage>{{.Identity.HideEULA}}</HideEULAPage>
{{- if .Identity.HideOOBE}}
        <HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideLocalAccountScreen>true</HideLocalAccountScreen>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <ProtectYourPC>3</ProtectYourPC>
{{- end}}
      </OOBE>
{{- if .Identity.Accounts}}
      <UserAccounts>
        <LocalAccounts>
{{- range .Identity.Accounts}}
          <LocalAccount wcm:action="add">
            <Name>{{.Name}}</Name>
{{- if .DisplayName}}
            <DisplayName>{{.DisplayName}}</DisplayName>
{{- end}}
            <Group>{{.Group}}</Group>
            <Password>
              <Value>{{.Password}}</Value>
              <PlainText>false</PlainText>
            </Password>
          </LocalAccount>
{{- end}}
        </LocalAccounts>
      </UserAccounts>
{{- end}}
{{- with .Identity.AutoLogon}}
      <AutoLogon>
        <Username>{{.Username}}</Username>
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
`
